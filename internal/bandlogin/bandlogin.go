// Package bandlogin은 폰에서 밴드에 다시 로그인하게 하는 서버다.
//
// 텔레그램에서 /renew를 보내면 서버가 창 없는 브라우저로 밴드 로그인 화면을 열고, 10분 동안 쓸 수 있는 1회용 링크를 답한다.
// 링크를 연 폰은 화면을 보며 직접 로그인하고(전화번호와 문자 인증), 서버는 화면을 비추고 입력을 전달할 뿐이다.
// 로그인을 마치면 세션을 저장하고(운영은 Secret Manager의 새 버전) 텔레그램으로 알린다.
//
// 링크 토큰은 주소의 # 뒤에 있어서 서버 요청 로그에 남지 않는다. 화면은 토큰을 처음 낸 기기에 쿠키로 묶이고,
// 그 뒤의 요청은 쿠키로만 확인한다. 입력 내용, 화면, 쿠키 값은 기록하지 않는다.
package bandlogin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/telegram"
)

// Screen은 원격으로 조작하는 로그인 화면이다. band.LoginScreen이 실제 구현이다.
type Screen interface {
	Frame() (jpeg []byte, seq int, next <-chan struct{})
	Tap(x, y float64) error
	Type(text string) error
	Press(key string) error
	LoggedIn() bool
	State() ([]byte, error)
	Close() error
}

// Saver는 로그인한 세션을 저장하고, 알림에 붙일 설명(예: "band-state 버전 9")을 돌려준다.
type Saver interface {
	Save(ctx context.Context, state []byte) (string, error)
}

// Notifier는 허용한 대화방에 글을 보낸다. 로컬 실행에서는 nil이다.
type Notifier interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// deviceCookie는 화면을 처음 연 기기를 묶는 쿠키다. 이 서비스는 Firebase Hosting을 거치지 않아서 이름에 제약이 없다.
const deviceCookie = "bl_device"

// Server는 텔레그램 웹훅과 원격 로그인 화면을 처리한다.
type Server struct {
	OpenScreen func() (Screen, error)
	Saver      Saver
	Notifier   Notifier
	// WebhookSecret은 웹훅을 등록할 때 정한 비밀 토큰이다. ChatID는 /renew를 받아 줄 대화방이다.
	WebhookSecret string
	ChatID        int64
	// BaseURL은 링크 주소의 앞부분이다. 비우면 웹훅 요청의 Host로 만든다.
	BaseURL string
	TTL     time.Duration
	Poll    time.Duration
	Log     *slog.Logger
	Now     func() time.Time

	mu      sync.Mutex
	cur     *loginSession
	misses  int
	missWin time.Time
}

type loginSession struct {
	token   string
	device  string
	screen  Screen
	expires time.Time
	done    chan struct{}
	// result는 끝난 뒤 화면에 보여 줄 문구다.
	result string
}

// 틀린 토큰이나 쿠키가 1분에 이만큼 넘게 오면 그 1분 동안은 맞는 값도 받지 않는다.
const maxMisses = 20

// Handler는 서버의 경로를 묶는다.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /telegram", s.webhook)
	mux.HandleFunc("GET /login", s.page)
	mux.HandleFunc("POST /login/claim", s.claim)
	mux.HandleFunc("GET /login/frames", s.frames)
	mux.HandleFunc("POST /login/input", s.input)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return http.NewCrossOriginProtection().Handler(mux)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Start는 로그인 화면을 열고 링크 토큰을 돌려준다. 이미 열린 화면이 있으면 같은 토큰을 돌려준다.
// 아직 어느 기기도 토큰을 쓰지 않았을 때만 같은 토큰을 다시 준다. 이미 쓴 화면이면 오류다.
func (s *Server) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cur; c != nil {
		if c.device != "" {
			return "", errors.New("다른 기기에서 로그인 화면을 쓰고 있습니다. 끝나거나 시간이 지난 뒤에 다시 보내세요")
		}
		return c.token, nil
	}
	screen, err := s.OpenScreen()
	if err != nil {
		return "", fmt.Errorf("로그인 화면을 열지 못했습니다: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		_ = screen.Close()
		return "", err
	}
	c := &loginSession{token: token, screen: screen, expires: s.now().Add(s.TTL), done: make(chan struct{})}
	s.cur = c
	go s.watch(c)
	return token, nil
}

