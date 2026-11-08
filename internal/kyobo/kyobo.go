// Package kyobo는 교보문고 검색 결과에서 책의 판본 정보를 읽고, 상품과 표지 이미지의 주소를 만든다.
// 교보문고 robots.txt는 검색과 상품 페이지 접근을 허용한다(/api/ 경로만 금지).
// 요청은 새로 등록되는 책마다 몇 번으로 제한하고 결과는 DB에 저장해서 다시 묻지 않는다.
package kyobo

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/book"
)

const userAgent = "abc-library/0.1 (+https://github.com/yonsei-reunion/abc-library)"

// Edition은 교보문고 검색 결과의 판본 하나다. 잡지처럼 ISBN-13이 없는 상품은 ISBN13이 비어 있다.
type Edition struct {
	PID string `json:"-"`
	// Barcode는 상품의 바코드다. 일반 도서는 ISBN-13과 같고, 표지 이미지 주소에 쓴다.
	Barcode   string   `json:"-"`
	ISBN13    string   `json:"isbn13"`
	Title     string   `json:"title"`
	Authors   []string `json:"authors"`
	Publisher string   `json:"publisher"`
	PubDate   string   `json:"pub_date"`
	Category  string   `json:"category"`
}

// Client는 교보문고 통합 검색을 호출한다. 요청 사이에 MinInterval만큼 쉰다.
type Client struct {
	HTTP        *http.Client
	BaseURL     string
	MinInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewClient는 기본 설정의 Client를 만든다.
func NewClient() *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 15 * time.Second},
		BaseURL:     "https://search.kyobobook.co.kr/search",
		MinInterval: 2 * time.Second,
	}
}

// Search는 검색어로 국내외 도서 판본을 찾는다. ISBN-13이 없는 상품(eBook 등)은 뺀다.
func (c *Client) Search(ctx context.Context, query string) ([]Edition, error) {
	page, err := c.fetch(ctx, query)
	if err != nil {
		return nil, err
	}
	return ParseSearch(page), nil
}

// Product는 상품 번호로 검색해서 그 상품을 찾는다. 상품 번호로 검색하면 그 상품이 나온다.
// 잡지처럼 ISBN-13이 없는 상품도 돌려준다. 찾지 못하면 ok가 거짓이다.
func (c *Client) Product(ctx context.Context, pid string) (ed Edition, ok bool, err error) {
	page, err := c.fetch(ctx, pid)
	if err != nil {
		return Edition{}, false, err
	}
	for _, ed := range parseItems(page) {
		if ed.PID == pid {
			return ed, true, nil
		}
	}
	return Edition{}, false, nil
}

