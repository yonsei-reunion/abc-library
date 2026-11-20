// Package extract는 Claude로 밴드 게시글과 댓글에서 책을 찾고, 개인 정보를 뺀 언급 요약을 만든다.
// Claude가 교보문고 검색 도구로 ISBN을 고르고, 코드는 검색 결과에 실제로 있었던 ISBN만 받아들인다.
// 글에 교보문고 상품 링크가 있으면 코드가 그 상품을 먼저 찾아서 Claude에게 링크와 함께 보여 준다.
// 언급에 들어 있던 링크는 서점 구매 링크와 Claude가 그 책과 직접 관련이 없다고 판정한 링크만 빼고 코드가 모두 붙인다.
package extract

import (
	"context"
	"encoding/base64"
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
)

const (
	// maxTurns는 글 하나에서 Claude를 부르는 횟수(search_kyobo 호출 포함)의 상한이다.
	maxTurns = 8
	maxTags  = 5
)

// ErrRefused는 Claude가 게시글 처리를 거절했다는 뜻이다.
var ErrRefused = errors.New("claude가 게시글 처리를 거절했습니다")

// Image는 게시글에 첨부된 사진이다.
type Image struct {
	MediaType string
	Data      []byte
}

// Comment는 게시글의 댓글 하나다. 작성자는 담지 않는다.
type Comment struct {
	At    time.Time
	Text  string
	Links []book.Link
}

// Input은 추출할 게시글의 내용이다.
type Input struct {
	PostedAt time.Time
	Text     string
	// Links는 본문의 링크다.
	Links    []book.Link
	Images   []Image
	Comments []Comment
}

// Extractor는 게시글 하나에서 책과 언급 요약을 뽑는다.
type Extractor struct {
	api    claude.MessageAPI
	search claude.Searcher
	tags   book.TagList
}

// New는 Extractor를 만든다. 책에 붙일 태그는 tags에서만 고른다.
func New(api claude.MessageAPI, search claude.Searcher, tags book.TagList) *Extractor {
	return &Extractor{api: api, search: search, tags: tags}
}

// result는 Claude의 답이다. 스키마는 resultSchema에 있다.
type result struct {
	Books []resultBook `json:"books"`
}

type resultBook struct {
	Title   string   `json:"title"`
	Authors []string `json:"authors"`
	ISBN13  string   `json:"isbn13"`
	Tags    []string `json:"tags"`
	Post    struct {
		Summary string `json:"summary"`
		Links   []int  `json:"links"`
	} `json:"post"`
	Comments []struct {
		Comment int    `json:"comment"`
		Summary string `json:"summary"`
	} `json:"comments"`
	UnrelatedLinks []int  `json:"unrelated_links"`
	KyoboPID       string `json:"kyobo_pid"`
}

// Extract는 게시글(본문과 댓글)에서 찾은 책과, 그 책에 대한 언급 요약을 돌려준다.
func (e *Extractor) Extract(ctx context.Context, in Input) ([]book.Finding, error) {
	eds := claude.FindLinked(ctx, e.search, linkURLs(in))
	text, err := claude.Ask(ctx, e.api, eds, claude.Request{
		System:   systemPrompt(e.tags),
		Schema:   resultSchema(e.tags),
		Content:  userContent(in, eds),
		MaxTurns: maxTurns,
	})
	if errors.Is(err, claude.ErrRefused) {
		return nil, ErrRefused
	}
	if err != nil {
		return nil, err
	}
	var r result
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, fmt.Errorf("추출 결과를 읽지 못했습니다: %w", err)
	}
	return e.findings(r, in, eds), nil
}

// linkURLs는 본문과 댓글의 링크 주소다.
func linkURLs(in Input) []string {
	var urls []string
	for _, l := range in.Links {
		urls = append(urls, l.URL)
	}
	for _, c := range in.Comments {
		for _, l := range c.Links {
			urls = append(urls, l.URL)
		}
	}
	return urls
}

func userContent(in Input, eds *claude.Editions) []anthropic.BetaContentBlockParamUnion {
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, img := range in.Images {
		blocks = append(blocks, anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
			MediaType: anthropic.BetaBase64ImageSourceMediaType(img.MediaType),
			Data:      base64.StdEncoding.EncodeToString(img.Data),
		}))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "게시일: %s\n\n## 본문\n%s\n\n## 본문 링크\n", in.PostedAt.Format("2006-01-02"), strings.TrimSpace(in.Text))
	if len(in.Links) == 0 {
		b.WriteString("(없음)\n")
	}
	for i, l := range in.Links {
		fmt.Fprintf(&b, "[%d] %s %s%s\n", i+1, l.Title, claude.ReadableURL(l.URL), eds.ProductNote(l.URL))
	}
	b.WriteString("\n## 댓글\n")
	if len(in.Comments) == 0 {
		b.WriteString("(없음)\n")
	}
	for i, c := range in.Comments {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i+1, c.At.Format("2006-01-02"), strings.TrimSpace(c.Text))
		for j, l := range c.Links {
			fmt.Fprintf(&b, "    링크 [%d] %s %s%s\n", commentLinkNo(in, i, j), l.Title, claude.ReadableURL(l.URL), eds.ProductNote(l.URL))
		}
	}
	fmt.Fprintf(&b, "\n## 사진\n첨부 사진 %d장을 함께 보냈다.\n", len(in.Images))
	return append(blocks, anthropic.NewBetaTextBlock(b.String()))
}

// commentLinkNo는 i번째 댓글(0부터)의 j번째 링크(0부터)에 붙이는 번호다. 본문 링크 번호 다음부터 이어진다.
func commentLinkNo(in Input, i, j int) int {
	no := len(in.Links)
	for _, c := range in.Comments[:i] {
		no += len(c.Links)
	}
	return no + j + 1
}