// watch는 로그인을 마치거나 시간이 지날 때까지 기다렸다가 화면을 닫는다.
func (s *Server) watch(c *loginSession) {
	ctx := context.Background()
	msg := ""
	for {
		if c.screen.LoggedIn() {
			msg = s.save(ctx, c)
			break
		}
		if s.now().After(c.expires) {
			msg = "시간이 지나서 로그인 화면을 닫았습니다. 세션은 바꾸지 않았습니다."
			break
		}
		time.Sleep(s.Poll)
	}
	s.mu.Lock()
	c.result = msg
	if s.cur == c {
		s.cur = nil
	}
	s.mu.Unlock()
	close(c.done)
	_ = c.screen.Close()
	s.notify(ctx, msg)
}

// save는 로그인한 세션을 저장하고 알림 문구를 돌려준다. 저장하지 못하면 지금의 세션을 그대로 둔다.
func (s *Server) save(ctx context.Context, c *loginSession) string {
	state, err := c.screen.State()
	if err != nil {
		s.Log.Error("세션을 꺼내지 못했습니다", "오류", err)
		return "로그인은 했지만 세션을 꺼내지 못했습니다. 세션은 바꾸지 않았습니다."
	}
	where, err := s.Saver.Save(ctx, state)
	if err != nil {
		s.Log.Error("세션을 저장하지 못했습니다", "오류", err)
		return "로그인은 했지만 세션을 저장하지 못했습니다. 세션은 바꾸지 않았습니다."
	}
	msg := "밴드 세션을 갱신했습니다(" + where + ")."
	if exp := tokenExpiry(state); !exp.IsZero() {
		msg += " 토큰 만료 " + exp.In(kst).Format("2006-01-02 15:04") + "."
	}
	s.Log.Info("밴드 세션을 갱신했습니다", "저장", where)
	return msg
}

var kst = time.FixedZone("KST", 9*60*60)

func tokenExpiry(state []byte) time.Time {
	ls, err := band.StateLifetimes(state)
	if err != nil {
		return time.Time{}
	}
	for _, l := range ls {
		if l.Name == "band_session" {
			return l.TokenExp
		}
	}
	return time.Time{}
}

func (s *Server) notify(ctx context.Context, text string) {
	if s.Notifier == nil {
		s.Log.Info(text)
		return
	}
	if err := s.Notifier.Send(ctx, s.ChatID, text); err != nil {
		s.Log.Error("알림을 보내지 못했습니다", "오류", err)
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// webhook은 텔레그램 업데이트를 받는다. 허용한 대화방의 /renew에만 링크를 답한다.
// 다른 요청에도 200을 줘서 텔레그램이 같은 업데이트를 다시 보내지 않게 한다.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if s.WebhookSecret == "" || !equal(r.Header.Get(telegram.SecretHeader), s.WebhookSecret) {
		http.NotFound(w, r)
		return
	}
	var u telegram.Update
	if err := decodeJSON(r, &u, 1<<20); err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	if u.ChatID() != s.ChatID {
		s.Log.Warn("허용하지 않은 대화방의 메시지를 무시했습니다")
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx := r.Context()
	switch u.Command() {
	case "renew":
		token, err := s.Start()
		if err != nil {
			s.notify(ctx, err.Error())
			break
		}
		s.notify(ctx, "10분 안에 폰에서 열어 밴드에 로그인하세요. 처음 연 기기에서만 열립니다.\n"+s.baseURL(r)+"/login#"+token)
	case "start", "help":
		s.notify(ctx, "/renew를 보내면 밴드에 다시 로그인할 링크를 보냅니다.")
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) baseURL(r *http.Request) string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/")
	}
	return "https://" + r.Host
}

// limited는 틀린 시도가 많아서 지금은 받지 않는지 알려 준다. miss가 true면 틀린 시도로 센다.
func (s *Server) limited(miss bool) bool {
	now := s.now()
	if now.Sub(s.missWin) > time.Minute {
		s.missWin, s.misses = now, 0
	}
	if miss {
		s.misses++
	}
	return s.misses > maxMisses
}

