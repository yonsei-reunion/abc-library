package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/book"
)

// probeOffsets는 밴드 홈을 열어 볼 시각을 토큰 만료 시각에서 거꾸로 센 것이다. 음수는 만료 뒤다.
var probeOffsets = []time.Duration{
	18 * time.Hour, 12 * time.Hour, 6 * time.Hour, 2 * time.Hour, time.Hour,
	30 * time.Minute, 10 * time.Minute, 3 * time.Minute, -5 * time.Minute,
}

// probeTokens는 만료 시각을 비교하는 세션 토큰이다.
var probeTokens = []string{"band_session", "rt"}

// probe는 밴드가 세션 토큰을 만료 전에 다시 발급하는지 시험한다(batch probe).
// 세션 파일을 임시 디렉터리에 복사하고, 만료 18시간 전부터 만료 5분 뒤까지 밴드 홈을 몇 번 열어
// band_session과 rt의 exp가 늘었는지 로그에 남긴다. 쿠키 값은 남기지 않고 운영 비밀에도 쓰지 않는다.
// 지금보다 앞선 시각은 건너뛰므로 운영 배치가 이 세션을 마지막으로 쓴 뒤에 시작한다.
// 토큰을 바꿔 주면서 예전 토큰을 끊는 서버라면 운영 세션이 일찍 끝날 수 있기 때문이다.
func probe(log *slog.Logger) error {
	if err := band.InstallHeadlessBrowser(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "band-probe")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "state.json")
	if err := copyFile(statePath(), path); err != nil {
		return err
	}
	p := sessionProbe{
		path:  path,
		visit: func(path string) (bool, error) { return band.Visit(path, 30*time.Second) },
		now:   time.Now,
		sleep: time.Sleep,
		log:   log,
	}
	_, err = p.run()
	return err
}

type sessionProbe struct {
	path  string
	visit func(path string) (bool, error)
	now   func() time.Time
	sleep func(time.Duration)
	log   *slog.Logger
}

// run은 계획한 시각마다 세션을 열고, 어느 토큰이든 exp가 늘었으면 true를 돌려준다.
func (p sessionProbe) run() (bool, error) {
	prev, err := tokenExps(p.path)
	if err != nil {
		return false, err
	}
	exp := prev["band_session"]
	if exp.IsZero() {
		return false, errors.New("세션 파일에 band_session 토큰의 만료 시각이 없습니다")
	}
	plan := probePlan(exp, p.now())
	if len(plan) == 0 {
		p.log.Warn("남은 시험 시각이 없습니다", "토큰 만료", kst(exp))
		return false, nil
	}
	var names []string
	for _, t := range plan {
		names = append(names, kst(t))
	}
	p.log.Info("세션 시험 시작", "토큰 만료", kst(exp), "여는 시각", strings.Join(names, ", "))

	extended := false
	for len(plan) > 0 {
		for now := p.now(); now.Before(plan[0]); now = p.now() {
			p.sleep(min(plan[0].Sub(now), 20*time.Second))
		}
		for len(plan) > 0 && !p.now().Before(plan[0]) {
			plan = plan[1:]
		}
		alive, err := p.visit(p.path)
		if err != nil {
			p.log.Warn("밴드 홈을 열지 못했습니다", "오류", err)
			continue
		}
		cur, err := tokenExps(p.path)
		if err != nil {
			return extended, err
		}
		attrs := []any{"만료까지", exp.Sub(p.now()).Round(time.Minute).String(), "로그인", alive}
		grew := false
		for _, name := range probeTokens {
			attrs = append(attrs, name, kst(cur[name]))
			if !prev[name].IsZero() && cur[name].After(prev[name]) {
				grew = true
			}
			if !cur[name].IsZero() {
				prev[name] = cur[name]
			}
		}
		attrs = append(attrs, "연장", grew)
		p.log.Info("밴드 홈을 열었습니다", attrs...)
		extended = extended || grew
	}
	result := "exp가 늘지 않았다(재발급 없음)"
	if extended {
		result = "exp가 늘었다(재발급됨)"
	}
	p.log.Info("세션 시험 끝", "결과", result)
	return extended, nil
}

// probePlan은 exp 기준으로 밴드 홈을 열 시각 가운데 now보다 뒤인 것을 순서대로 돌려준다.
func probePlan(exp, now time.Time) []time.Time {
	var plan []time.Time
	for _, off := range probeOffsets {
		if t := exp.Add(-off); t.After(now) {
			plan = append(plan, t)
		}
	}
	return plan
}

func tokenExps(path string) (map[string]time.Time, error) {
	lifetimes, err := band.SessionLifetimes(path)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, l := range lifetimes {
		if !l.TokenExp.IsZero() {
			out[l.Name] = l.TokenExp
		}
	}
	return out, nil
}

func kst(t time.Time) string {
	if t.IsZero() {
		return "없음"
	}
	return t.In(book.Seoul).Format("2006-01-02 15:04:05")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
