// Package gcp는 Google Cloud REST API를 부르는 공통 부분이다. cloudrun과 secretmanager가 쓴다.
// 요청과 응답 본문은 오류에 담지 않는다. 비밀 값이 들어 있을 수 있기 때문이다.
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TokenFunc는 API에 보낼 접근 토큰을 준다. Cloud Run에서는 MetadataToken을 쓴다.
type TokenFunc func(ctx context.Context) (string, error)

// Call은 REST API를 부른다. in이 nil이 아니면 JSON 본문으로 보내고, out이 nil이 아니면 응답 JSON을 읽어 넣는다.
// hc가 nil이면 30초 제한의 클라이언트를 쓴다. 200이 아니면 상태 코드와 API의 오류 메시지만 담은 오류를 돌려준다.
func Call(ctx context.Context, hc *http.Client, token TokenFunc, method, url string, in, out any) error {
	if token == nil {
		return errors.New("접근 토큰을 줄 함수가 없습니다")
	}
	tok, err := token(ctx)
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
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client(hc).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
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

// LastSegment는 리소스 이름(projects/p/locations/r/jobs/j/executions/e)의 마지막 부분이다.
func LastSegment(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

func client(hc *http.Client) *http.Client {
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
	resp, err := client(nil).Do(req)
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
