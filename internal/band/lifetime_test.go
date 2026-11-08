package band

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeJWT(exp int64) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp))) + ".sig"
}

func TestSessionLifetimes(t *testing.T) {
	exp := time.Date(2026, 11, 4, 7, 26, 27, 0, time.UTC).Unix()
	state := fmt.Sprintf(`{"cookies":[
		{"name":"band_session","value":%q,"expires":-1},
		{"name":"rt","value":%q,"expires":1900000000},
		{"name":"SESSION","value":"opaque","expires":-1},
		{"name":"other","value":"x","expires":-1}]}`, fakeJWT(exp), fakeJWT(exp))
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SessionLifetimes(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("got %v, err %v", got, err)
	}
	if !got[0].CookieExpires.IsZero() || got[0].TokenExp.Unix() != exp {
		t.Errorf("band_session: %+v", got[0])
	}
	if got[1].CookieExpires.Unix() != 1900000000 {
		t.Errorf("rt: %+v", got[1])
	}
	if !got[2].TokenExp.IsZero() {
		t.Errorf("JWT가 아닌 값은 토큰 만료가 없어야 한다: %+v", got[2])
	}
	d := got[0].Describe(time.Unix(exp, 0).Add(-48 * time.Hour))
	if !strings.Contains(d, "세션 쿠키") || !strings.Contains(d, "48h0m0s 뒤") || strings.Contains(d, "opaque") {
		t.Errorf("Describe: %s", d)
	}
}

func TestSessionLifetimesFailures(t *testing.T) {
	if _, err := SessionLifetimes(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("없는 파일은 오류여야 한다")
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(path, []byte("{"), 0o600)
	if _, err := SessionLifetimes(path); err == nil {
		t.Error("깨진 JSON은 오류여야 한다")
	}
	if !jwtExp("a.!!!.c").IsZero() || !jwtExp("abc").IsZero() {
		t.Error("잘못된 토큰은 0이어야 한다")
	}
}

func TestSessionAlive(t *testing.T) {
	cases := map[string]bool{
		"https://band.us/home":                          true,
		"https://band.us/band/12345":                    true,
		"https://www.band.us/about/kr/intro":            false,
		"https://auth.band.us/login_page?next_url=home": false,
		"https://band.us/login?next=%2Fhome":            false,
	}
	for u, want := range cases {
		if got := SessionAlive(u); got != want {
			t.Errorf("%s: %v, want %v", u, got, want)
		}
	}
}
