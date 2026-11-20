package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/yonsei-alumni/abc-library/internal/weekly"
)

type fakeBand struct {
	posts   []string
	listErr error
	created []string
}

func (f *fakeBand) Posted(marker string) (bool, error) {
	if f.listErr != nil {
		return false, f.listErr
	}
	for _, p := range f.posts {
		if strings.Contains(p, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeBand) Write(content, _ string) error {
	f.created = append(f.created, content)
	f.posts = append(f.posts, content)
	return nil
}

func TestPostWeekly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sum := weekly.Summary{Room: "ABC", Start: "2026-10-19", End: "2026-10-25", Count: 3, Keywords: []string{"SF"}}

	band := &fakeBand{posts: []string{"다른 글"}}
	if got, err := postWeekly(log, band, sum); err != nil || len(band.created) != 1 || got != "2026-10-19~2026-10-25 대화방 주간 요약을 밴드에 올렸습니다(책 0, 링크 0)." {
		t.Fatalf("첫 실행: %q err=%v created=%d", got, err, len(band.created))
	}
	// 다시 실행하면 같은 기간의 글이 있으므로 올리지 않는다.
	if got, err := postWeekly(log, band, sum); err != nil || len(band.created) != 1 || !strings.Contains(got, "이미 밴드에 있어서") {
		t.Errorf("다시 실행: %q err=%v created=%d", got, err, len(band.created))
	}
	// 다음 주는 올린다.
	next := sum
	next.Start, next.End = "2026-10-26", "2026-11-01"
	if _, err := postWeekly(log, band, next); err != nil || len(band.created) != 2 {
		t.Errorf("다음 주: err=%v created=%d", err, len(band.created))
	}

	// 메시지가 없는 주는 올리지 않는다.
	empty := &fakeBand{}
	if got, err := postWeekly(log, empty, weekly.Summary{Start: "2026-10-19", End: "2026-10-25"}); err != nil || len(empty.created) != 0 || !strings.Contains(got, "메시지가 없어서") {
		t.Errorf("빈 주: %q err=%v created=%d", got, err, len(empty.created))
	}

	// 이미 올렸는지 확인하지 못하면 올리지 않고 실패한다.
	broken := &fakeBand{listErr: errors.New("목록 실패")}
	if _, err := postWeekly(log, broken, sum); err == nil || len(broken.created) != 0 {
		t.Errorf("목록 실패: err=%v created=%d", err, len(broken.created))
	}
}
