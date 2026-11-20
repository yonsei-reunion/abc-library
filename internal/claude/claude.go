// Package claude는 Claude Messages API를 부르는 공통 흐름과, Claude가 책의 판본을 고를 때 쓰는 교보문고 검색 도구(search_kyobo)를 둔다.
// 게시글 추출(extract)과 대화방 주간 요약(weekly)이 함께 쓴다.
// 응답은 structured outputs로 받고, Claude가 고른 판본은 상품 링크나 검색 결과에 실제로 있었던 것만 받아들인다(Editions).
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

const (
	model     = "claude-opus-5-5"
	maxTokens = 16000
	// maxCandidates는 search_kyobo가 한 번에 돌려주는 판본 수의 상한이다.
	maxCandidates = 8
	searchTool    = "search_kyobo"
)

// ErrRefused는 Claude가 요청 처리를 거절했다는 뜻이다(stop_reason이 refusal).
var ErrRefused = errors.New("claude가 처리를 거절했습니다")

// MessageAPI는 Claude Messages API다. *anthropic.BetaMessageService가 구현한다.
type MessageAPI interface {
	New(ctx context.Context, params anthropic.BetaMessageNewParams, opts ...option.RequestOption) (*anthropic.BetaMessage, error)
}

// Searcher는 교보문고 검색이다. *kyobo.Client가 구현한다.
type Searcher interface {
	Search(ctx context.Context, query string) ([]kyobo.Edition, error)
	Product(ctx context.Context, pid string) (kyobo.Edition, bool, error)
}

// Request는 search_kyobo 도구를 쓰면서 JSON 스키마에 맞는 답을 받는 요청이다.
type Request struct {
	System string
	// Schema는 답의 JSON 스키마다.
	Schema map[string]any
	// Content는 첫 사용자 메시지다.
	Content []anthropic.BetaContentBlockParamUnion
	// MaxTurns는 도구 호출을 포함해 Claude를 부르는 횟수의 상한이다.
	MaxTurns int
}

// Ask는 Claude가 답을 마칠 때까지 search_kyobo 호출을 처리하고, 답의 JSON 텍스트를 돌려준다.
// 검색 결과에 나온 판본은 eds에 모은다. Claude가 거절하면 ErrRefused를 돌려준다.
func Ask(ctx context.Context, api MessageAPI, eds *Editions, req Request) (string, error) {
	messages := []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(req.Content...)}
	for range req.MaxTurns {
		resp, err := api.New(ctx, params(req, messages))
		if err != nil {
			return "", err
		}
		switch resp.StopReason {
		case anthropic.BetaStopReasonRefusal:
			return "", ErrRefused
		case anthropic.BetaStopReasonToolUse:
			messages = append(messages, resp.ToParam())
			var results []anthropic.BetaContentBlockParamUnion
			for _, block := range resp.Content {
				if use, ok := block.AsAny().(anthropic.BetaToolUseBlock); ok {
					results = append(results, eds.runSearch(ctx, use))
				}
			}
			messages = append(messages, anthropic.NewBetaUserMessage(results...))
		case anthropic.BetaStopReasonEndTurn:
			return responseText(resp), nil
		default:
			return "", fmt.Errorf("예상하지 못한 stop_reason: %s", resp.StopReason)
		}
	}
	return "", fmt.Errorf("도구 호출이 %d번을 넘었습니다", req.MaxTurns)
}

var searchToolDescription = fmt.Sprintf(`교보문고 통합 검색으로 책의 판본을 찾는다. 검색 결과의 판본마다 isbn13, title, authors(저자만), publisher, pub_date, category(국내도서, 외국도서 등)를 돌려준다. 최대 %d개까지 돌려준다.`, maxCandidates)

func params(req Request, messages []anthropic.BetaMessageParam) anthropic.BetaMessageNewParams {
	return anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:  messages,
		Tools: []anthropic.BetaToolUnionParam{{OfTool: &anthropic.BetaToolParam{
			Name:        searchTool,
			Description: anthropic.String(searchToolDescription),
			Strict:      anthropic.Bool(true),
			InputSchema: anthropic.BetaToolInputSchemaParam{
				Properties: map[string]any{
					"query": map[string]any{"type": "string", "description": "검색어. 제목과 저자"},
				},
				Required:    []string{"query"},
				ExtraFields: map[string]any{"additionalProperties": false},
			},
		}}},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: req.Schema},
		},
		// 안전 분류기가 오탐으로 거절하면 서버가 다른 모델로 다시 처리한다.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	}
}

func responseText(resp *anthropic.BetaMessage) string {
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

type searchInput struct {
	Query string `json:"query"`
}

// runSearch는 search_kyobo 호출 하나를 처리하고, 결과에 나온 판본을 받아들일 수 있는 판본으로 기억한다.
func (e *Editions) runSearch(ctx context.Context, use anthropic.BetaToolUseBlock) anthropic.BetaContentBlockParamUnion {
	var in searchInput
	if use.Name != searchTool || json.Unmarshal([]byte(use.JSON.Input.Raw()), &in) != nil || strings.TrimSpace(in.Query) == "" {
		return anthropic.NewBetaToolResultBlock(use.ID, "알 수 없는 도구 호출입니다. search_kyobo에 query를 넣어 호출하세요.", true)
	}
	found, err := e.search.Search(ctx, in.Query)
	if err != nil {
		return anthropic.NewBetaToolResultBlock(use.ID, "검색에 실패했습니다: "+err.Error(), true)
	}
	if len(found) > maxCandidates {
		found = found[:maxCandidates]
	}
	for _, ed := range found {
		if _, ok := e.byISBN[ed.ISBN13]; !ok {
			e.byISBN[ed.ISBN13] = ed
		}
	}
	out, _ := json.Marshal(found)
	return anthropic.NewBetaToolResultBlock(use.ID, string(out), false)
}