// fetch는 통합 검색 결과 HTML을 받는다.
func (c *Client) fetch(ctx context.Context, query string) (string, error) {
	c.wait()
	q := url.Values{"keyword": {query}, "gbCode": {"TOT"}, "target": {"total"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("교보문고 검색 요청 실패: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("교보문고 검색 응답 코드 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	return string(body), err
}

func (c *Client) wait() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.MinInterval - time.Since(c.last); d > 0 {
		time.Sleep(d)
	}
	c.last = time.Now()
}

var (
	itemStart = regexp.MustCompile(`<li class="prod_item"`)
	comment   = regexp.MustCompile(`(?s)<!--.*?-->`)
	pidRe     = regexp.MustCompile(`data-pid="([A-Z]\d+)"`)
	// 전자책 바코드에는 영문자가 들어 있다(예: 480D180102360).
	bidRe    = regexp.MustCompile(`data-bid="([0-9A-Za-z]+)"`)
	titleRe  = regexp.MustCompile(`<span id="cmdtName_[A-Z]\d+">([^<]+)</span>`)
	catRe    = regexp.MustCompile(`<span class="prod_category">\[([^\]]+)\]</span>`)
	authorRe = regexp.MustCompile(`class="author[^"]*">([^<]+)</a>\s*<span class="type">([^<]+)</span>`)
	pubRe    = regexp.MustCompile(`pbcm[^>]*>([^<]+)</a>`)
	dateRe   = regexp.MustCompile(`<span class="date">\s*([^<]+?)\s*</span>`)
)

// ParseSearch는 검색 결과 HTML에서 ISBN-13이 있는 판본 목록을 읽는다.
func ParseSearch(page string) []Edition {
	var eds []Edition
	seen := map[string]bool{}
	for _, ed := range parseItems(page) {
		if ed.ISBN13 == "" || seen[ed.ISBN13] {
			continue
		}
		seen[ed.ISBN13] = true
		eds = append(eds, ed)
	}
	return eds
}

// parseItems는 검색 결과 HTML의 상품을 모두 읽는다. 바코드가 ISBN-13이 아니면 ISBN13을 비운다.
func parseItems(page string) []Edition {
	starts := itemStart.FindAllStringIndex(page, -1)
	var eds []Edition
	for i, s := range starts {
		end := len(page)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		// 저자 이름과 역할 사이에 HTML 주석이 끼어 있어서 주석을 먼저 지운다.
		item := comment.ReplaceAllString(page[s[0]:end], "")
		ed := Edition{
			PID:       first(pidRe, item),
			Barcode:   first(bidRe, item),
			Title:     first(titleRe, item),
			Category:  first(catRe, item),
			Publisher: first(pubRe, item),
			PubDate:   first(dateRe, item),
		}
		for _, m := range authorRe.FindAllStringSubmatch(item, -1) {
			if isAuthorRole(m[2]) {
				ed.Authors = append(ed.Authors, strings.TrimSpace(html.UnescapeString(m[1])))
			}
		}
		if book.ValidISBN13(ed.Barcode) {
			ed.ISBN13 = ed.Barcode
		}
		if ed.PID == "" || ed.Title == "" {
			continue
		}
		eds = append(eds, ed)
	}
	return eds
}

func first(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

// isAuthorRole은 번역, 그림, 감수 같은 역할을 빼고 글쓴이 역할만 고른다.
func isAuthorRole(role string) bool {
	for _, r := range []string{"저", "글", "지은", "엮", "편"} {
		if strings.Contains(role, r) {
			return !strings.Contains(role, "번역")
		}
	}
	return false
}

const (
	productHost = "product.kyobobook.co.kr"
	ebookHost   = "ebook-product.kyobobook.co.kr"
)

var (
	productPath = regexp.MustCompile(`^/detail/([A-Z]\d+)/?$`)
	ebookPath   = regexp.MustCompile(`^/dig/epd/ebook/(E\d+)/?$`)
)

// ProductPID는 교보문고 상품 페이지 주소(https://product.kyobobook.co.kr/detail/S000001981838)나
// 전자책 상품 페이지 주소(https://ebook-product.kyobobook.co.kr/dig/epd/ebook/E000003412895)의 상품 번호다.
// 상품 페이지 주소가 아니면 ok가 거짓이다.
func ProductPID(raw string) (pid string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	var m []string
	switch strings.ToLower(u.Hostname()) {
	case productHost:
		m = productPath.FindStringSubmatch(u.Path)
	case ebookHost:
		m = ebookPath.FindStringSubmatch(u.Path)
	}
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ProductURL은 교보문고 상품 페이지 주소다. 전자책(상품 번호가 E로 시작)은 전자책 상품 페이지다.
func ProductURL(pid string) string {
	if strings.HasPrefix(pid, "E") {
		return "https://" + ebookHost + "/dig/epd/ebook/" + url.PathEscape(pid)
	}
	return "https://" + productHost + "/detail/" + url.PathEscape(pid)
}

// SearchURL은 상품을 확정하지 못한 책을 교보문고에서 찾아보는 검색 주소다.
func SearchURL(query string) string {
	return "https://search.kyobobook.co.kr/search?" + url.Values{"keyword": {query}}.Encode()
}

// CoverURL은 교보문고가 바코드(일반 도서는 ISBN)로 제공하는 표지 이미지 주소다. width는 픽셀 단위 너비다.
// 상품 번호로는 표지를 주지 않고 '이미지 없음' 그림을 준다.
func CoverURL(barcode string, width int) string {
	return fmt.Sprintf("https://contents.kyobobook.co.kr/sih/fit-in/%dx0/pdt/%s.jpg", width, url.PathEscape(barcode))
}
