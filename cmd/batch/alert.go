package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/telegram"
)

// sessionWarnBefore는 세션 만료를 미리 알리는 기준이다. 배치는 하루 한 번 돌기 때문에 다음 실행 전에 끝나는지를 본다.
const sessionWarnBefore = 24 * time.Hour

// alertSession은 밴드 세션이 만료되어 실패했거나 다음 실행 전에 끝나면 텔레그램으로 알린다.
// TELEGRAM_BOT_TOKEN과 TELEGRAM_CHAT_ID가 없으면 아무것도 하지 않는다. 알림이 실패해도 배치 결과는 바꾸지 않는다.
func alertSession(ctx context.Context, log *slog.Logger, cmd string, runErr error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chat, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	if token == "" || err != nil || chat == 0 {
		return
	}
	var exp time.Time
	if ls, err := band.SessionLifetimes(statePath()); err == nil {
		for _, l := range ls {
			if l.Name == "band_session" {
				exp = l.TokenExp
			}
		}
	}
	msg := sessionNotice(cmd, exp, time.Now(), runErr)
	if msg == "" {
		return
	}
	if err := (&telegram.Client{Token: token}).Send(ctx, chat, msg); err != nil {
		log.Warn("세션 알림을 보내지 못했습니다", "오류", err)
	}
}

// sessionNotice는 알릴 문구를 만든다. 알릴 것이 없으면 빈 문자열이다.
func sessionNotice(cmd string, exp, now time.Time, runErr error) string {
	if errors.Is(runErr, band.ErrSessionExpired) {
		return "밴드 세션이 만료되어 batch " + cmd + " 명령이 실패했습니다. /renew를 보내 다시 로그인하세요."
	}
	if exp.IsZero() || exp.Sub(now) >= sessionWarnBefore {
		return ""
	}
	if !exp.After(now) {
		return "밴드 세션이 만료되었습니다. /renew를 보내 다시 로그인하세요."
	}
	return "밴드 세션이 " + exp.In(kstZone).Format("01-02 15:04") + "에 만료됩니다. 다음 배치 전에 /renew를 보내 다시 로그인하세요."
}

var kstZone = time.FixedZone("KST", 9*60*60)
