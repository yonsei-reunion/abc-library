package band

import (
	"os"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Playwright의 대기 시간. 밴드 화면은 스크립트로 그리므로 요소가 붙기를 기다린다.
const (
	// pageWaitTimeout은 게시글 목록이나 글쓰기 버튼처럼 페이지를 연 뒤 첫 요소가 그려지기를 기다리는 시간이다.
	pageWaitTimeout = 20 * time.Second
	// postWaitTimeout은 게시글 본문을 기다리는 시간이다. 지나면 삭제된 글로 본다.
	postWaitTimeout = 15 * time.Second
	// clickTimeout은 화면에 이미 있는 메뉴나 버튼을 누를 때 기다리는 시간이다.
	clickTimeout = 5 * time.Second
	// writeClickTimeout은 글쓰기 창의 버튼과 편집기를 누를 때 기다리는 시간이다.
	writeClickTimeout = 10 * time.Second
	// editorWaitTimeout은 글쓰기 창을 연 뒤 편집기가 보이기를 기다리는 시간이다.
	editorWaitTimeout = 15 * time.Second
)

// ms는 Playwright 옵션이 받는 밀리초 값이다.
func ms(d time.Duration) *float64 {
	return playwright.Float(float64(d.Milliseconds()))
}

// browserSession은 Playwright로 띄운 Chromium과 그 안의 브라우저 컨텍스트, 페이지 하나다.
type browserSession struct {
	pw       *playwright.Playwright
	chromium playwright.Browser
	bctx     playwright.BrowserContext
	page     playwright.Page
}

// launchOptions는 브라우저를 띄우는 방식이다.
type launchOptions struct {
	// headless가 거짓이면 사람이 볼 수 있게 창을 띄운다.
	headless bool
	// statePath는 불러올 storageState 파일이다. 비어 있으면 로그인하지 않은 새 세션이다.
	statePath string
	// viewport는 화면 크기다. nil이면 Playwright 기본값이다.
	viewport *playwright.Size
	// chromeUserAgent가 참이면 headless shell이 밝히는 HeadlessChrome 대신 일반 Chrome의 사용자 에이전트를 쓴다.
	chromeUserAgent bool
}

// launch는 Chromium을 띄우고 페이지 하나를 연다. 중간에 실패하면 띄운 것을 모두 닫는다.
func launch(o launchOptions) (*browserSession, error) {
	b := &browserSession{}
	var err error
	if b.pw, err = playwright.Run(); err != nil {
		return nil, err
	}
	if b.chromium, err = b.pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(o.headless)}); err != nil {
		_ = b.close()
		return nil, err
	}
	opts := playwright.BrowserNewContextOptions{
		Locale:     playwright.String("ko-KR"),
		TimezoneId: playwright.String("Asia/Seoul"),
		Viewport:   o.viewport,
	}
	if o.statePath != "" {
		opts.StorageStatePath = playwright.String(o.statePath)
	}
	if o.chromeUserAgent {
		opts.UserAgent = playwright.String(chromeUserAgent(b.chromium.Version()))
	}
	if b.bctx, err = b.chromium.NewContext(opts); err != nil {
		_ = b.close()
		return nil, err
	}
	if b.page, err = b.bctx.NewPage(); err != nil {
		_ = b.close()
		return nil, err
	}
	return b, nil
}

// close는 브라우저를 닫고 Playwright 드라이버를 멈춘다. 띄우다 만 상태에서도 부를 수 있다.
func (b *browserSession) close() error {
	if b.chromium != nil {
		_ = b.chromium.Close()
	}
	if b.pw != nil {
		return b.pw.Stop()
	}
	return nil
}

// saveState는 브라우저가 가진 세션을 path에 저장한다. 다른 사용자가 읽지 못하게 임시 파일에 0600으로 쓴 뒤 바꿔 넣는다.
func (b *browserSession) saveState(path string) error {
	tmp := path + ".tmp"
	if _, err := b.bctx.StorageState(playwright.BrowserContextStorageStateOptions{Path: playwright.String(tmp)}); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// chromeUserAgent는 일반 Chrome의 사용자 에이전트다. headless shell은 HeadlessChrome이라고 밝히므로,
// 밴드가 자동화 브라우저로 보고 로그인을 막지 않게 바꾼다.
func chromeUserAgent(version string) string {
	return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + " Safari/537.36"
}
