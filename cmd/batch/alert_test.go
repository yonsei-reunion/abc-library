package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/pipeline"
)

func TestSessionNotice(t *testing.T) {
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC) // 11-13 03:00 KST
	expired := fmt.Errorf("게시글 목록을 읽지 못했습니다: %w", band.ErrSessionExpired)
	for _, c := range []struct {
		name string
		exp  time.Time
		err  error
		want string
	}{
		{"만료로 실패", now.Add(time.Hour), expired, "batch run 명령이 실패했습니다"},
		{"만료로 실패, exp 모름", time.Time{}, expired, "batch run 명령이 실패했습니다"},
		{"하루 넘게 남음", now.Add(sessionWarnBefore), nil, ""},
		{"하루 안 남음", now.Add(sessionWarnBefore - time.Minute), nil, "11-14 02:59에 만료됩니다"},
		{"이미 지남", now, nil, "만료되었습니다"},
		{"exp 모름", time.Time{}, nil, ""},
		{"다른 오류", now.Add(48 * time.Hour), errors.New("DB 실패"), ""},
	} {
		got := sessionNotice("run", c.exp, now, c.err)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, 기대 %q", c.name, got, c.want)
		}
	}
}

func TestResultNotice(t *testing.T) {
	expired := fmt.Errorf("게시글 목록을 읽지 못했습니다: %w", band.ErrSessionExpired)
	sum := runSummary(pipeline.Report{LatestPostNo: 210, Checked: 12, Extracted: 2, Unchanged: 10, Books: 3})
	for _, c := range []struct {
		name, cmd, summary string
		err                error
		want               string
	}{
		{"일일 배치 성공", "run", sum, nil, "batch run 명령을 마쳤습니다. 최신 글 210, 확인 12, 추출 2, 변경 없음 10, 없는 글 0, 실패 0, 책 3."},
		{"주간 요약 성공", "weekly", "요약을 올렸습니다.", nil, "batch weekly 명령을 마쳤습니다. 요약을 올렸습니다."},
		{"결과 없음", "weekly", "", nil, "batch weekly 명령을 마쳤습니다."},
		{"실패", "run", sum, errors.New("DB 비밀번호 틀림"), "batch run 명령이 실패했습니다. Cloud Run Job 로그를 확인하세요."},
		{"세션 만료는 alertSession이 알린다", "run", sum, expired, ""},
	} {
		if got := resultNotice(c.cmd, c.summary, c.err); got != c.want {
			t.Errorf("%s: %q, 기대 %q", c.name, got, c.want)
		}
	}
}

func TestReportResultOnlyWhenRequested(t *testing.T) {
	var sent []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sent = append(sent, in.Text)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	telegramAPI = ts.URL
	t.Cleanup(func() { telegramAPI = "" })
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "42")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// 정기 실행(TELEGRAM_REPORT 없음)은 알리지 않는다.
	t.Setenv("TELEGRAM_REPORT", "")
	reportResult(ctx, log, "run", "x", nil)
	t.Setenv("TELEGRAM_REPORT", "0")
	reportResult(ctx, log, "run", "x", nil)
	if len(sent) != 0 {
		t.Fatalf("정기 실행은 알리면 안 된다: %v", sent)
	}
	t.Setenv("TELEGRAM_REPORT", "1")
	reportResult(ctx, log, "run", "x", nil)
	if len(sent) != 1 || sent[0] != "batch run 명령을 마쳤습니다. x" {
		t.Errorf("보낸 알림 %v", sent)
	}
	// 대화방 번호가 없으면 보내지 않는다.
	t.Setenv("TELEGRAM_CHAT_ID", "")
	reportResult(ctx, log, "run", "x", nil)
	if len(sent) != 1 {
		t.Errorf("대화방 번호가 없으면 보내면 안 된다: %v", sent)
	}
}
