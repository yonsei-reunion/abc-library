// Package band는 Playwright로 밴드 웹 화면을 열어 게시글을 읽는다.
// 로그인은 사람이 직접 하고, 코드는 저장한 storageState만 쓴다.
// 밴드 내부 API를 직접 호출하지 않고, 웹 클라이언트가 그린 화면에서 내용을 읽는다.
// 쓰기 동작은 주간 배치가 대화방 주간 요약을 올리는 Write 하나뿐이다(write.go). 수집은 읽기만 한다.
package band

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/yonsei-alumni/abc-library/internal/book"
)

const (
	maxImages    = 4
	maxImageSize = 4 << 20

	// homeURL은 로그인한 뒤 처음 여는 밴드 홈이다.
	homeURL = "https://band.us/home"
	// defaultDelay는 Crawler.Delay의 기본값이다.
	defaultDelay = 2 * time.Second
	// maxCommentPages는 접힌 이전 댓글을 펼치는 최대 횟수다.
	maxCommentPages = 10

	// latestCommentSort는 게시글 목록의 정렬 메뉴에서 고르는 항목이다.
	latestCommentSort = "최신 댓글순"
	// resortSettle은 정렬을 바꾼 뒤 목록이 다시 그려지기를 기다리는 시간이다.
	resortSettle = 3 * time.Second
	// scrollPause는 목록을 내린 뒤 다음 글이 그려지기를 기다리는 시간이다.
	scrollPause = 1500 * time.Millisecond
	// maxIdleScrolls는 새 카드 없이 목록을 내린 횟수가 이만큼이면 목록 끝으로 보는 기준이다.
	maxIdleScrolls = 5
)

var (
	// ErrSessionExpired는 저장한 로그인 세션이 만료되었다는 뜻이다.
	ErrSessionExpired = errors.New("밴드 로그인 세션이 만료되었습니다. make band-login으로 다시 로그인하세요")
	// ErrNotFound는 삭제되었거나 없는 게시글이라는 뜻이다.
	ErrNotFound = errors.New("게시글이 없습니다")
)

// Post는 밴드 게시글 하나의 내용이다. 글과 댓글의 작성자 정보는 읽지 않는다.
type Post struct {
	No       int
	PostedAt time.Time
	Text     string
	// Links는 본문에 있는 외부 링크다. 댓글의 링크는 Comment에 따로 둔다.
	Links    []book.Link
	Images   []Image
	Comments []Comment
	// CommentCount는 화면에 나온 댓글 수다. 글자가 없어서 Comments에서 뺀 댓글과 답글도 센다.
	CommentCount int
}

// Comment는 게시글의 댓글 하나다.
type Comment struct {
	At    time.Time
	Text  string
	Links []book.Link
}

// Image는 게시글에 첨부된 사진이다.
type Image struct {
	MediaType string
	Data      []byte
}

// PostURL은 게시글 주소다. 비공개 밴드라서 멤버만 열어 볼 수 있다.
func PostURL(bandNo string, postNo int) string {
	return fmt.Sprintf("https://band.us/band/%s/post/%d", bandNo, postNo)
}

// PostListURL은 게시글 목록 주소다.
func PostListURL(bandNo string) string {
	return fmt.Sprintf("https://band.us/band/%s/post", bandNo)
}

// OnPostPage는 브라우저가 아직 그 게시글 화면에 있는지 알려 준다. 밴드는 band.us와 www.band.us를 오간다.
func OnPostPage(pageURL, bandNo string, postNo int) bool {
	u, err := url.Parse(pageURL)
	if err != nil || (u.Host != "band.us" && u.Host != "www.band.us") {
		return false
	}
	return strings.TrimSuffix(u.Path, "/") == fmt.Sprintf("/band/%s/post/%d", bandNo, postNo)
}

// InstallBrowser는 Playwright 드라이버와 Chromium이 없으면 내려받는다. 창을 띄우는 Login에 쓴다.
func InstallBrowser() error {
	return playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}, Verbose: false})
}

