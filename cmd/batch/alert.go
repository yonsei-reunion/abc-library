package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/pipeline"
	"github.com/yonsei-alumni/abc-library/internal/telegram"
)

// sessionWarnBefore는 세션 만료를 미리 알리는 기준이다. 배치는 하루 한 번 돌기 때문에 다음 실행 전에 끝나는지를 본다.
const sessionWarnBefore = 24 * time.Hour

// alertSession은 밴드 세션이 만료되어 실패했거나 다음 실행 전에 끝나면 텔레그램으로 알린다.
// TELEGRAM_BOT_TOKEN과 TELEGRAM_CHAT_ID가 없으면 아무것도 하지 않는다. 알림이 실패해도 배치 결과는 바꾸지 않는다.
func alertSession(ctx context.Context, log *slog.Logger, cmd string, runErr error) {
	if _, ok := telegramTarget(); !ok {
		return
	}
	var exp time.Time
	if ls, err := band.SessionLifetimes(statePath()); err == nil {
		exp = band.SessionTokenExp(ls)
	}
	if msg := sessionNotice(cmd, exp, time.Now(), runErr); msg != "" {
		sendTelegram(ctx, log, msg)
	}
}

// telegramTarget은 알림을 보낼 대화방을 읽는다. TELEGRAM_BOT_TOKEN과 TELEGRAM_CHAT_ID가 없으면 false다.
func telegramTarget() (int64, bool) {
	chat, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	return chat, os.Getenv("TELEGRAM_BOT_TOKEN") != "" && err == nil && chat != 0
}

// telegramAPI는 Bot API 주소다. 비우면 telegram.Client의 기본값을 쓴다. 테스트에서 바꾼다.
var telegramAPI string

// sendTelegram은 알림을 보낸다. 실패해도 배치 결과는 바꾸지 않는다.
func sendTelegram(ctx context.Context, log *slog.Logger, msg string) {
	chat, ok := telegramTarget()
	if !ok {
		return
	}
	if err := (&telegram.Client{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), BaseURL: telegramAPI}).Send(ctx, chat, msg); err != nil {
		log.Warn("텔레그램 알림을 보내지 못했습니다", "오류", err)
	}
}

// reportResult는 텔레그램 /batch나 /weekly로 시작한 실행(TELEGRAM_REPORT=1)이면 끝난 결과를 알린다.
// 정기 실행은 알리지 않는다. 오류 내용은 Job 로그에만 남기고 텔레그램에는 보내지 않는다.
func reportResult(ctx context.Context, log *slog.Logger, cmd, summary string, runErr error) {
	if os.Getenv("TELEGRAM_REPORT") != "1" {
		return
	}
	if msg := resultNotice(cmd, summary, runErr); msg != "" {
		sendTelegram(ctx, log, msg)
	}
}

// resultNotice는 결과 문구를 만든다. 세션 만료로 실패했으면 alertSession이 알리므로 빈 문자열이다.
func resultNotice(cmd, summary string, runErr error) string {
	switch {
	case errors.Is(runErr, band.ErrSessionExpired):
		return ""
	case runErr != nil:
		return "batch " + cmd + " 명령이 실패했습니다. Cloud Run Job 로그를 확인하세요."
	case summary == "":
		return "batch " + cmd + " 명령을 마쳤습니다."
	}
	return "batch " + cmd + " 명령을 마쳤습니다. " + summary
}

// runSummary는 일일 배치의 처리 건수를 한 줄로 만든다. 글 내용은 담지 않는다.
func runSummary(r pipeline.Report) string {
	return fmt.Sprintf("최신 글 %d, 확인 %d, 추출 %d, 변경 없음 %d, 없는 글 %d, 실패 %d, 책 %d.",
		r.LatestPostNo, r.Checked, r.Extracted, r.Unchanged, r.Missing, r.Failed, r.Books)
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
	return "밴드 세션이 " + exp.In(book.Seoul).Format("01-02 15:04") + "에 만료됩니다. 다음 배치 전에 /renew를 보내 다시 로그인하세요."
}
