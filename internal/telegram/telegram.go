// Package telegram은 Telegram Bot API로 메시지를 보내고 웹훅 업데이트를 읽는다.
// 봇 토큰이 요청 주소에 들어가므로 오류에 주소를 담지 않는다.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SecretHeader는 웹훅을 등록할 때 정한 비밀 토큰을 Telegram이 실어 보내는 헤더다.
const SecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// Client는 봇 하나로 메시지를 보낸다.
type Client struct {
	Token string
	// BaseURL은 Bot API 주소다. 비우면 https://api.telegram.org를 쓴다. 테스트에서 바꾼다.
	BaseURL string
	HTTP    *http.Client
}

// Send는 chatID 대화방에 글을 보낸다.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		return err
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+c.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return errors.New("텔레그램 요청을 만들지 못했습니다")
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// url.Error는 봇 토큰이 든 주소를 담고 있다.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("텔레그램 메시지를 보내지 못했습니다: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK {
		return fmt.Errorf("텔레그램 메시지를 보내지 못했습니다: HTTP %d %s", resp.StatusCode, out.Description)
	}
	return nil
}

// Update는 웹훅으로 받은 업데이트에서 쓰는 부분이다.
type Update struct {
	Message *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// Command는 메시지가 봇 명령이면 이름을 돌려준다. "/renew@abc_bot 아무거나"는 "renew"다.
func (u Update) Command() string {
	if u.Message == nil || !strings.HasPrefix(u.Message.Text, "/") {
		return ""
	}
	cmd, _, _ := strings.Cut(strings.Fields(u.Message.Text)[0][1:], "@")
	return cmd
}

// ChatID는 메시지가 온 대화방이다. 메시지가 아니면 0이다.
func (u Update) ChatID() int64 {
	if u.Message == nil {
		return 0
	}
	return u.Message.Chat.ID
}
