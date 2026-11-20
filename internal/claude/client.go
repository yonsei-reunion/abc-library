package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const metadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=https://api.anthropic.com&format=full"

// NewClient는 실행 환경에 맞는 인증으로 Claude 클라이언트를 만든다.
// ANTHROPIC_FEDERATION_RULE_ID가 있으면 Cloud Run 메타데이터 서버의 ID 토큰으로 Workload Identity Federation을 쓰고,
// 없으면 CLAUDE_API_KEY를 쓴다. ANTHROPIC_API_KEY라는 이름을 피한 것은
// 같은 디렉터리에서 실행한 Claude Code 같은 도구가 그 키를 자동으로 가져다 쓰지 않게 하기 위해서다.
func NewClient() (anthropic.Client, error) {
	if rule := os.Getenv("ANTHROPIC_FEDERATION_RULE_ID"); rule != "" {
		return anthropic.NewClient(option.WithFederationTokenProvider(googleIdentityToken, option.FederationOptions{
			FederationRuleID: rule,
			OrganizationID:   os.Getenv("ANTHROPIC_ORGANIZATION_ID"),
			ServiceAccountID: os.Getenv("ANTHROPIC_SERVICE_ACCOUNT_ID"),
			WorkspaceID:      os.Getenv("ANTHROPIC_WORKSPACE_ID"),
		})), nil
	}
	key := os.Getenv("CLAUDE_API_KEY")
	if key == "" {
		return anthropic.Client{}, errors.New("CLAUDE_API_KEY 환경 변수가 없습니다")
	}
	return anthropic.NewClient(option.WithAPIKey(key)), nil
}

// googleIdentityToken은 Cloud Run에 연결된 서비스 계정의 ID 토큰을 메타데이터 서버에서 받는다.
func googleIdentityToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("메타데이터 서버 요청 실패: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("메타데이터 서버 응답 코드 %d", resp.StatusCode)
	}
	return strings.TrimSpace(string(body)), nil
}
