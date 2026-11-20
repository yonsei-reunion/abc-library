// Package secretmanager는 Secret Manager REST API로 비밀의 새 버전을 올리고 이전 버전을 사용 중지한다.
// Cloud Run에서는 메타데이터 서버가 준 서비스 계정 토큰을 쓴다. 비밀 값은 오류와 로그에 담지 않는다.
package secretmanager

import (
	"context"
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yonsei-alumni/abc-library/internal/gcp"
)

// Client는 한 프로젝트의 비밀을 다룬다.
type Client struct {
	Project string
	// Token은 접근 토큰을 준다. 비우면 메타데이터 서버에서 받는다.
	Token gcp.TokenFunc
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
	return gcp.LastSegment(out.Name), nil
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
		n := gcp.LastSegment(v.Name)
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

func (c *Client) call(ctx context.Context, method, u string, in, out any) error {
	token := c.Token
	if token == nil {
		token = gcp.MetadataToken
	}
	return gcp.Call(ctx, c.HTTP, token, method, u, in, out)
}
