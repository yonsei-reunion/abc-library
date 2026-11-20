package claude

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

// productSearch는 상품 번호 검색만 흉내 낸다. S로 시작하는 상품 번호는 찾고, E000000000404는 오류를 낸다.
type productSearch struct{ queries []string }

func (p *productSearch) Search(context.Context, string) ([]kyobo.Edition, error) { return nil, nil }

func (p *productSearch) Product(_ context.Context, pid string) (kyobo.Edition, bool, error) {
	p.queries = append(p.queries, pid)
	switch {
	case pid == "E000000000404":
		return kyobo.Edition{}, false, errors.New("검색 실패")
	case strings.HasPrefix(pid, "S000000000"):
		return kyobo.Edition{PID: pid, ISBN13: "97889" + pid[len(pid)-8:], Title: "책 " + pid}, true, nil
	}
	return kyobo.Edition{}, false, nil
}

func TestFindLinkedLimitsLookups(t *testing.T) {
	var urls []string
	for i := range maxLinkedProducts + 3 {
		urls = append(urls, fmt.Sprintf("https://product.kyobobook.co.kr/detail/S%012d", i+1))
	}
	urls = append([]string{urls[0], "https://blog.example.com/x"}, urls...)
	search := &productSearch{}
	FindLinked(context.Background(), search, urls)
	if len(search.queries) != maxLinkedProducts {
		t.Errorf("상품 번호 검색은 같은 상품을 빼고 %d번까지만 해야 한다: %v", maxLinkedProducts, search.queries)
	}
}

func TestEditionsResolve(t *testing.T) {
	eds := FindLinked(context.Background(), &productSearch{}, []string{
		"https://product.kyobobook.co.kr/detail/S000000000001",
		"https://ebook-product.kyobobook.co.kr/dig/epd/ebook/E000000000404",
	})
	eds.byISBN["9788900000002"] = kyobo.Edition{PID: "S000000000099", ISBN13: "9788900000002", Title: "검색한 책"}

	if ed, linked, ok := eds.Resolve("S000000000001", ""); !ok || !linked || ed.Title != "책 S000000000001" {
		t.Errorf("상품 링크의 판본을 상품 번호로 찾아야 한다: %+v %v %v", ed, linked, ok)
	}
	if ed, linked, ok := eds.Resolve("", "9788900000001"); !ok || !linked || ed.PID != "S000000000001" {
		t.Errorf("상품 링크의 판본은 ISBN으로 찾아도 링크의 판본이다: %+v %v %v", ed, linked, ok)
	}
	if ed, linked, ok := eds.Resolve("", "9788900000002"); !ok || linked || ed.Title != "검색한 책" {
		t.Errorf("검색 결과의 판본은 링크의 판본이 아니다: %+v %v %v", ed, linked, ok)
	}
	for _, c := range [][2]string{{"S000000000777", ""}, {"", "9788999999999"}, {"E000000000404", ""}, {"", ""}} {
		if _, _, ok := eds.Resolve(c[0], c[1]); ok {
			t.Errorf("링크와 검색 결과에 없던 판본 %v를 받아들였다", c)
		}
	}
}

func TestProductNote(t *testing.T) {
	eds := &Editions{byPID: map[string]kyobo.Edition{
		"S000000000001": {PID: "S000000000001", Title: "잡지", Authors: []string{"편집부"}, Publisher: "출판사"},
	}}
	if got := eds.ProductNote("https://product.kyobobook.co.kr/detail/S000000000001"); got != " → 교보문고 상품: 상품 번호 S000000000001, isbn13 없음, 잡지, 편집부, 출판사" {
		t.Errorf("ProductNote = %q", got)
	}
	for _, u := range []string{"https://product.kyobobook.co.kr/detail/S000000000002", "https://blog.example.com/x", "::"} {
		if got := eds.ProductNote(u); got != "" {
			t.Errorf("찾지 못한 상품이나 상품이 아닌 링크 %s에는 붙이지 않는다: %q", u, got)
		}
	}
}

func TestReadableURL(t *testing.T) {
	if got := ReadableURL("https://blog.example.com/%EC%97%B0%ED%9D%AC%EB%8F%99-%EB%9F%AC%EB%84%88/"); got != "https://blog.example.com/연희동-러너/" {
		t.Errorf("ReadableURL = %q", got)
	}
	if got := ReadableURL("https://x.com/%zz"); got != "https://x.com/%zz" {
		t.Errorf("풀 수 없는 주소는 그대로 둬야 한다: %q", got)
	}
}