// InstallHeadlessBrowser는 Playwright 드라이버와 Chromium headless shell이 없으면 내려받는다.
// 창 없이 도는 Open에는 headless shell만 있으면 되므로, 배치 이미지에는 전체 Chromium을 넣지 않는다.
func InstallHeadlessBrowser() error {
	return playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}, OnlyInstallShell: true, Verbose: false})
}

// Login은 브라우저 창을 띄우고, 사람이 로그인을 마치면 세션을 statePath에 저장한다.
func Login(statePath string, timeout time.Duration) error {
	b, err := launch(launchOptions{headless: false})
	if err != nil {
		return err
	}
	defer func() { _ = b.close() }()
	if _, err := b.page.Goto(homeURL); err != nil {
		return err
	}
	fmt.Println("브라우저 창에서 밴드에 로그인하세요. 로그인을 마치면 세션을 저장하고 창을 닫습니다.")

	deadline := time.Now().Add(timeout)
	for !loggedIn(b.bctx, b.page) {
		if time.Now().After(deadline) {
			return errors.New("로그인을 기다리다가 시간이 지났습니다")
		}
		time.Sleep(2 * time.Second)
	}
	return b.saveState(statePath)
}

func loggedIn(bctx playwright.BrowserContext, page playwright.Page) bool {
	if strings.Contains(page.URL(), "auth.band.us") {
		return false
	}
	cookies, err := bctx.Cookies("https://band.us")
	if err != nil {
		return false
	}
	for _, c := range cookies {
		if c.Name == SessionCookie && c.Value != "" {
			return true
		}
	}
	return false
}

// Crawler는 저장한 세션으로 밴드 화면을 여는 헤드리스 브라우저다.
type Crawler struct {
	BandNo string
	// Board는 글을 올릴 게시판 이름이다. 게시판을 쓰는 밴드에서 글을 올릴 때만 쓴다.
	Board string
	// Delay는 페이지를 열기 전에 쉬는 시간이다. 밴드에 부담을 주지 않기 위해 둔다.
	Delay time.Duration

	*browserSession

	// dialogs는 페이지가 띄운 알림창 수다. 삭제된 글은 알림창을 띄우고 목록으로 돌아간다.
	dialogs atomic.Int32
	// dialogTexts는 알림창 문구다. 글쓰기가 막힌 이유를 오류에 남기려고 모은다.
	dialogMu    sync.Mutex
	dialogTexts []string
}

// Open은 statePath의 세션으로 브라우저를 연다.
func Open(statePath, bandNo string) (*Crawler, error) {
	if _, err := os.Stat(statePath); err != nil {
		return nil, fmt.Errorf("세션 파일 %s를 읽을 수 없습니다. make band-login으로 먼저 로그인하세요: %w", statePath, err)
	}
	b, err := launch(launchOptions{headless: true, statePath: statePath})
	if err != nil {
		return nil, err
	}
	c := &Crawler{BandNo: bandNo, Delay: defaultDelay, browserSession: b}
	c.watchDialogs()
	return c, nil
}

// watchDialogs는 페이지가 띄우는 알림창을 세고 문구를 모은 뒤 닫는다.
func (c *Crawler) watchDialogs() {
	c.page.OnDialog(func(d playwright.Dialog) {
		c.dialogs.Add(1)
		c.dialogMu.Lock()
		c.dialogTexts = append(c.dialogTexts, d.Type()+": "+shorten(d.Message(), 200))
		c.dialogMu.Unlock()
		_ = d.Dismiss()
	})
}

// takeDialogs는 모은 알림창 문구를 돌려주고 비운다.
func (c *Crawler) takeDialogs() []string {
	c.dialogMu.Lock()
	defer c.dialogMu.Unlock()
	texts := c.dialogTexts
	c.dialogTexts = nil
	return texts
}