// claim은 링크 토큰을 받아 이 기기를 화면에 묶는다. 같은 기기가 다시 열면(새로 고침) 그대로 허용한다.
func (s *Server) claim(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	_ = decodeJSON(r, &in, 1<<10)
	cookie, _ := r.Cookie(deviceCookie)

	s.mu.Lock()
	c := s.cur
	ok := c != nil && !s.limited(false) && s.now().Before(c.expires)
	if ok {
		if c.device == "" {
			ok = equal(in.Token, c.token)
		} else {
			// 새로 고침하면 주소에 토큰이 없으므로 묶인 기기는 쿠키만 본다.
			ok = cookie != nil && equal(cookie.Value, c.device)
		}
	}
	if !ok {
		s.limited(true)
		s.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	if c.device == "" {
		dev, err := randomToken()
		if err != nil {
			s.mu.Unlock()
			http.Error(w, "기기 값을 만들지 못했습니다", http.StatusInternalServerError)
			return
		}
		c.device = dev
		s.Log.Info("로그인 화면을 기기에 묶었습니다")
	}
	dev := c.device
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: dev, Path: "/login", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(s.TTL / time.Second),
	})
	w.WriteHeader(http.StatusNoContent)
}

// session은 요청의 기기 쿠키에 묶인 화면을 찾는다. 없으면 404를 쓰고 nil을 돌려준다.
func (s *Server) session(w http.ResponseWriter, r *http.Request) *loginSession {
	cookie, _ := r.Cookie(deviceCookie)
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur
	if c == nil || c.device == "" || cookie == nil || s.limited(false) || !equal(cookie.Value, c.device) {
		s.limited(true)
		http.NotFound(w, r)
		return nil
	}
	return c
}

// frames는 화면이 바뀔 때마다 JPEG를 base64로 보내는 SSE다. 끝나면 done 이벤트로 결과 문구를 보낸다.
func (s *Server) frames(w http.ResponseWriter, r *http.Request) {
	c := s.session(w, r)
	if c == nil {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "스트리밍을 할 수 없습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	last := -1
	for {
		jpeg, seq, next := c.screen.Frame()
		if seq != last && jpeg != nil {
			last = seq
			if _, err := fmt.Fprintf(w, "data: %s\n\n", base64.StdEncoding.EncodeToString(jpeg)); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			s.mu.Lock()
			msg := c.result
			s.mu.Unlock()
			_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", strings.ReplaceAll(msg, "\n", " "))
			flusher.Flush()
			return
		case <-next:
		case <-time.After(15 * time.Second):
			// 프록시가 연결을 끊지 않게 주석 줄을 보낸다.
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		}
	}
}

// input은 화면 누르기, 글자, 특수 키를 브라우저에 전달한다. 입력 내용은 기록하지 않는다.
func (s *Server) input(w http.ResponseWriter, r *http.Request) {
	c := s.session(w, r)
	if c == nil {
		return
	}
	var in struct {
		Type string  `json:"type"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		Text string  `json:"text"`
		Key  string  `json:"key"`
	}
	if err := decodeJSON(r, &in, 4<<10); err != nil {
		http.Error(w, "입력을 읽지 못했습니다", http.StatusBadRequest)
		return
	}
	var err error
	switch in.Type {
	case "tap":
		err = c.screen.Tap(in.X, in.Y)
	case "text":
		if len(in.Text) > 256 {
			http.Error(w, "글자가 너무 깁니다", http.StatusBadRequest)
			return
		}
		err = c.screen.Type(in.Text)
	case "key":
		err = c.screen.Press(in.Key)
	default:
		http.Error(w, "모르는 입력입니다", http.StatusBadRequest)
		return
	}
	if err != nil {
		// 오류 문구에 입력 내용이 들어가지 않도록 종류만 남긴다.
		s.Log.Warn("입력을 전달하지 못했습니다", "종류", in.Type)
		http.Error(w, "입력을 전달하지 못했습니다", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func equal(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