// candidate는 언급을 만들 책이다. raw는 Claude 답에서의 순서다.
type candidate struct {
	book   book.Book
	raw    resultBook
	linked bool
}

// findings는 Claude의 답을 검증해서 책과 언급으로 바꾼다.
func (e *Extractor) findings(r result, in Input, eds *claude.Editions) []book.Finding {
	candidates := e.candidates(r, eds)
	// 본문이 책 한 권만 다뤘으면 본문 링크는 모두 그 책의 언급이다.
	onlyBookInPost := countFunc(candidates, func(c candidate) bool { return strings.TrimSpace(c.raw.Post.Summary) != "" }) == 1
	var findings []book.Finding
	for _, c := range candidates {
		unrelated := map[int]bool{}
		for _, no := range c.raw.UnrelatedLinks {
			unrelated[no] = true
		}
		var mentions []book.Mention
		if m, ok := postMention(c.raw, in, onlyBookInPost, unrelated); ok {
			mentions = append(mentions, m)
		}
		mentions = append(mentions, commentMentions(c.raw, in, unrelated)...)
		if len(mentions) == 0 {
			continue
		}
		findings = append(findings, book.Finding{Book: c.book, Mentions: mentions, Linked: c.linked})
	}
	return findings
}

// candidates는 Claude가 찾은 책에 판본을 적용하고, 같은 작품이 두 번 나오면 처음 것만 남긴다.
// 상품 링크와 검색 결과에 없던 상품 번호와 ISBN은 Claude가 지어냈을 수 있으므로 버린다.
func (e *Extractor) candidates(r result, eds *claude.Editions) []candidate {
	var out []candidate
	for _, rb := range r.Books {
		b := book.Book{Title: strings.TrimSpace(rb.Title), Authors: trimAll(rb.Authors), Tags: pickTags(rb.Tags, e.tags)}
		ed, linked, ok := eds.Resolve(rb.KyoboPID, rb.ISBN13)
		if ok {
			ed.ApplyTo(&b)
		}
		if b.Title == "" || slices.ContainsFunc(out, func(c candidate) bool { return book.SameWork(c.book, c.linked, b, linked) }) {
			continue
		}
		out = append(out, candidate{book: b, raw: rb, linked: linked})
	}
	return out
}

// postMention은 본문이 이 책에 대해 한 이야기다. 본문이 다루지 않았으면 false다.
func postMention(rb resultBook, in Input, onlyBookInPost bool, unrelated map[int]bool) (book.Mention, bool) {
	summary := strings.TrimSpace(rb.Post.Summary)
	if summary == "" {
		return book.Mention{}, false
	}
	ids := rb.Post.Links
	if onlyBookInPost {
		ids = nil
		for i := range in.Links {
			ids = append(ids, i+1)
		}
	}
	ids = slices.DeleteFunc(ids, func(id int) bool { return unrelated[id] })
	return book.Mention{Kind: book.InPost, At: in.PostedAt, Summary: summary, Links: pickLinks(in.Links, ids)}, true
}

// commentMentions는 댓글마다 이 책에 대해 한 이야기다. 없는 댓글 번호와 같은 댓글의 두 번째 요약은 뺀다.
// 댓글의 링크는 Claude가 관련 없다고 한 것만 빼고 모두 붙인다.
func commentMentions(rb resultBook, in Input, unrelated map[int]bool) []book.Mention {
	var mentions []book.Mention
	used := map[int]bool{}
	for _, rc := range rb.Comments {
		summary := strings.TrimSpace(rc.Summary)
		if rc.Comment < 1 || rc.Comment > len(in.Comments) || used[rc.Comment] || summary == "" {
			continue
		}
		used[rc.Comment] = true
		c := in.Comments[rc.Comment-1]
		var ids []int
		for j := range c.Links {
			if !unrelated[commentLinkNo(in, rc.Comment-1, j)] {
				ids = append(ids, j+1)
			}
		}
		mentions = append(mentions, book.Mention{Kind: book.InComment, At: c.At, Summary: summary, Links: pickLinks(c.Links, ids)})
	}
	return mentions
}

func countFunc[T any](xs []T, f func(T) bool) int {
	n := 0
	for _, x := range xs {
		if f(x) {
			n++
		}
	}
	return n
}

// pickLinks는 1부터 시작하는 번호로 링크를 고른다. 서점 구매 링크와 잘못된 번호는 뺀다.
func pickLinks(links []book.Link, ids []int) []book.Link {
	var out []book.Link
	seen := map[string]bool{}
	for _, id := range ids {
		if id < 1 || id > len(links) {
			continue
		}
		l := links[id-1]
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || seen[l.URL] || book.IsStoreLink(u) {
			continue
		}
		seen[l.URL] = true
		title := strings.TrimSpace(l.Title)
		if title == "" {
			title = u.Host
		}
		out = append(out, book.Link{URL: l.URL, Title: title})
	}
	return out
}

// pickTags는 목록(allowed)에 있는 태그만 중복 없이 최대 maxTags개 남긴다.
// SF 책에는 과학 태그를 붙이지 않는다. 모임에서 과학 태그는 과학 교양서를 가리킨다.
func pickTags(ts []string, allowed book.TagList) []string {
	sf := slices.Contains(ts, "SF")
	var out []string
	for _, t := range ts {
		if sf && t == "과학" {
			continue
		}
		if allowed.Has(t) && !slices.Contains(out, t) && len(out) < maxTags {
			out = append(out, t)
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