// shorten은 s를 글자 n개까지로 줄인다.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Close는 브라우저를 닫는다.
func (c *Crawler) Close() error {
	return c.close()
}

func (c *Crawler) open(url string) error {
	time.Sleep(c.Delay)
	if _, err := c.page.Goto(url, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded}); err != nil {
		return err
	}
	if IsLoginURL(c.page.URL()) {
		return ErrSessionExpired
	}
	return nil
}

// IsLoginURL은 밴드 로그인 화면의 주소인지 알려 준다.
func IsLoginURL(raw string) bool {
	return strings.Contains(raw, "auth.band.us") || strings.Contains(raw, "/login")
}

// waitFor는 요소가 화면에 붙을 때까지 기다린다. 밴드는 세션이 끝났으면 페이지를 띄운 뒤 스크립트로 로그인 화면으로 옮겨 가므로,
// 기다리다 실패하면 로그인 화면인지 확인해서 ErrSessionExpired를 돌려준다.
func (c *Crawler) waitFor(loc playwright.Locator, timeout time.Duration) error {
	err := loc.WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateAttached, Timeout: ms(timeout)})
	if err != nil && IsLoginURL(c.page.URL()) {
		return ErrSessionExpired
	}
	return err
}

// openPostList는 게시글 목록을 열고 ready가 그려지기를 기다린다. what은 기다리다 실패했을 때 오류에 넣을 설명이다.
func (c *Crawler) openPostList(ready playwright.Locator, what string) error {
	if err := c.open(PostListURL(c.BandNo)); err != nil {
		return err
	}
	if err := c.waitFor(ready, pageWaitTimeout); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// LatestPostNo는 게시글 목록에서 가장 최근 글의 번호를 읽는다.
func (c *Crawler) LatestPostNo() (int, error) {
	sel := fmt.Sprintf(`a[href*="/band/%s/post/"]`, c.BandNo)
	if err := c.openPostList(c.page.Locator(sel).First(), "게시글 목록을 읽지 못했습니다"); err != nil {
		return 0, err
	}
	v, err := c.page.Evaluate(`bandNo => Math.max(0, ...[...document.querySelectorAll('a[href*="/band/' + bandNo + '/post/"]')]
		.map(a => Number((a.getAttribute('href').match(/\/post\/(\d+)/) || [])[1] || 0)))`, c.BandNo)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int)
	if !ok {
		return 0, fmt.Errorf("게시글 번호를 읽지 못했습니다: %v", v)
	}
	return n, nil
}

// Activity는 게시글 목록의 카드 하나에 나온 글 번호와 댓글 수다.
type Activity struct {
	No       int `json:"no"`
	Comments int `json:"comments"`
}

// cardsScript는 게시글 목록에 그려진 카드마다 글 번호와 댓글 수를 읽는다. 댓글이 없는 글에는 댓글 수가 나오지 않는다.
// 작성자 이름 링크가 게시글 주소를 가리키므로 주소만 읽고 이름은 읽지 않는다.
const cardsScript = `bandNo => JSON.stringify([...document.querySelectorAll('article._postMainWrap')].map(card => {
  const a = card.querySelector('.postWriterInfoWrap a[href*="/band/' + bandNo + '/post/"]');
  const count = card.querySelector('.dPostCountView ._commentCountBtn .count');
  return {
    no: a ? Number((a.getAttribute('href').match(/\/post\/(\d+)/) || [])[1] || 0) : 0,
    comments: count ? Number(count.textContent.replace(/\D/g, '')) : 0,
  };
}).filter(c => c.no > 0))`

