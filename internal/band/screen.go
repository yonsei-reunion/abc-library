package band

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/mxschmitt/playwright-go"
)

// 원격 로그인 화면의 크기. 폰 화면에 맞춘다.
const (
	ScreenWidth  = 390
	ScreenHeight = 844
)

// LoginURL은 원격 로그인 화면이 처음 여는 주소다. 로그인 수단을 고르는 화면이다.
const LoginURL = "https://auth.band.us/login_page?next_url=https%3A%2F%2Fband.us%2Fhome"

// screenKeys는 원격 화면에서 누를 수 있는 특수 키다. 나머지 글자는 Type으로 넣는다.
var screenKeys = map[string]bool{"Backspace": true, "Enter": true, "Tab": true}

// ErrUnknownKey는 원격 화면이 받지 않는 키라는 뜻이다.
var ErrUnknownKey = errors.New("받지 않는 키입니다")

// LoginScreen은 창 없이 띄운 브라우저의 밴드 로그인 화면이다. 사람이 다른 기기에서 화면을 보며 직접 로그인한다.
// 코드는 화면을 비추고 받은 입력을 그대로 전달할 뿐이고, 입력 내용과 화면을 저장하거나 기록하지 않는다.
type LoginScreen struct {
	*browserSession
	cdp playwright.CDPSession

	mu     sync.Mutex
	frame  []byte
	seq    int
	notify chan struct{}
	closed bool
}

// OpenLoginScreen은 새 브라우저 컨텍스트에서 밴드 로그인 화면을 열고 화면 중계를 시작한다.
func OpenLoginScreen() (*LoginScreen, error) {
	b, err := launch(launchOptions{
		headless:        true,
		viewport:        &playwright.Size{Width: ScreenWidth, Height: ScreenHeight},
		chromeUserAgent: true,
	})
	if err != nil {
		return nil, err
	}
	s := &LoginScreen{browserSession: b, notify: make(chan struct{})}
	if err := s.start(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// start는 화면 중계를 시작하고 로그인 화면을 연다.
func (s *LoginScreen) start() error {
	var err error
	if s.cdp, err = s.bctx.NewCDPSession(s.page); err != nil {
		return err
	}
	s.cdp.On("Page.screencastFrame", func(ev map[string]any) {
		data, _ := ev["data"].(string)
		id := ev["sessionId"]
		// 이벤트를 받는 고루틴에서 Send를 기다리면 응답을 받지 못하므로 따로 보낸다.
		go func() { _, _ = s.cdp.Send("Page.screencastFrameAck", map[string]any{"sessionId": id}) }()
		jpeg, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return
		}
		s.setFrame(jpeg)
	})
	if _, err := s.cdp.Send("Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 60, "maxWidth": ScreenWidth, "maxHeight": ScreenHeight,
	}); err != nil {
		return err
	}
	_, err = s.page.Goto(LoginURL, playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded})
	return err
}

func (s *LoginScreen) setFrame(jpeg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.frame = jpeg
	s.seq++
	close(s.notify)
	s.notify = make(chan struct{})
}

// Frame은 마지막 화면(JPEG)과 그 번호, 다음 화면이 오면 닫히는 채널을 돌려준다.
func (s *LoginScreen) Frame() (jpeg []byte, seq int, next <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame, s.seq, s.notify
}

// Tap은 화면의 (x, y) 지점을 누른다. 좌표는 화면 너비와 높이에 대한 비율(0~1)이다.
func (s *LoginScreen) Tap(x, y float64) error {
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return errors.New("화면 밖을 눌렀습니다")
	}
	return s.page.Mouse().Click(x*ScreenWidth, y*ScreenHeight)
}

// Type은 포커스가 있는 입력창에 글자를 넣는다.
func (s *LoginScreen) Type(text string) error {
	return s.page.Keyboard().InsertText(text)
}

// Press는 특수 키(Backspace, Enter, Tab)를 누른다.
func (s *LoginScreen) Press(key string) error {
	if !screenKeys[key] {
		return ErrUnknownKey
	}
	return s.page.Keyboard().Press(key)
}

// LoggedIn은 로그인을 마쳤는지 알려 준다.
func (s *LoginScreen) LoggedIn() bool {
	return loggedIn(s.bctx, s.page)
}

// State는 로그인한 세션을 storageState JSON으로 돌려준다. 파일은 잠깐 쓰고 지운다.
func (s *LoginScreen) State() ([]byte, error) {
	dir, err := os.MkdirTemp("", "band-login")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "state.json")
	if _, err := s.bctx.StorageState(playwright.BrowserContextStorageStateOptions{Path: playwright.String(path)}); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Close는 브라우저를 닫는다. 여러 번 불러도 된다.
func (s *LoginScreen) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.notify)
	s.mu.Unlock()
	return s.close()
}
