// Package weekly는 카카오톡 대화방의 한 주 대화를 Claude로 정리해서 밴드에 붙여 넣을 '대화방 주간 요약'을 만든다.
// Claude에게는 메시지 본문과 보낸 시각만 보내고 작성자는 보내지 않는다.
// 링크는 코드가 본문에서 모두 뽑아서 책이나 링크 모음에 하나씩 넣으므로 요약에서 빠지지 않는다.
// 책의 판본은 extract와 같이 Claude가 search_kyobo 도구(internal/claude)로 고르고, 코드는 검색 결과나 상품 링크에 있었던 판본만 받아들인다.
package weekly

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/claude"
	"github.com/yonsei-alumni/abc-library/internal/kakao"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

const (
	// maxTurns는 Claude를 부르는 횟수(search_kyobo 호출 포함)의 상한이다. 한 주의 대화에는 책이 많아서 게시글 추출보다 넉넉히 둔다.
	maxTurns = 12
	// maxMessageRunes는 메시지 하나에서 Claude에게 보내는 글자 수의 상한이다.
	maxMessageRunes = 3000
	keywordCount    = 10
)

// ErrRefused는 Claude가 요약을 거절했다는 뜻이다.
var ErrRefused = errors.New("claude가 대화방 요약을 거절했습니다")

// BookFinder는 사이트에 있는 책을 찾는다. *store.Store가 구현한다.
type BookFinder interface {
	FindBook(ctx context.Context, b book.Book) (id int64, ok bool, err error)
}

// Summarizer는 대화방 주간 요약을 만든다.
type Summarizer struct {
	api    claude.MessageAPI
	search claude.Searcher
	titles TitleFetcher
	books  BookFinder
	site   string
}

// New는 Summarizer를 만든다. titles가 nil이면 링크 제목을 읽지 않고, books가 nil이면 사이트의 책 링크를 붙이지 않는다.
// siteURL은 책 상세 화면 주소의 앞부분(https://abc-lib.web.app)이다.
func New(api claude.MessageAPI, search claude.Searcher, titles TitleFetcher, books BookFinder, siteURL string) *Summarizer {
	return &Summarizer{api: api, search: search, titles: titles, books: books, site: strings.TrimRight(siteURL, "/")}
}

// Input은 요약할 기간과 메시지다. 메시지는 kakao.Week로 거르고 정렬한 것이다.
type Input struct {
	Room     string
	Start    string
	End      string
	Messages []kakao.Message
}

// Summary는 정리한 결과다. Text가 밴드에 붙여 넣을 글을 만든다.
type Summary struct {
	Room     string
	Start    string
	End      string
	Count    int
	Books    []Book
	Links    []SharedLink
	Keywords []string
}

// Book은 대화에서 공유되거나 추천된 책이다.
type Book struct {
	Title   string
	Authors []string
	Context []string
	// Links는 이 책과 관련된 링크 중 서점 링크를 뺀 것이다. 구매 링크는 교보문고로 통일한다.
	Links []Link
	// Purchase는 교보문고 상품 주소다. 상품을 확정하지 못하면 교보문고 검색 주소이고 Searched가 참이다.
	Purchase string
	Searched bool
	// Page는 사이트의 책 상세 화면 주소다. 사이트에 없는 책이면 비어 있다.
	Page string
}

// SharedLink는 어느 책에도 넣지 않은 링크다.
type SharedLink struct {
	Link
	Context string
}

// result는 Claude의 답이다. 스키마는 resultSchema에 있다.
type result struct {
	Books    []resultBook `json:"books"`
	Links    []resultLink `json:"links"`
	Keywords []string     `json:"keywords"`
}

type resultBook struct {
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	ISBN13   string   `json:"isbn13"`
	KyoboPID string   `json:"kyobo_pid"`
	Context  []string `json:"context"`
	Links    []int    `json:"links"`
}

type resultLink struct {
	Link    int    `json:"link"`
	Title   string `json:"title"`
	Context string `json:"context"`
}

// Summarize는 한 주의 메시지를 정리한다. 메시지가 없으면 Claude를 부르지 않는다.
func (s *Summarizer) Summarize(ctx context.Context, in Input) (Summary, error) {
	sum := Summary{Room: in.Room, Start: in.Start, End: in.End, Count: len(in.Messages)}
	if len(in.Messages) == 0 {
		return sum, nil
	}
	links := fetchTitles(ctx, s.titles, ExtractURLs(in.Messages))
	urls := make([]string, len(links))
	for i, l := range links {
		urls[i] = l.URL
	}
	eds := claude.FindLinked(ctx, s.search, urls)
	text, err := claude.Ask(ctx, s.api, eds, claude.Request{
		System:   systemPrompt,
		Schema:   resultSchema(),
		Content:  []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(userText(in, links, eds))},
		MaxTurns: maxTurns,
	})
	if errors.Is(err, claude.ErrRefused) {
		return sum, ErrRefused
	}
	if err != nil {
		return sum, err
	}
	var r result
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return sum, fmt.Errorf("요약 결과를 읽지 못했습니다: %w", err)
	}
	var used []bool
	sum.Books, used = s.pickBooks(ctx, r.Books, links, eds)
	sum.Links = sharedLinks(r.Links, links, used)
	sum.Keywords = keywords(r.Keywords)
	return sum, nil
}

var weekdays = []string{"일", "월", "화", "수", "목", "금", "토"}