// RecentlyCommented는 게시글 목록을 '최신 댓글순'으로 바꾸고, 최근에 댓글이 달린 글부터 카드의 글 번호와 댓글 수를 읽는다.
// 목록은 끝까지 내려야 다음 글을 불러오고 지나간 카드는 화면에서 지우므로, 조금씩 내리면서 그려진 카드를 모은다.
// done이 참인 카드를 만나면 그때 그려진 카드까지만 모으고, 목록 끝에 닿거나 limit개를 모아도 멈춘다.
// 정렬은 이 화면에서만 바뀌고 계정 설정에 저장되지 않는다.
func (c *Crawler) RecentlyCommented(done func(Activity) bool, limit int) ([]Activity, error) {
	if err := c.sortByLatestComment(); err != nil {
		return nil, err
	}
	var cards []Activity
	seen := map[int]bool{}
	// idle은 새 카드 없이 목록을 내린 횟수다.
	for idle := 0; len(cards) < limit && idle < maxIdleScrolls; {
		drawn, err := evalJSON[[]Activity](c.page, cardsScript, c.BandNo)
		if err != nil {
			return nil, fmt.Errorf("게시글 목록을 읽지 못했습니다: %w", err)
		}
		found, added := false, 0
		for _, a := range drawn {
			if !seen[a.No] {
				seen[a.No] = true
				cards = append(cards, a)
				found = found || done(a)
				added++
			}
		}
		if found {
			break
		}
		if added == 0 {
			idle++
		} else {
			idle = 0
		}
		// 마지막 카드가 보이게 내리면 다음 글을 불러온다.
		if err := c.page.Locator("article._postMainWrap").Last().ScrollIntoViewIfNeeded(); err != nil {
			return nil, err
		}
		time.Sleep(scrollPause)
	}
	return cards[:min(len(cards), limit)], nil
}

// sortByLatestComment는 게시글 목록을 열고 정렬을 '최신 댓글순'으로 바꾼 뒤, 목록이 다시 그려지기를 기다린다.
func (c *Crawler) sortByLatestComment() error {
	sortButton := c.page.Locator("._btnSort").First()
	if err := c.openPostList(sortButton, "게시글 목록의 정렬 버튼을 찾지 못했습니다"); err != nil {
		return err
	}
	click := playwright.LocatorClickOptions{Timeout: ms(clickTimeout)}
	if err := sortButton.Click(click); err != nil {
		return err
	}
	if err := c.page.Locator("button._optionMenuLink", playwright.PageLocatorOptions{HasText: latestCommentSort}).First().Click(click); err != nil {
		return fmt.Errorf("'최신 댓글순' 정렬을 고르지 못했습니다: %w", err)
	}
	if err := c.waitFor(c.page.Locator("._btnSort", playwright.PageLocatorOptions{HasText: latestCommentSort}).First(), pageWaitTimeout); err != nil {
		return fmt.Errorf("정렬이 '최신 댓글순'으로 바뀌지 않았습니다: %w", err)
	}
	// 정렬 버튼은 바로 바뀌지만 목록은 새로 불러온 뒤에 다시 그린다.
	time.Sleep(resortSettle)
	return nil
}

