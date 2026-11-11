// bandlogin은 폰에서 밴드에 다시 로그인하게 하는 서버다(Cloud Run 서비스 abc-band-login).
//
// 텔레그램에서 /renew를 보내면 로그인 화면 링크를 답하고, 로그인을 마치면 세션을 Secret Manager의 새 버전으로 올린다.
// 자세한 흐름은 internal/bandlogin과 docs/login-plan-b.md에 있다.
//
//	bandlogin          운영. 텔레그램 웹훅을 받는다.
//	bandlogin -local   이 컴퓨터에서 시험한다. 링크를 출력하고, 로그인하면 세션을 BAND_STATE_PATH 파일에 저장하고 끝난다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/bandlogin"
	"github.com/yonsei-alumni/abc-library/internal/config"
	"github.com/yonsei-alumni/abc-library/internal/secretmanager"
	"github.com/yonsei-alumni/abc-library/internal/telegram"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	local := flag.Bool("local", false, "텔레그램 없이 이 컴퓨터에서 시험하고 세션을 파일에 저장한다")
	flag.Parse()
	if err := run(log, *local); err != nil {
		log.Error("bandlogin 실패", "오류", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, local bool) error {
	if err := band.InstallHeadlessBrowser(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	port := config.Get("PORT", "8080")
	srv := &bandlogin.Server{
		OpenScreen: func() (bandlogin.Screen, error) { return band.OpenLoginScreen() },
		TTL:        10 * time.Minute,
		Poll:       2 * time.Second,
		Log:        log,
	}
	finished := make(chan struct{})
	if local {
		srv.BaseURL = "http://localhost:" + port
		srv.Saver = fileSaver{path: config.Get("BAND_STATE_PATH", "band-state.json")}
		srv.Notifier = localNotifier{done: finished}
	} else {
		if err := configure(srv, log); err != nil {
			return err
		}
	}

	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = hs.Serve(ln) }()
	log.Info("bandlogin을 시작했습니다", "포트", port, "로컬", local)

	if local {
		token, err := srv.Start()
		if err != nil {
			return err
		}
		// 로컬 시험이라서 링크를 출력한다. 운영에서는 텔레그램으로만 보낸다.
		fmt.Printf("10분 안에 브라우저에서 여세요: %s/login#%s\n", srv.BaseURL, token)
	}
	select {
	case <-ctx.Done():
	case <-finished:
		// 결과 문구가 화면에 전달되도록 잠깐 기다린다.
		time.Sleep(time.Second)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

// configure는 운영 설정(텔레그램과 Secret Manager)을 읽는다.
func configure(srv *bandlogin.Server, log *slog.Logger) error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	srv.WebhookSecret = os.Getenv("TELEGRAM_WEBHOOK_SECRET")
	chat, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	secret := config.Get("BAND_STATE_SECRET", "band-state")
	switch {
	case token == "" || srv.WebhookSecret == "":
		return errors.New("TELEGRAM_BOT_TOKEN과 TELEGRAM_WEBHOOK_SECRET이 필요합니다")
	case err != nil || chat == 0:
		return errors.New("TELEGRAM_CHAT_ID가 숫자가 아닙니다")
	case project == "":
		return errors.New("GOOGLE_CLOUD_PROJECT가 필요합니다")
	}
	srv.ChatID = chat
	srv.BaseURL = os.Getenv("BASE_URL")
	srv.Notifier = &telegram.Client{Token: token}
	srv.Saver = secretSaver{sm: &secretmanager.Client{Project: project}, secret: secret, log: log}
	return nil
}

// secretSaver는 세션을 Secret Manager의 새 버전으로 올리고 이전 버전을 끈다(scripts/band-secret.sh와 같다).
// 새 버전을 올리지 못하면 이전 버전을 건드리지 않는다.
type secretSaver struct {
	sm     *secretmanager.Client
	secret string
	log    *slog.Logger
}

func (s secretSaver) Save(ctx context.Context, state []byte) (string, error) {
	v, err := s.sm.AddVersion(ctx, s.secret, state)
	if err != nil {
		return "", err
	}
	disabled, err := s.sm.DisableOthers(ctx, s.secret, v)
	if err != nil {
		// 배치는 latest를 읽으므로 새 버전이 올라갔으면 성공으로 본다.
		s.log.Warn("이전 버전을 끄지 못했습니다", "오류", err)
	}
	s.log.Info("세션 비밀에 새 버전을 올렸습니다", "버전", v, "끈 버전", disabled)
	return fmt.Sprintf("%s 버전 %s", s.secret, v), nil
}

// fileSaver는 로컬 시험에서 세션을 파일로 저장한다(make band-login과 같은 파일).
type fileSaver struct{ path string }

func (f fileSaver) Save(_ context.Context, state []byte) (string, error) {
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, state, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return "", err
	}
	return f.path, nil
}

// localNotifier는 로컬 시험에서 결과를 출력하고 서버를 끝내게 한다.
type localNotifier struct{ done chan struct{} }

func (n localNotifier) Send(_ context.Context, _ int64, text string) error {
	fmt.Println(text)
	close(n.done)
	return nil
}
