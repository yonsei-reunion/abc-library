// Package cloudrun은 Cloud Run Admin API(v2)로 Job을 실행하고 최근 실행 기록을 읽는다.
// bandlogin이 텔레그램의 /batch와 /weekly를 받아 배치 Job을 실행할 때 쓴다.
package cloudrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Job은 한 리전의 Cloud Run Job 하나다.
type Job struct {
	Project, Region, Name string
	// Token은 접근 토큰을 준다. Cloud Run에서는 secretmanager.MetadataToken을 쓴다.
	Token func(ctx context.Context) (string, error)
	// BaseURL은 API 주소다. 비우면 https://run.googleapis.com을 쓴다. 테스트에서 바꾼다.
	BaseURL string
	HTTP    *http.Client
}

// Execution은 Job 실행 하나다.
type Execution struct {
	// Name은 실행 이름의 마지막 부분이다(예: abc-batch-dtskd).
	Name string
	// Args는 실행할 때 덮어쓴 인자다. 덮어쓰지 않았으면(이미지의 기본 CMD) 비어 있다.
	Args      []string
	Created   time.Time
	Completed time.Time
	Succeeded bool
}

// Running은 아직 끝나지 않은 실행인지 알려 준다.
func (e Execution) Running() bool { return e.Completed.IsZero() }

// Executions는 최근 실행을 최대 n개 돌려준다. API는 최근 것부터 준다.
func (j *Job) Executions(ctx context.Context, n int) ([]Execution, error) {
	var out struct {
		Executions []struct {
			Name           string    `json:"name"`
			CreateTime     time.Time `json:"createTime"`
			CompletionTime time.Time `json:"completionTime"`
			SucceededCount int       `json:"succeededCount"`
			Template       struct {
				Containers []struct {
					Args []string `json:"args"`
				} `json:"containers"`
			} `json:"template"`
		} `json:"executions"`
	}
	q := url.Values{"pageSize": {strconv.Itoa(n)}}
	if err := j.call(ctx, http.MethodGet, j.path()+"/executions?"+q.Encode(), nil, &out); err != nil {
		return nil, fmt.Errorf("Job %s의 실행 기록을 읽지 못했습니다: %w", j.Name, err)
	}
	list := make([]Execution, 0, len(out.Executions))
	for _, e := range out.Executions {
		x := Execution{Name: lastPart(e.Name), Created: e.CreateTime, Completed: e.CompletionTime, Succeeded: e.SucceededCount > 0}
		if len(e.Template.Containers) > 0 {
			x.Args = e.Template.Containers[0].Args
		}
		list = append(list, x)
	}
	return list, nil
}

// Run은 인자와 환경 변수를 덮어써서 Job을 실행하고 실행 이름을 돌려준다. 끝날 때까지 기다리지 않는다.
// 덮어쓴 환경 변수는 Job의 환경 변수에 더해진다.
func (j *Job) Run(ctx context.Context, args []string, env map[string]string) (string, error) {
	type envVar struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	override := map[string]any{"args": args}
	if len(env) > 0 {
		vars := make([]envVar, 0, len(env))
		for k, v := range env {
			vars = append(vars, envVar{k, v})
		}
		override["env"] = vars
	}
	body := map[string]any{"overrides": map[string]any{"containerOverrides": []any{override}}}
	// 응답은 오래 걸리는 작업(Operation)이고 metadata에 만들어진 실행이 있다.
	var op struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := j.call(ctx, http.MethodPost, j.path()+":run", body, &op); err != nil {
		return "", fmt.Errorf("Job %s을 실행하지 못했습니다: %w", j.Name, err)
	}
	return lastPart(op.Metadata.Name), nil
}

func (j *Job) path() string {
	base := j.BaseURL
	if base == "" {
		base = "https://run.googleapis.com"
	}
	return base + "/v2/projects/" + url.PathEscape(j.Project) + "/locations/" + url.PathEscape(j.Region) + "/jobs/" + url.PathEscape(j.Name)
}

func lastPart(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

func (j *Job) call(ctx context.Context, method, u string, in, out any) error {
	if j.Token == nil {
		return errors.New("접근 토큰을 줄 함수가 없습니다")
	}
	token, err := j.Token(ctx)
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
	hc := j.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
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
	return json.NewDecoder(resp.Body).Decode(out)
}