// userText는 Claude에게 보내는 메시지와 링크 목록이다. 작성자는 넣지 않는다.
func userText(in Input, links []Link, eds *claude.Editions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "대화방: %s\n기간: %s ~ %s\n\n## 메시지\n", in.Room, in.Start, in.End)
	for i, m := range in.Messages {
		at := m.At().In(book.Seoul)
		text := strings.TrimSpace(m.Text)
		if r := []rune(text); len(r) > maxMessageRunes {
			text = string(r[:maxMessageRunes]) + "…(생략)"
		}
		text = strings.ReplaceAll(text, "\n", "\n    ")
		fmt.Fprintf(&b, "[%d] %s(%s) %s %s\n", i+1, at.Format("01-02"), weekdays[at.Weekday()], clock(m, at), text)
	}
	b.WriteString("\n## 링크\n")
	if len(links) == 0 {
		b.WriteString("(없음)\n")
	}
	for i, l := range links {
		fmt.Fprintf(&b, "[L%d] %s", i+1, claude.ReadableURL(l.URL))
		if l.Title != "" {
			fmt.Fprintf(&b, " - %s", l.Title)
		}
		b.WriteString(eds.ProductNote(l.URL) + "\n")
	}
	return b.String()
}

// clock은 메시지의 시각이다. 폰이 시각을 읽지 못한 메시지는 추정한 시각이므로 ~를 붙인다.
func clock(m kakao.Message, at time.Time) string {
	if m.Time != "" && m.Date != "" {
		return at.Format("15:04")
	}
	return "~" + at.Format("15:04")
}

// pickBooks는 Claude가 찾은 책을 검증해서 요약의 책으로 바꾼다. used는 links 중 어느 책에 붙은 링크다.
//   - 상품 링크와 검색 결과에 없던 상품 번호, ISBN은 버린다.
//   - 같은 작품이 두 번 나오면 처음 것만 쓴다. 상품 링크로 가리킨 판본은 ISBN이 다르면 다른 책이다.
//   - 링크 번호는 처음 나온 책 하나에만 붙인다. 책에 붙은 서점 링크는 교보문고 구매 링크로 대신한다.
func (s *Summarizer) pickBooks(ctx context.Context, rbs []resultBook, links []Link, eds *claude.Editions) ([]Book, []bool) {
	used := make([]bool, len(links))
	type picked struct {
		book   book.Book
		linked bool
	}
	var seen []picked
	var out []Book
	for _, rb := range rbs {
		b := book.Book{Title: strings.TrimSpace(rb.Title), Authors: trimAll(rb.Authors)}
		ed, linked, ok := eds.Resolve(rb.KyoboPID, rb.ISBN13)
		if ok {
			ed.ApplyTo(&b)
		}
		if b.Title == "" || slices.ContainsFunc(seen, func(p picked) bool { return book.SameWork(p.book, p.linked, b, linked) }) {
			continue
		}
		seen = append(seen, picked{book: b, linked: linked})

		sb := Book{Title: b.Title, Authors: b.Authors, Context: trimAll(rb.Context)}
		sb.Purchase, sb.Searched = purchaseLink(b)
		for _, no := range rb.Links {
			if no < 1 || no > len(links) || used[no-1] {
				continue
			}
			used[no-1] = true
			if u, err := url.Parse(links[no-1].URL); err == nil && book.IsStoreLink(u) {
				continue
			}
			sb.Links = append(sb.Links, links[no-1])
		}
		sb.Page = s.page(ctx, b)
		out = append(out, sb)
	}
	return out, used
}

// purchaseLink는 책의 교보문고 구매 링크다. 상품을 확정하지 못했으면 제목과 첫 저자로 찾는 검색 주소이고 searched가 참이다.
func purchaseLink(b book.Book) (link string, searched bool) {
	if b.KyoboPID != "" {
		return kyobo.ProductURL(b.KyoboPID), false
	}
	query := b.Title
	if len(b.Authors) > 0 {
		query += " " + b.Authors[0]
	}
	return kyobo.SearchURL(strings.TrimSpace(query)), true
}

// page는 사이트의 책 상세 화면 주소다. 사이트에 없는 책이면 빈 문자열이다.
// 사이트 링크는 덤이라서 조회에 실패해도 요약은 보낸다.
func (s *Summarizer) page(ctx context.Context, b book.Book) string {
	if s.books == nil || s.site == "" {
		return ""
	}
	id, found, err := s.books.FindBook(ctx, b)
	if err != nil || !found {
		return ""
	}
	return fmt.Sprintf("%s/books/%d", s.site, id)
}

// sharedLinks는 어느 책에도 붙지 않은 링크를 모두 링크 모음에 넣는다. Claude가 붙인 제목과 설명이 있으면 쓴다.
func sharedLinks(notes []resultLink, links []Link, used []bool) []SharedLink {
	byNo := map[int]SharedLink{}
	for _, rl := range notes {
		if _, dup := byNo[rl.Link]; !dup {
			byNo[rl.Link] = SharedLink{Link: Link{Title: strings.TrimSpace(rl.Title)}, Context: strings.TrimSpace(rl.Context)}
		}
	}
	var out []SharedLink
	for i, l := range links {
		if used[i] {
			continue
		}
		sl := byNo[i+1]
		sl.URL = l.URL
		if sl.Title == "" {
			sl.Title = l.Title
		}
		out = append(out, sl)
	}
	return out
}

// keywords는 핵심 키워드를 중복 없이 keywordCount개까지 남긴다.
func keywords(ks []string) []string {
	var out []string
	for _, k := range trimAll(ks) {
		if !slices.Contains(out, k) && len(out) < keywordCount {
			out = append(out, k)
		}
	}
	return out
}

func trimAll(ss []string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
