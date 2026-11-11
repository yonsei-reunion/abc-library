// Package secretmanager는 Secret Manager REST API로 비밀의 새 버전을 올리고 이전 버전을 사용 중지한다.
// Cloud Run에서는 메타데이터 서버가 준 서비스 계정 토큰을 쓴다. 비밀 값은 오류와 로그에 담지 않는다.
package secretmanager

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client는 한 프로젝트의 비밀을 다룬다.
type Client struct {
	Project string
	// Token은 접근 토큰을 준다. 비우면 메타데이터 서버에서 받는다.
	Token func(ctx context.Context) (string, error)
	// BaseURL은 API 주소다. 비우면 https://secretmanager.googleapis.com을 쓴다. 테스트에서 바꾼다.
	BaseURL string
	HTTP    *http.Client
}

// AddVersion은 secret에 data를 새 버전으로 올리고 버전 번호를 돌려준다.
func (c *Client) AddVersion(ctx context.Context, secret string, data []byte) (string, error) {
	crc := crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	body := map[string]any{"payload": map[string]any{
		"data":       base64.StdEncoding.EncodeToString(data),
		"dataCrc32c": strconv.FormatUint(uint64(crc), 10),
	}}
	var out struct {
		Name string `json:"name"`
	}
	if err := c.call(ctx, http.MethodPost, c.secretPath(secret)+":addVersion", body, &out); err != nil {
		return "", fmt.Errorf("비밀 %s에 새 버전을 올리지 못했습니다: %w", secret, err)
	}
	return versionOf(out.Name), nil
}

// DisableOthers는 keep을 뺀 사용 중인 버전을 모두 사용 중지하고, 끈 버전 번호를 돌려준다.
func (c *Client) DisableOthers(ctx context.Context, secret, keep string) ([]string, error) {
	var list struct {
		Versions []struct {
			Name string `json:"name"`
		} `json:"versions"`
	}
	q := url.Values{"filter": {"state:ENABLED"}}
	if err := c.call(ctx, http.MethodGet, c.secretPath(secret)+"/versions?"+q.Encode(), nil, &list); err != nil {
		return nil, fmt.Errorf("비밀 %s의 버전 목록을 읽지 못했습니다: %w", secret, err)
	}
	var disabled []string
	for _, v := range list.Versions {
		n := versionOf(v.Name)
		if n == keep {
			continue
		}
		if err := c.call(ctx, http.MethodPost, c.secretPath(secret)+"/versions/"+n+":disable", map[string]any{}, nil); err != nil {
			return disabled, fmt.Errorf("비밀 %s의 버전 %s을 사용 중지하지 못했습니다: %w", secret, n, err)
		}
		disabled = append(disabled, n)
	}
	return disabled, nil
}

func (c *Client) secretPath(secret string) string {
	base := c.BaseURL
	if base == "" {
		base = "https://secretmanager.googleapis.com"
	}
	return base + "/v1/projects/" + url.PathEscape(c.Project) + "/secrets/" + url.PathEscape(secret)
}

func versionOf(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

func (c *Client) call(ctx context.Context, method, u string, in, out any) error {
	tokenFn := c.Token
	if tokenFn == nil {
		tokenFn = MetadataToken
	}
	token, err := tokenFn(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(c.HTTP).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// 오류 응답에는 비밀 값이 없다. 상태와 메시지만 남긴다.
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, e.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func httpClient(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// metadataTokenURL은 Cloud Run 메타데이터 서버에서 서비스 계정 접근 토큰을 받는 주소다.
const metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"

// MetadataToken은 메타데이터 서버에서 서비스 계정의 접근 토큰을 받는다. Cloud Run에서만 된다.
func MetadataToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := httpClient(nil).Do(req)
	if err != nil {
		return "", fmt.Errorf("메타데이터 서버에서 토큰을 받지 못했습니다: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("메타데이터 서버에서 토큰을 받지 못했습니다: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", errors.New("메타데이터 서버의 토큰 응답을 읽지 못했습니다")
	}
	return out.AccessToken, nil
}
