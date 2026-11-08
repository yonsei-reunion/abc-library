package weekly

import (
	"context"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yonsei-alumni/abc-library/internal/extract"
	"github.com/yonsei-alumni/abc-library/internal/kakao"
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// trailing은 문장 부호처럼 주소 끝에 붙어 들어온 글자다.
const trailing = `.,;:!?)]}」』》〉>'"…~`

// Link는 대화에서 공유된 링크 하나다.
type Link struct {
	URL string
	// Title은 링크 페이지의 제목이다. 읽지 못했으면 비어 있다.
	Title string
}

// ExtractURLs는 메시지 본문에서 http(s) 주소를 처음 나온 순서대로 중복 없이 뽑는다.
// 링크는 코드가 모두 뽑아서 요약에서 빠지지 않게 한다.
func ExtractURLs(msgs []kakao.Message) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range msgs {
		for _, raw := range urlPattern.FindAllString(m.Text, -1) {
			u := trimURL(raw)
			if p, err := url.Parse(u); err != nil || p.Host == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// trimURL은 주소 끝에 붙은 문장 부호를 뗀다. 주소 안에 여는 괄호가 있으면 닫는 괄호는 남긴다.
func trimURL(u string) string {
	for u != "" {
		r, size := utf8.DecodeLastRuneInString(u)
		if !strings.ContainsRune(trailing, r) || (r == ')' && strings.Count(u, "(") >= strings.Count(u, ")")) {
			break
		}
		u = u[:len(u)-size]
	}
	return u
}

// TitleFetcher는 링크 페이지의 제목을 읽는다. 읽지 못하면 빈 문자열을 돌려준다.
type TitleFetcher interface {
	Title(ctx context.Context, rawURL string) string
}

// PageTitles는 링크 페이지의 og:title이나 <title>을 읽는다. 링크 미리보기처럼 페이지마다 한 번만 받는다.
type PageTitles struct {
	HTTP *http.Client
	// MaxBytes는 페이지에서 읽는 바이트 수의 상한이다. 제목은 문서 앞쪽에 있다.
	MaxBytes int64
}

// NewPageTitles는 기본 설정의 PageTitles를 만든다.
func NewPageTitles() *PageTitles {
	return &PageTitles{HTTP: &http.Client{Timeout: 8 * time.Second}, MaxBytes: 512 << 10}
}

const userAgent = "abc-library/0.1 (+https://github.com/yonsei-reunion/abc-library)"

var (
	ogTitle    = regexp.MustCompile(`(?is)<meta\s[^>]*(?:property|name)\s*=\s*["'](?:og:title|twitter:title)["'][^>]*>`)
	contentArg = regexp.MustCompile(`(?is)\scontent\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	titleTag   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	spaces     = regexp.MustCompile(`\s+`)
)

// Title은 HTML 페이지의 제목을 읽는다. HTML이 아니거나 실패하면 빈 문자열이다.
func (p *PageTitles) Title(ctx context.Context, rawURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/html" && mt != "application/xhtml+xml" {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.MaxBytes))
	if err != nil {
		return ""
	}
	return ParseTitle(string(body))
}

// ParseTitle은 HTML에서 og:title(없으면 twitter:title, <title>)을 읽어 공백을 정리한다. 200자를 넘으면 자른다.
func ParseTitle(page string) string {
	var t string
	if tag := ogTitle.FindString(page); tag != "" {
		if m := contentArg.FindStringSubmatch(tag); m != nil {
			t = m[1] + m[2]
		}
	}
	if strings.TrimSpace(t) == "" {
		if m := titleTag.FindStringSubmatch(page); m != nil {
			t = m[1]
		}
	}
	t = strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(t), " "))
	if r := []rune(t); len(r) > 200 {
		t = string(r[:200]) + "…"
	}
	return t
}

const (
	maxTitleFetches = 30
	titleWorkers    = 4
)

// fetchTitles는 링크 제목을 몇 개씩 동시에 읽는다. 서점 링크는 읽지 않는다(교보문고 상품 페이지는 봇 요청에 빈 응답을 준다).
func fetchTitles(ctx context.Context, f TitleFetcher, urls []string) []Link {
	links := make([]Link, len(urls))
	var targets []int
	for i, u := range urls {
		links[i].URL = u
		if p, err := url.Parse(u); err == nil && !extract.IsStoreLink(p) && len(targets) < maxTitleFetches {
			targets = append(targets, i)
		}
	}
	if f == nil {
		return links
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range titleWorkers {
		wg.Go(func() {
			for i := range jobs {
				links[i].Title = f.Title(ctx, links[i].URL)
			}
		})
	}
	for _, i := range targets {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return links
}
