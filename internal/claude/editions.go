package claude

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

// maxLinkedProducts는 요청 하나에서 상품 번호로 찾아보는 교보문고 상품 링크 수의 상한이다.
const maxLinkedProducts = 8

// Editions는 요청 하나에서 Claude가 고를 수 있는 판본이다. 글에 있던 교보문고 상품 링크의 판본과 search_kyobo 결과에 나온 판본만 담는다.
// 여기에 없는 상품 번호와 ISBN은 Claude가 지어냈을 수 있으므로 받아들이지 않는다.
type Editions struct {
	search Searcher
	// byPID는 상품 링크로 찾은 판본이다. 키는 상품 번호다.
	byPID map[string]kyobo.Edition
	// byISBN은 받아들일 수 있는 모든 판본이다. 키는 ISBN이다.
	byISBN map[string]kyobo.Edition
}

// FindLinked는 urls 중 교보문고 상품 링크가 가리키는 판본을 상품 번호로 찾는다. 잡지처럼 ISBN이 없는 상품도 넣는다.
// 찾지 못한 링크는 Claude가 평소처럼 제목으로 검색한다. 상품 링크의 판본은 검색 결과로 치고, 검색에서 같은 ISBN이 나와도 링크의 상품을 쓴다.
func FindLinked(ctx context.Context, search Searcher, urls []string) *Editions {
	e := &Editions{search: search, byPID: map[string]kyobo.Edition{}, byISBN: map[string]kyobo.Edition{}}
	tried := map[string]bool{}
	for _, u := range urls {
		pid, ok := kyobo.ProductPID(u)
		if !ok || tried[pid] || len(tried) >= maxLinkedProducts {
			continue
		}
		tried[pid] = true
		// 상품을 찾지 못해도 Claude가 제목으로 검색할 수 있으므로 오류는 넘어간다.
		if ed, ok, err := search.Product(ctx, pid); err == nil && ok {
			e.byPID[pid] = ed
			if ed.ISBN13 != "" {
				e.byISBN[ed.ISBN13] = ed
			}
		}
	}
	return e
}

// Resolve는 Claude가 고른 상품 번호나 ISBN에 맞는 판본을 찾는다. 상품 번호를 먼저 본다.
// linked는 그 판본이 글의 상품 링크가 가리킨 것인지 알려 준다.
func (e *Editions) Resolve(pid, isbn string) (ed kyobo.Edition, linked, ok bool) {
	ed, ok = e.byPID[pid]
	if !ok && isbn != "" {
		ed, ok = e.byISBN[isbn]
	}
	if ok {
		_, linked = e.byPID[ed.PID]
	}
	return ed, linked, ok
}

// ProductNote는 Claude에게 보여 줄 때 상품 링크 뒤에 붙이는 그 상품의 판본 정보다. 프롬프트는 이 문구('→ 교보문고 상품')를 근거로
// 상품 번호를 고르게 한다. 상품 링크가 아니거나 찾지 못한 상품이면 빈 문자열이다.
func (e *Editions) ProductNote(rawURL string) string {
	pid, ok := kyobo.ProductPID(rawURL)
	if !ok {
		return ""
	}
	ed, ok := e.byPID[pid]
	if !ok {
		return ""
	}
	isbn := ed.ISBN13
	if isbn == "" {
		isbn = "없음"
	}
	return fmt.Sprintf(" → 교보문고 상품: 상품 번호 %s, isbn13 %s, %s, %s, %s", ed.PID, isbn, ed.Title, strings.Join(ed.Authors, ", "), ed.Publisher)
}

// ReadableURL은 퍼센트 인코딩된 주소를 사람이 읽을 수 있게 푼다. 독서 기록 주소처럼 제목 없는 링크는
// 주소에 든 책 제목(예: /연희동-러너/)이 Claude가 책을 알아볼 유일한 단서다. 저장하는 주소는 바꾸지 않는다.
func ReadableURL(raw string) string {
	if s, err := url.PathUnescape(raw); err == nil {
		return s
	}
	return raw
}
