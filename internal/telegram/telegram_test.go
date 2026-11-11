package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSend(t *testing.T) {
	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTOKEN123/sendMessage" {
			t.Errorf("경로 %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c := &Client{Token: "TOKEN123", BaseURL: ts.URL}
	if err := c.Send(context.Background(), 42, "안녕"); err != nil {
		t.Fatal(err)
	}
	if got["chat_id"] != float64(42) || got["text"] != "안녕" {
		t.Errorf("본문 %v", got)
	}
}

func TestSendErrorsHideToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	c := &Client{Token: "SECRET-TOKEN", BaseURL: ts.URL}
	err := c.Send(context.Background(), 1, "x")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("오류 응답: %v", err)
	}
	ts.Close()
	// 서버가 없어서 연결이 실패하면 url.Error가 주소를 담는다.
	err = c.Send(context.Background(), 1, "x")
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("토큰이 오류에 들어갔다: %v", err)
	}
}

func TestCommand(t *testing.T) {
	for text, want := range map[string]string{
		"/renew": "renew", "/renew@abc_bot": "renew", "/renew 지금": "renew",
		"renew": "", "": "", "/": "", " /renew": "",
	} {
		var u Update
		_ = json.Unmarshal([]byte(`{"message":{"chat":{"id":5},"text":`+quote(text)+`}}`), &u)
		if got := u.Command(); got != want {
			t.Errorf("%q: %q, 기대 %q", text, got, want)
		}
		if u.ChatID() != 5 {
			t.Errorf("chat id %d", u.ChatID())
		}
	}
	var none Update
	if none.Command() != "" || none.ChatID() != 0 {
		t.Error("메시지가 없는 업데이트")
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
