package band

import (
	"encoding/json"
	"errors"

	"github.com/mxschmitt/playwright-go"
)

// evalJSON은 JSON 문자열을 돌려주는 페이지 스크립트를 실행하고 그 결과를 T로 읽는다.
func evalJSON[T any](page playwright.Page, script string, arg ...any) (T, error) {
	var out T
	v, err := page.Evaluate(script, arg...)
	if err != nil {
		return out, err
	}
	raw, ok := v.(string)
	if !ok {
		return out, errors.New("스크립트가 JSON 문자열을 돌려주지 않았습니다")
	}
	err = json.Unmarshal([]byte(raw), &out)
	return out, err
}

// evalString은 문자열을 돌려주는 페이지 스크립트를 실행한다. 오류를 남기는 데 쓰므로, 실행하지 못하면 빈 문자열이다.
func evalString(page playwright.Page, script string, arg ...any) string {
	v, err := page.Evaluate(script, arg...)
	if err != nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
