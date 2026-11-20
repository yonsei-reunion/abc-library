package bandlogin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/cloudrun"
)

type fakeJob struct {
	mu      sync.Mutex
	execs   []cloudrun.Execution
	listErr error
	runErr  error
	runs    [][]string
	envs    []map[string]string
}

func (j *fakeJob) Executions(_ context.Context, n int) ([]cloudrun.Execution, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n < 1 {
		return nil, errors.New("n이 0이다")
	}
	return j.execs, j.listErr
}

func (j *fakeJob) Run(_ context.Context, args []string, env map[string]string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.runErr != nil {
		return "", j.runErr
	}
	j.runs = append(j.runs, args)
	j.envs = append(j.envs, env)
	// 실제 API처럼 실행한 뒤에는 목록에 실행 중으로 나온다.
	j.execs = append([]cloudrun.Execution{{Name: "abc-batch-new", Args: args, Created: time.Now()}}, j.execs...)
	return "abc-batch-new", nil
}

// 픽스처의 시각은 2026-11-12 18:00 KST다.
var (
	sameDay  = time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC) // 11-12 03:00 KST
	lastDay  = time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC) // 11-11 03:00 KST
	dayStart = time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC) // 11-12 00:00 KST
)

func done(name string, created time.Time, ok bool, args ...string) cloudrun.Execution {
	return cloudrun.Execution{Name: name, Args: args, Created: created, Completed: created.Add(time.Minute), Succeeded: ok}
}

func (f *fixture) command(t *testing.T, text string) string {
	t.Helper()
	if code := f.webhook(chatID, text, secret); code != 200 {
		t.Fatalf("웹훅 상태 %d", code)
	}
	return <-f.note.sent
}

func TestBatchCommand(t *testing.T) {
	for _, c := range []struct {
		name  string
		cmd   string
		execs []cloudrun.Execution
		want  string
		args  string
	}{
		{"기록 없음", "/batch", nil, "일일 배치를 시작했습니다(실행 abc-batch-new)", "run"},
		{"오늘 실패만 있음", "/batch", []cloudrun.Execution{done("a", sameDay, false)}, "일일 배치를 시작했습니다", "run"},
		{"어제 성공", "/batch", []cloudrun.Execution{done("a", lastDay, true)}, "일일 배치를 시작했습니다", "run"},
		{"자정 직전 성공은 어제", "/batch", []cloudrun.Execution{done("a", dayStart.Add(-time.Second), true)}, "일일 배치를 시작했습니다", "run"},
		{"오늘 주간 요약만 성공", "/batch", []cloudrun.Execution{done("w", sameDay, true, "weekly")}, "일일 배치를 시작했습니다", "run"},
		{"오늘 성공", "/batch", []cloudrun.Execution{done("a", sameDay, true)}, "오늘 일일 배치가 이미 성공했습니다(실행 a, 03:00 시작)", ""},
		{"자정에 성공", "/batch", []cloudrun.Execution{done("a", dayStart, true)}, "이미 성공했습니다", ""},
		{"인자를 준 일일 배치 성공", "/batch", []cloudrun.Execution{done("a", sameDay, true, "run", "-limit", "5")}, "이미 성공했습니다", ""},
		{"실행 중", "/batch", []cloudrun.Execution{{Name: "r", Created: sameDay}}, "실행 중인 배치가 있습니다(실행 r, 03:00 시작)", ""},
		{"주간 요약", "/weekly", nil, "대화방 주간 요약을 시작했습니다", "weekly"},
		{"오늘 일일 배치 뒤 주간 요약", "/weekly", []cloudrun.Execution{done("a", sameDay, true)}, "대화방 주간 요약을 시작했습니다", "weekly"},
		{"주간 요약 성공 뒤 다시", "/weekly@abc_bot", []cloudrun.Execution{done("w", sameDay, true, "weekly")}, "시작했습니다", "weekly"},
		{"실행 중에 주간 요약", "/weekly", []cloudrun.Execution{{Name: "r", Created: sameDay, Args: []string{"weekly"}}}, "실행 중인 배치가 있습니다", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			job := &fakeJob{execs: c.execs}
			f.srv.Batch = job
			if msg := f.command(t, c.cmd); !strings.Contains(msg, c.want) {
				t.Errorf("답 %q, 기대 %q", msg, c.want)
			}
			if c.args == "" {
				if len(job.runs) != 0 {
					t.Errorf("실행하면 안 된다: %v", job.runs)
				}
				return
			}
			if len(job.runs) != 1 || strings.Join(job.runs[0], " ") != c.args || job.envs[0][ReportEnv] != "1" {
				t.Errorf("실행 %v %v, 기대 %s", job.runs, job.envs, c.args)
			}
		})
	}
}

