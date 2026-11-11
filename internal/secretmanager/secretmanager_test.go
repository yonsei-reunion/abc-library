package secretmanager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeAPI struct {
	mu       sync.Mutex
	calls    []string
	added    []byte
	addFail  bool
	disabled []string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	base := "/v1/projects/p1/secrets/band-state"
	switch {
	case r.URL.Path == base+":addVersion":
		if f.addFail {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"권한 없음"}}`))
			return
		}
		var in struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.added, _ = base64.StdEncoding.DecodeString(in.Payload.Data)
		_, _ = w.Write([]byte(`{"name":"projects/123/secrets/band-state/versions/9"}`))
	case r.URL.Path == base+"/versions":
		_, _ = w.Write([]byte(`{"versions":[{"name":"projects/123/secrets/band-state/versions/9"},{"name":"projects/123/secrets/band-state/versions/8"}]}`))
	case strings.HasSuffix(r.URL.Path, ":disable"):
		f.disabled = append(f.disabled, r.URL.Path)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func client(url string) *Client {
	return &Client{Project: "p1", BaseURL: url, Token: func(context.Context) (string, error) { return "tok", nil }}
}

func TestAddAndDisable(t *testing.T) {
	api := &fakeAPI{}
	ts := httptest.NewServer(api)
	defer ts.Close()
	c := client(ts.URL)
	v, err := c.AddVersion(context.Background(), "band-state", []byte(`{"cookies":[]}`))
	if err != nil || v != "9" || string(api.added) != `{"cookies":[]}` {
		t.Fatalf("버전 %q, 오류 %v, 올린 값 %q", v, err, api.added)
	}
	off, err := c.DisableOthers(context.Background(), "band-state", v)
	if err != nil || len(off) != 1 || off[0] != "8" {
		t.Fatalf("끈 버전 %v, 오류 %v", off, err)
	}
	if len(api.disabled) != 1 || !strings.HasSuffix(api.disabled[0], "/versions/8:disable") {
		t.Errorf("끈 경로 %v", api.disabled)
	}
	if !strings.Contains(api.calls[1], "filter=state%3AENABLED") {
		t.Errorf("목록 조회 %v", api.calls)
	}
}

func TestAddFailureDoesNotLeakData(t *testing.T) {
	api := &fakeAPI{addFail: true}
	ts := httptest.NewServer(api)
	defer ts.Close()
	_, err := client(ts.URL).AddVersion(context.Background(), "band-state", []byte("SESSION-VALUE"))
	if err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "SESSION-VALUE") {
		t.Errorf("오류 %v", err)
	}
}

func TestTokenFailure(t *testing.T) {
	c := &Client{Project: "p1", BaseURL: "http://127.0.0.1:1", Token: func(context.Context) (string, error) {
		return "", errors.New("토큰 없음")
	}}
	if _, err := c.AddVersion(context.Background(), "band-state", []byte("x")); err == nil {
		t.Error("토큰이 없으면 실패해야 한다")
	}
}
