package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

func writeProbeState(t *testing.T, path string, session, rt time.Time) {
	t.Helper()
	var cookies []string
	if !session.IsZero() {
		cookies = append(cookies, fmt.Sprintf(`{"name":"band_session","value":%q,"expires":-1}`, probeJWT(session)))
	}
	if !rt.IsZero() {
		cookies = append(cookies, fmt.Sprintf(`{"name":"rt","value":%q,"expires":-1}`, probeJWT(rt)))
	}
	cookies = append(cookies, `{"name":"SESSION","value":"opaque","expires":-1}`)
	if err := os.WriteFile(path, []byte(`{"cookies":[`+strings.Join(cookies, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeClock은 sleep할 때만 시간이 흐른다.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) sleep(d time.Duration)   { c.t = c.t.Add(d) }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

var probeExp = time.Date(2026, 11, 11, 1, 34, 26, 0, time.UTC)

func TestProbePlan(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		{"만료 하루 전이면 모두", probeExp.Add(-24 * time.Hour), 9},
		{"18시간 전 정각은 건너뛴다", probeExp.Add(-18 * time.Hour), 8},
		{"1시간 전 정각부터는 30분 전 이후만", probeExp.Add(-time.Hour), 4},
		{"만료 직후에는 만료 뒤 한 번만", probeExp.Add(time.Minute), 1},
		{"만료 10분 뒤에는 없다", probeExp.Add(10 * time.Minute), 0},
	}
	for _, c := range cases {
		plan := probePlan(probeExp, c.now)
		if len(plan) != c.want {
			t.Errorf("%s: %d개, want %d", c.name, len(plan), c.want)
		}
		for i := 1; i < len(plan); i++ {
			if !plan[i].After(plan[i-1]) {
				t.Errorf("%s: 시각이 순서대로가 아니다: %v", c.name, plan)
			}
		}
	}
}

func newTestProbe(t *testing.T, clock *fakeClock, visit func(path string) (bool, error)) (sessionProbe, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	writeProbeState(t, path, probeExp, probeExp)
	var buf bytes.Buffer
	return sessionProbe{
		path:  path,
		visit: visit,
		now:   clock.now,
		sleep: clock.sleep,
		log:   slog.New(slog.NewTextHandler(&buf, nil)),
	}, &buf
}

func TestSessionProbeNotExtended(t *testing.T) {
	clock := &fakeClock{probeExp.Add(-20 * time.Hour)}
	var visits []time.Time
	p, buf := newTestProbe(t, clock, func(string) (bool, error) {
		visits = append(visits, clock.now())
		clock.advance(30 * time.Second) // 여는 데 걸리는 시간
		return clock.now().Before(probeExp), nil
	})
	extended, err := p.run()
	if err != nil || extended {
		t.Fatalf("extended %v, err %v", extended, err)
	}
	plan := probePlan(probeExp, probeExp.Add(-20*time.Hour))
	if len(visits) != len(plan) {
		t.Fatalf("%d번 열었다, want %d", len(visits), len(plan))
	}
	for i, v := range visits {
		if v.Before(plan[i]) || v.Sub(plan[i]) > time.Second {
			t.Errorf("%d번째를 %v에 열었다, want %v", i, v, plan[i])
		}
	}
	out := buf.String()
	if !strings.Contains(out, "재발급 없음") || strings.Contains(out, "연장=true") {
		t.Errorf("로그: %s", out)
	}
	if !strings.Contains(out, "로그인=false") {
		t.Errorf("만료 뒤에 연 것은 로그인=false여야 한다: %s", out)
	}
	if strings.Contains(out, "eyJ") {
		t.Errorf("토큰 값이 로그에 남았다: %s", out)
	}
}

func TestSessionProbeExtended(t *testing.T) {
	clock := &fakeClock{probeExp.Add(-90 * time.Minute)}
	calls := 0
	p, buf := newTestProbe(t, clock, nil)
	p.visit = func(path string) (bool, error) {
		calls++
		if calls == 2 { // 30분 전에 열었을 때 토큰을 다시 받는다
			later := probeExp.Add(48 * time.Hour)
			writeProbeState(t, path, later, later)
		}
		return true, nil
	}
	extended, err := p.run()
	if err != nil || !extended {
		t.Fatalf("extended %v, err %v", extended, err)
	}
	out := buf.String()
	if strings.Count(out, "연장=true") != 1 {
		t.Errorf("연장은 받은 한 번만 표시해야 한다: %s", out)
	}
	if !strings.Contains(out, "재발급됨") {
		t.Errorf("로그: %s", out)
	}
}

func TestSessionProbeVisitError(t *testing.T) {
	clock := &fakeClock{probeExp.Add(-4 * time.Minute)}
	calls := 0
	p, buf := newTestProbe(t, clock, func(string) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("연결 끊김")
		}
		return false, nil
	})
	extended, err := p.run()
	if err != nil || extended {
		t.Fatalf("extended %v, err %v", extended, err)
	}
	if calls != 2 || !strings.Contains(buf.String(), "열지 못했습니다") {
		t.Errorf("한 번 실패해도 다음 시각에 다시 열어야 한다: calls %d, %s", calls, buf.String())
	}
}

func TestSessionProbeMissingCookieKeepsPrevious(t *testing.T) {
	clock := &fakeClock{probeExp.Add(-4 * time.Minute)}
	calls := 0
	p, buf := newTestProbe(t, clock, nil)
	p.visit = func(path string) (bool, error) {
		calls++
		if calls == 1 { // 만료 직전에 밴드가 쿠키를 지운다
			writeProbeState(t, path, time.Time{}, time.Time{})
		} else { // 다음에 다시 받은 토큰이 처음보다 늦게 끝나면 연장이다
			writeProbeState(t, path, probeExp.Add(time.Hour), time.Time{})
		}
		return true, nil
	}
	extended, err := p.run()
	if err != nil || !extended {
		t.Fatalf("extended %v, err %v: %s", extended, err, buf.String())
	}
	if !strings.Contains(buf.String(), "band_session=없음") {
		t.Errorf("지워진 쿠키는 없음으로 남겨야 한다: %s", buf.String())
	}
}

func TestSessionProbeNoSessionOrNoTime(t *testing.T) {
	clock := &fakeClock{probeExp.Add(-time.Hour)}
	p, _ := newTestProbe(t, clock, func(string) (bool, error) {
		t.Fatal("열면 안 된다")
		return false, nil
	})
	writeProbeState(t, p.path, time.Time{}, probeExp)
	if _, err := p.run(); err == nil {
		t.Error("band_session이 없으면 오류여야 한다")
	}

	clock.t = probeExp.Add(time.Hour)
	writeProbeState(t, p.path, probeExp, probeExp)
	extended, err := p.run()
	if err != nil || extended {
		t.Errorf("남은 시각이 없으면 열지 않고 끝내야 한다: %v, %v", extended, err)
	}

	p.path = filepath.Join(t.TempDir(), "missing.json")
	if _, err := p.run(); err == nil {
		t.Error("세션 파일이 없으면 오류여야 한다")
	}
}
