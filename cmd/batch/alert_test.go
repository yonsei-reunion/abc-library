package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
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