func TestBatchCommandTwiceRunsOnce(t *testing.T) {
	f := newFixture(t)
	job := &fakeJob{}
	f.srv.Batch = job
	f.command(t, "/batch")
	if msg := f.command(t, "/weekly"); !strings.Contains(msg, "실행 중인 배치가 있습니다") {
		t.Errorf("두 번째 답 %q", msg)
	}
	if len(job.runs) != 1 {
		t.Errorf("한 번만 실행해야 한다: %v", job.runs)
	}
}

func TestBatchCommandConcurrent(t *testing.T) {
	f := newFixture(t)
	job := &fakeJob{}
	f.srv.Batch = job
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { f.webhook(chatID, "/batch", secret) })
	}
	wg.Wait()
	if len(job.runs) != 1 {
		t.Errorf("동시에 보내도 한 번만 실행해야 한다: %v", job.runs)
	}
}

func TestBatchCommandFailures(t *testing.T) {
	f := newFixture(t)
	if msg := f.command(t, "/batch"); !strings.Contains(msg, "설정하지 않았습니다") {
		t.Errorf("Job이 없을 때 %q", msg)
	}
	job := &fakeJob{listErr: errors.New("HTTP 403 run.executions.list")}
	f.srv.Batch = job
	if msg := f.command(t, "/batch"); !strings.Contains(msg, "실행 기록을 읽지 못해서") || len(job.runs) != 0 {
		t.Errorf("목록 실패 %q %v", msg, job.runs)
	}
	job.listErr, job.runErr = nil, errors.New("HTTP 403 run.jobs.runWithOverrides")
	if msg := f.command(t, "/weekly"); !strings.Contains(msg, "대화방 주간 요약을 실행하지 못했습니다") {
		t.Errorf("실행 실패 %q", msg)
	}
	// 오류 내용은 로그에만 남긴다.
	if !strings.Contains(f.logs.String(), "runWithOverrides") {
		t.Errorf("로그에 오류가 없다: %s", f.logs.String())
	}
}

func TestBatchCommandIgnoresOtherChats(t *testing.T) {
	f := newFixture(t)
	job := &fakeJob{}
	f.srv.Batch = job
	if code := f.webhook(7, "/batch", secret); code != 200 {
		t.Errorf("상태 %d", code)
	}
	if code := f.webhook(chatID, "/batch", "wrong"); code != 404 {
		t.Errorf("틀린 비밀 상태 %d", code)
	}
	if len(job.runs) != 0 || len(f.note.msgs) != 0 {
		t.Errorf("다른 대화방이나 틀린 비밀로는 실행하지 않는다: %v %v", job.runs, f.note.msgs)
	}
}

func TestHelpListsBatchCommands(t *testing.T) {
	f := newFixture(t)
	msg := f.command(t, "/help")
	for _, cmd := range []string{"/renew", "/batch", "/weekly"} {
		if !strings.Contains(msg, cmd) {
			t.Errorf("도움말에 %s가 없다: %q", cmd, msg)
		}
	}
}