// extractScript는 게시글 상세 화면에서 본문, 외부 링크, 첨부 사진, 게시 시각과 댓글을 읽는다.
// 글과 댓글의 작성자 영역은 읽지 않는다.
const extractScript = `() => {
  const main = document.querySelector('.postWrap .postMain');
  if (!main) return null;
  // 본문의 주소 텍스트와 링크 미리보기가 같은 주소를 가리키므로, 주소마다 하나만 남기고 미리보기 제목을 쓴다.
  const linksIn = (root) => {
    const links = new Map();
    for (const a of root ? root.querySelectorAll('a[href^="http"]') : []) {
      const href = a.getAttribute('href');
      if (/^https?:\/\/([^\/]*\.)?band\.us(\/|$)/.test(href)) continue;
      const snippet = a.querySelector('.snippetTit');
      let title = (snippet ? snippet.textContent : a.textContent).trim();
      if (title === href) title = '';
      const prev = links.get(href);
      if (!prev || (!prev.title && title)) links.set(href, {url: href, title});
    }
    return [...links.values()];
  };
  const body = main.querySelector('.postBody');
  // 본문은 사진이나 첨부를 사이에 두고 여러 덩어리로 나뉠 수 있으므로 모두 읽는다.
  const texts = body ? [...body.querySelectorAll('.dPostTextView .txtBody')].map(t => t.innerText.trim()).filter(t => t) : [];
  const images = body ? [...body.querySelectorAll('img')]
    .filter(img => !img.closest('.snippet'))
    .map(img => img.currentSrc || img.getAttribute('src'))
    .filter(src => src && /^https:\/\/[^\/]*pstatic\.net\//.test(src)) : [];
  // 최근 글과 댓글은 화면에 '17시간 전'처럼 나오므로, 절대 시각이 담긴 title 속성을 먼저 읽는다.
  const timeOf = (el) => el ? (el.getAttribute('title') || el.textContent).trim() : '';
  const time = main.querySelector('.postWriterInfoWrap time.time');
  const comments = [...document.querySelectorAll('.postWrap .dPostCommentMainView .cComment')].map(c => {
    const cbody = c.querySelector('.commentBody');
    const ctext = cbody && cbody.querySelector('p.txt');
    const ctime = c.querySelector('.func time.time');
    return {text: ctext ? ctext.innerText.trim() : '', time: timeOf(ctime), links: linksIn(cbody)};
  }).filter(c => c.text || c.links.length);
  // 댓글이 없는 글에는 댓글 수가 나오지 않는다.
  const count = document.querySelector('.postWrap .dPostCountView ._commentCountBtn .count');
  return JSON.stringify({
    text: texts.join('\n\n'), links: linksIn(body), images: [...new Set(images)],
    time: timeOf(time), comments, commentCount: count ? Number(count.textContent.replace(/\D/g, '')) : 0,
  });
}`

type scraped struct {
	Text     string      `json:"text"`
	Links    []book.Link `json:"links"`
	Images   []string    `json:"images"`
	Time     string      `json:"time"`
	Comments []struct {
		Text  string      `json:"text"`
		Time  string      `json:"time"`
		Links []book.Link `json:"links"`
	} `json:"comments"`
	CommentCount int `json:"commentCount"`
}

var moreComments = regexp.MustCompile(`이전 댓글|댓글 더보기`)

// loadAllComments는 접혀 있는 이전 댓글을 펼친다. 댓글 목록을 더 불러오기만 하고 쓰기 동작은 하지 않는다.
func (c *Crawler) loadAllComments() {
	for range maxCommentPages {
		btn := c.page.Locator(".dPostCommentMainView").Locator("button, a", playwright.LocatorLocatorOptions{HasText: moreComments}).First()
		if n, err := btn.Count(); err != nil || n == 0 {
			return
		}
		if err := btn.Click(playwright.LocatorClickOptions{Timeout: ms(clickTimeout)}); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
}

// Post는 게시글 하나를 읽는다. 삭제된 글이면 ErrNotFound를 돌려준다.
func (c *Crawler) Post(no int) (Post, error) {
	c.dialogs.Store(0)
	if err := c.open(PostURL(c.BandNo, no)); err != nil {
		return Post{}, err
	}
	// 로그인 화면으로 옮겨 간 글을 삭제된 글로 여기지 않는다.
	if err := c.waitFor(c.page.Locator(".postWrap .postMain .postBody"), postWaitTimeout); err != nil {
		if errors.Is(err, ErrSessionExpired) {
			return Post{}, err
		}
		return Post{}, ErrNotFound
	}
	c.loadAllComments()
	v, err := c.page.Evaluate(extractScript)
	if err != nil {
		return Post{}, err
	}
	if c.leftPost(no) {
		return Post{}, ErrNotFound
	}
	raw, ok := v.(string)
	if !ok { // 화면에 글이 없으면 스크립트가 null을 돌려준다.
		return Post{}, ErrNotFound
	}
	var s scraped
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Post{}, err
	}
	p, err := s.post(no, time.Now())
	if err != nil {
		return Post{}, err
	}
	p.Images = c.downloadImages(s.Images)
	return p, nil
}

