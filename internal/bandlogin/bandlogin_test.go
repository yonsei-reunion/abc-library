package bandlogin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/telegram"
)

type fakeScreen struct {
	mu       sync.Mutex
	loggedIn bool
	stateErr error
	inputs   []string
	closed   bool
	notify   chan struct{}
}

func newFakeScreen() *fakeScreen { return &fakeScreen{notify: make(chan struct{})} }

func (f *fakeScreen) Frame() ([]byte, int, <-chan struct{}) {
	return []byte("jpeg"), 1, f.notify
}
func (f *fakeScreen) record(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, s)
	return nil
}
func (f *fakeScreen) Tap(x, y float64) error {
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return errors.New("화면 밖")
	}
	return f.record(fmt.Sprintf("tap %.1f %.1f", x, y))
}
func (f *fakeScreen) Type(t string) error { return f.record("text " + t) }
func (f *fakeScreen) Press(k string) error {
	if k != "Enter" && k != "Backspace" && k != "Tab" {
		return errors.New("모르는 키")
	}
	return f.record("key " + k)
}
func (f *fakeScreen) LoggedIn() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loggedIn
}
func (f *fakeScreen) State() ([]byte, error) {
	if f.stateErr != nil {
		return nil, f.stateErr
	}
	// exp 1700000000 = 2023-11-15 07:13 KST
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1700000000}`))
	return []byte(`{"cookies":[{"name":"band_session","value":"h.` + payload + `.s","expires":-1}]}`), nil
}
func (f *fakeScreen) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}
func (f *fakeScreen) login() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loggedIn = true
}

type fakeSaver struct {
	mu    sync.Mutex
	saved [][]byte
	err   error
}

func (s *fakeSaver) Save(_ context.Context, state []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.saved = append(s.saved, state)
	return "band-state 버전 9", nil
}

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []string
	sent chan string
}

func (n *fakeNotifier) Send(_ context.Context, chatID int64, text string) error {
	n.mu.Lock()
	n.msgs = append(n.msgs, fmt.Sprintf("%d:%s", chatID, text))
	n.mu.Unlock()
	if n.sent != nil {
		n.sent <- text
	}
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const (
	secret = "hook-secret"
	chatID = 42
)

type fixture struct {
	srv    *Server
	screen *fakeScreen
	saver  *fakeSaver
	note   *fakeNotifier
	clock  *clock
	logs   *bytes.Buffer
	h      http.Handler
	opens  int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		screen: newFakeScreen(), saver: &fakeSaver{}, note: &fakeNotifier{sent: make(chan string, 10)},
		clock: &clock{t: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{},
	}
	f.srv = &Server{
		OpenScreen: func() (Screen, error) {
			f.opens++
			return f.screen, nil
		},
		Saver: f.saver, Notifier: f.note, WebhookSecret: secret, ChatID: chatID,
		TTL: 10 * time.Minute, Poll: time.Millisecond, Now: f.clock.now,
		Log: slog.New(slog.NewTextHandler(f.logs, nil)),
	}
	f.h = f.srv.Handler()
	return f
}

func (f *fixture) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "login.example"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}

func (f *fixture) webhook(chat int64, text, sec string) int {
	body := fmt.Sprintf(`{"message":{"chat":{"id":%d},"text":%q}}`, chat, text)
	return f.do("POST", "/telegram", body, map[string]string{telegram.SecretHeader: sec}).Code
}

// renew는 /renew를 보내고 답으로 온 링크의 토큰을 돌려준다.
func (f *fixture) renew(t *testing.T) string {
	t.Helper()
	if code := f.webhook(chatID, "/renew", secret); code != 200 {
		t.Fatalf("웹훅 상태 %d", code)
	}
	msg := <-f.note.sent
	_, token, ok := strings.Cut(msg, "https://login.example/login#")
	if !ok {
		t.Fatalf("링크가 없는 답: %q", msg)
	}
	return token
}

func (f *fixture) claim(token, cookie string) *httptest.ResponseRecorder {
	hdr := map[string]string{"Content-Type": "application/json"}
	if cookie != "" {
		hdr["Cookie"] = deviceCookie + "=" + cookie
	}
	return f.do("POST", "/login/claim", `{"token":"`+token+`"}`, hdr)
}

func deviceOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == deviceCookie {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("쿠키 속성이 약하다: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("기기 쿠키가 없다")
	return ""
}

func TestWebhookRejects(t *testing.T) {
	f := newFixture(t)
	if code := f.webhook(chatID, "/renew", "wrong"); code != 404 {
		t.Errorf("틀린 비밀: %d", code)
	}
	if code := f.webhook(chatID, "/renew", ""); code != 404 {
		t.Errorf("비밀 없음: %d", code)
	}
	if code := f.webhook(7, "/renew", secret); code != 200 {
		t.Errorf("다른 대화방도 200으로 답해야 한다: %d", code)
	}
	if code := f.webhook(chatID, "renew", secret); code != 200 {
		t.Errorf("명령이 아닌 글: %d", code)
	}
	if f.opens != 0 || len(f.note.msgs) != 0 {
		t.Errorf("화면을 열거나 답하면 안 된다: opens=%d msgs=%v", f.opens, f.note.msgs)
	}
	if code := f.do("POST", "/telegram", "{", map[string]string{telegram.SecretHeader: secret}).Code; code != 200 {
		t.Errorf("깨진 JSON: %d", code)
	}
}

func TestWebhookEmptySecretRejectsAll(t *testing.T) {
	f := newFixture(t)
	f.srv.WebhookSecret = ""
	if code := f.webhook(chatID, "/renew", ""); code != 404 {
		t.Errorf("비밀을 정하지 않았으면 모두 막아야 한다: %d", code)
	}
}

func TestRenewLoginSaves(t *testing.T) {
	f := newFixture(t)
	token := f.renew(t)
	if f.renew(t) != token || f.opens != 1 {
		t.Fatal("쓰기 전에 다시 보낸 /renew는 같은 링크를 줘야 한다")
	}
	if strings.Contains(f.logs.String(), token) {
		t.Fatal("토큰이 로그에 남았다")
	}
	w := f.claim(token, "")
	if w.Code != 204 {
		t.Fatalf("claim %d", w.Code)
	}
	dev := deviceOf(t, w)

	if code := f.webhook(chatID, "/renew", secret); code != 200 {
		t.Fatal(code)
	}
	if msg := <-f.note.sent; !strings.Contains(msg, "다른 기기") {
		t.Errorf("쓰는 중에 /renew: %q", msg)
	}

	in := func(body string) int {
		return f.do("POST", "/login/input", body, map[string]string{"Cookie": deviceCookie + "=" + dev}).Code
	}
	for _, b := range []string{`{"type":"tap","x":0.5,"y":0.2}`, `{"type":"text","text":"01012345678"}`, `{"type":"key","key":"Enter"}`} {
		if code := in(b); code != 204 {
			t.Errorf("%s: %d", b, code)
		}
	}
	for _, b := range []string{`{"type":"tap","x":1.5,"y":0.2}`, `{"type":"key","key":"Delete"}`, `{"type":"x"}`, `{`,
		`{"type":"text","text":"` + strings.Repeat("1", 257) + `"}`} {
		if code := in(b); code != 400 {
			t.Errorf("%s: %d", b, code)
		}
	}
	if strings.Contains(f.logs.String(), "01012345678") {
		t.Fatal("입력 내용이 로그에 남았다")
	}

	f.screen.login()
	msg := <-f.note.sent
	if !strings.Contains(msg, "band-state 버전 9") || !strings.Contains(msg, "2023-11-15 07:13") {
		t.Errorf("완료 알림: %q", msg)
	}
	if len(f.saver.saved) != 1 || !f.screen.closed {
		t.Errorf("저장 %d번, 닫힘 %v", len(f.saver.saved), f.screen.closed)
	}
	if code := in(`{"type":"key","key":"Enter"}`); code != 404 {
		t.Errorf("끝난 뒤 입력: %d", code)
	}
	if strings.Contains(f.logs.String(), dev) {
		t.Fatal("기기 쿠키가 로그에 남았다")
	}
}

func TestClaimRules(t *testing.T) {
	f := newFixture(t)
	if w := f.claim("anything", ""); w.Code != 404 {
		t.Errorf("열린 화면이 없을 때: %d", w.Code)
	}
	token := f.renew(t)
	if w := f.claim("wrong", ""); w.Code != 404 {
		t.Errorf("틀린 토큰: %d", w.Code)
	}
	if w := f.claim("", ""); w.Code != 404 {
		t.Errorf("빈 토큰: %d", w.Code)
	}
	w := f.claim(token, "")
	dev := deviceOf(t, w)
	if w := f.claim(token, ""); w.Code != 404 {
		t.Errorf("다른 기기가 같은 토큰을 쓰면 안 된다: %d", w.Code)
	}
	if w := f.claim(token, "other"); w.Code != 404 {
		t.Errorf("다른 쿠키: %d", w.Code)
	}
	if w := f.claim("", dev); w.Code != 204 {
		t.Errorf("같은 기기의 새로 고침은 쿠키만으로 된다: %d", w.Code)
	}
	if code := f.do("POST", "/login/input", `{"type":"key","key":"Enter"}`, nil).Code; code != 404 {
		t.Errorf("쿠키 없는 입력: %d", code)
	}
	if code := f.do("GET", "/login/frames", "", map[string]string{"Cookie": deviceCookie + "=other"}).Code; code != 404 {
		t.Errorf("다른 쿠키의 화면: %d", code)
	}
}

func TestExpiredTokenAndTimeout(t *testing.T) {
	f := newFixture(t)
	f.srv.Poll = time.Hour // 시간이 지나는 것을 직접 확인하려고 watch를 멈춰 둔다.
	token := f.renew(t)
	f.clock.add(10*time.Minute + time.Second)
	if w := f.claim(token, ""); w.Code != 404 {
		t.Errorf("만료된 토큰: %d", w.Code)
	}

	g := newFixture(t)
	g.renew(t)
	g.clock.add(10*time.Minute + time.Second)
	msg := <-g.note.sent
	if !strings.Contains(msg, "시간이 지나서") {
		t.Errorf("시간 초과 알림: %q", msg)
	}
	if len(g.saver.saved) != 0 || !g.screen.closed {
		t.Errorf("저장하지 않고 닫아야 한다: 저장 %d, 닫힘 %v", len(g.saver.saved), g.screen.closed)
	}
	// 끝난 뒤에는 새 화면을 연다.
	g.screen = newFakeScreen()
	g.renew(t)
	if g.opens != 2 {
		t.Errorf("새 화면을 열어야 한다: %d", g.opens)
	}
}

func TestSaveFailures(t *testing.T) {
	for name, set := range map[string]func(*fixture){
		"state": func(f *fixture) { f.screen.stateErr = errors.New("꺼내기 실패") },
		"save":  func(f *fixture) { f.saver.err = errors.New("Secret Manager 실패") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			set(f)
			f.renew(t)
			f.screen.login()
			msg := <-f.note.sent
			if !strings.Contains(msg, "세션은 바꾸지 않았습니다") {
				t.Errorf("실패 알림: %q", msg)
			}
			if len(f.saver.saved) != 0 {
				t.Error("저장하면 안 된다")
			}
		})
	}
}

func TestOpenScreenFailure(t *testing.T) {
	f := newFixture(t)
	f.srv.OpenScreen = func() (Screen, error) { return nil, errors.New("브라우저 없음") }
	if code := f.webhook(chatID, "/renew", secret); code != 200 {
		t.Fatal(code)
	}
	if msg := <-f.note.sent; !strings.Contains(msg, "로그인 화면을 열지 못했습니다") {
		t.Errorf("%q", msg)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t)
	f.srv.Poll = time.Hour
	token := f.renew(t)
	for range maxMisses + 1 {
		f.claim("wrong", "")
	}
	if w := f.claim(token, ""); w.Code != 404 {
		t.Errorf("틀린 시도가 많으면 맞는 토큰도 막아야 한다: %d", w.Code)
	}
	f.clock.add(time.Minute + time.Second)
	if w := f.claim(token, ""); w.Code != 204 {
		t.Errorf("1분 뒤에는 받아야 한다: %d", w.Code)
	}
}

func TestFramesStream(t *testing.T) {
	f := newFixture(t)
	token := f.renew(t)
	dev := deviceOf(t, f.claim(token, ""))
	ts := httptest.NewServer(f.h)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/login/frames", nil)
	req.Header.Set("Cookie", deviceCookie+"="+dev)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 64)
	n, _ := io.ReadAtLeast(resp.Body, buf, len("data: "+base64.StdEncoding.EncodeToString([]byte("jpeg"))))
	if want := "data: " + base64.StdEncoding.EncodeToString([]byte("jpeg")); !strings.HasPrefix(string(buf[:n]), want) {
		t.Errorf("첫 프레임: %q", buf[:n])
	}
	f.screen.login()
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "event: done\ndata: 밴드 세션을 갱신했습니다") {
		t.Errorf("끝 이벤트: %q", rest)
	}
}

func TestPageHeaders(t *testing.T) {
	f := newFixture(t)
	w := f.do("GET", "/login", "", nil)
	if w.Code != 200 || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("화면 응답: %d %v", w.Code, w.Header())
	}
	if f.do("GET", "/health", "", nil).Code != 200 {
		t.Error("health")
	}
}
