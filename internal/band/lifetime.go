package band

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Visit은 statePath의 세션으로 밴드 홈을 헤드리스로 열고, 화면의 스크립트가 wait 동안 돌게 둔 뒤
// 브라우저가 가진 세션으로 statePath를 덮어쓴다. 밴드가 토큰을 만료 전에 다시 발급하는지 시험할 때 쓴다.
// 파일을 덮어쓰므로 운영 세션의 복사본을 넘긴다. 세션이 끝나서 로그인 화면이나 소개 페이지로 옮겨 가면 false다.
func Visit(statePath string, wait time.Duration) (bool, error) {
	pw, err := playwright.Run()
	if err != nil {
		return false, err
	}
	defer func() { _ = pw.Stop() }()
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true)})
	if err != nil {
		return false, err
	}
	defer func() { _ = browser.Close() }()
	bctx, err := newContext(browser, statePath)
	if err != nil {
		return false, err
	}
	page, err := bctx.NewPage()
	if err != nil {
		return false, err
	}
	if _, err := page.Goto("https://band.us/home", playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded}); err != nil {
		return false, err
	}
	// 기다릴 요소가 따로 없다. 브라우저는 별도 프로세스라서 그동안에도 화면의 스크립트가 돈다.
	time.Sleep(wait)
	alive := SessionAlive(page.URL())
	tmp := statePath + ".tmp"
	if _, err := bctx.StorageState(playwright.BrowserContextStorageStateOptions{Path: playwright.String(tmp)}); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return false, err
	}
	return alive, os.Rename(tmp, statePath)
}

// SessionAlive는 밴드 홈을 연 뒤의 주소로 세션이 살아 있는지 판단한다.
// 세션이 끝났으면 홈은 소개 페이지(www.band.us/about/…)로, 다른 화면은 로그인 화면으로 옮겨 간다.
func SessionAlive(pageURL string) bool {
	return !IsLoginURL(pageURL) && !strings.Contains(pageURL, "/about/")
}

// sessionCookies는 로그인 상태를 정하는 쿠키 이름이다.
var sessionCookies = []string{"band_session", "rt", "SESSION"}

// SessionLifetime은 세션 쿠키 하나의 만료 정보다. 쿠키 값은 담지 않는다.
type SessionLifetime struct {
	Name string
	// CookieExpires는 쿠키 자체의 만료 시각이다. 브라우저를 닫으면 사라지는 세션 쿠키면 0이다.
	CookieExpires time.Time
	// TokenExp는 값이 JWT일 때 payload의 exp다. JWT가 아니거나 exp가 없으면 0이다.
	TokenExp time.Time
}

// SessionLifetimes는 statePath의 세션 쿠키가 언제 끝나는지 읽는다. '로그인 유지'가 먹혔는지 확인하는 데 쓴다.
func SessionLifetimes(statePath string) ([]SessionLifetime, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	return StateLifetimes(data)
}

// StateLifetimes는 storageState JSON에서 세션 쿠키가 언제 끝나는지 읽는다.
func StateLifetimes(data []byte) ([]SessionLifetime, error) {
	var state struct {
		Cookies []struct {
			Name    string  `json:"name"`
			Value   string  `json:"value"`
			Expires float64 `json:"expires"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	var out []SessionLifetime
	for _, name := range sessionCookies {
		for _, c := range state.Cookies {
			if c.Name != name {
				continue
			}
			l := SessionLifetime{Name: name, TokenExp: jwtExp(c.Value)}
			if c.Expires > 0 {
				l.CookieExpires = time.Unix(int64(c.Expires), 0)
			}
			out = append(out, l)
		}
	}
	return out, nil
}

func jwtExp(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// Describe는 한 줄 요약이다. now는 남은 시간을 계산하는 기준이다.
func (l SessionLifetime) Describe(now time.Time) string {
	cookie := "세션 쿠키(브라우저를 닫으면 끝)"
	if !l.CookieExpires.IsZero() {
		cookie = "쿠키 만료 " + l.CookieExpires.Format("2006-01-02 15:04")
	}
	token := "토큰 만료 정보 없음"
	if !l.TokenExp.IsZero() {
		token = fmt.Sprintf("토큰 만료 %s(%s 뒤)", l.TokenExp.Format("2006-01-02 15:04"), l.TokenExp.Sub(now).Round(time.Minute))
	}
	return fmt.Sprintf("%s: %s, %s", l.Name, cookie, token)
}