// leftPost는 게시글 no의 화면을 벗어났는지 알려 준다.
// 삭제된 글은 '삭제되었거나 찾을 수 없습니다' 알림창을 띄우고 게시글 목록으로 옮겨 간다.
// 목록 화면에도 같은 구조의 글이 있으므로, 그 글을 이 번호의 글로 읽지 않게 확인한다.
func (c *Crawler) leftPost(no int) bool {
	return c.dialogs.Load() > 0 || !OnPostPage(c.page.URL(), c.BandNo, no)
}

// post는 화면에서 읽은 내용을 게시글 no로 바꾼다. 사진은 따로 내려받는다.
// 연도가 빠진 시각은 now를 기준으로 읽고, 댓글 시각을 읽지 못했거나 게시 시각보다 앞서면 게시 시각으로 본다.
func (s scraped) post(no int, now time.Time) (Post, error) {
	postedAt, err := ParseTime(s.Time, now)
	if err != nil {
		return Post{}, fmt.Errorf("게시글 %d: %w", no, err)
	}
	p := Post{No: no, PostedAt: postedAt, Text: s.Text, Links: s.Links, CommentCount: s.CommentCount}
	for _, sc := range s.Comments {
		at, err := ParseTime(sc.Time, now)
		if err != nil || at.Before(postedAt) {
			at = postedAt
		}
		p.Comments = append(p.Comments, Comment{At: at, Text: sc.Text, Links: sc.Links})
	}
	return p, nil
}

// downloadImages는 사진을 maxImages장까지 내려받는다. 내려받지 못한 사진은 건너뛴다.
func (c *Crawler) downloadImages(srcs []string) []Image {
	var images []Image
	for _, src := range srcs {
		if len(images) == maxImages {
			break
		}
		if img, err := c.download(src); err == nil {
			images = append(images, img)
		}
	}
	return images
}

func (c *Crawler) download(src string) (Image, error) {
	resp, err := c.bctx.Request().Get(src, playwright.APIRequestContextGetOptions{
		Headers: map[string]string{"Referer": "https://band.us/"},
	})
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = resp.Dispose() }()
	if resp.Status() != http.StatusOK {
		return Image{}, fmt.Errorf("사진 응답 코드 %d", resp.Status())
	}
	data, err := resp.Body()
	if err != nil {
		return Image{}, err
	}
	mediaType := http.DetectContentType(data)
	switch {
	case len(data) > maxImageSize:
		return Image{}, errors.New("사진이 너무 큽니다")
	case mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/gif" && mediaType != "image/webp":
		return Image{}, fmt.Errorf("지원하지 않는 사진 형식 %s", mediaType)
	}
	return Image{MediaType: mediaType, Data: data}, nil
}

var timeRe = regexp.MustCompile(`(?:(\d{4})년\s*)?(\d{1,2})월\s*(\d{1,2})일\s*(오전|오후)\s*(\d{1,2}):(\d{2})`)

// ParseTime은 "2026년 10월 23일 오후 8:09" 형식의 게시 시각을 읽는다.
// 올해 글처럼 연도가 빠져 있으면 now의 연도로 보고, 그 시각이 미래면 작년으로 본다.
func ParseTime(s string, now time.Time) (time.Time, error) {
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("게시 시각 %q를 읽지 못했습니다", s)
	}
	now = now.In(book.Seoul)
	year := now.Year()
	if m[1] != "" {
		year, _ = strconv.Atoi(m[1])
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[5])
	minute, _ := strconv.Atoi(m[6])
	if hour == 12 {
		hour = 0
	}
	if m[4] == "오후" {
		hour += 12
	}
	t := time.Date(year, time.Month(month), day, hour, minute, 0, 0, book.Seoul)
	if m[1] == "" && t.After(now) {
		t = t.AddDate(-1, 0, 0)
	}
	return t, nil
}
