package cloudrun

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const jobPath = "/v2/projects/p1/locations/asia-northeast3/jobs/abc-batch"

func newJob(url string) *Job {
	return &Job{
		Project: "p1", Region: "asia-northeast3", Name: "abc-batch", BaseURL: url,
		Token: func(context.Context) (string, error) { return "tok", nil },
	}
}

func TestExecutions(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != jobPath+"/executions" || r.URL.Query().Get("pageSize") != "5" {
			t.Errorf("요청 %s %s %s", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"executions":[
			{"name":"projects/p1/locations/asia-northeast3/jobs/abc-batch/executions/abc-batch-new","createTime":"2026-11-20T01:00:00Z","template":{"containers":[{"args":["weekly"]}]}},
			{"name":"projects/p1/locations/asia-northeast3/jobs/abc-batch/executions/abc-batch-ok","createTime":"2026-11-19T18:00:04Z","completionTime":"2026-11-19T18:01:00Z","succeededCount":1,"template":{"containers":[{}]}},
			{"name":"projects/p1/locations/asia-northeast3/jobs/abc-batch/executions/abc-batch-fail","createTime":"2026-11-18T18:00:04Z","completionTime":"2026-11-18T18:01:00Z","failedCount":1,"template":{}}
		]}`))
	}))
	defer ts.Close()
	list, err := newJob(ts.URL).Executions(context.Background(), 5)
	if err != nil || len(list) != 3 {
		t.Fatalf("목록 %v, 오류 %v", list, err)
	}
	if e := list[0]; e.Name != "abc-batch-new" || !e.Running() || e.Succeeded || len(e.Args) != 1 || e.Args[0] != "weekly" {
		t.Errorf("실행 중인 실행 %+v", e)
	}
	if e := list[1]; e.Name != "abc-batch-ok" || e.Running() || !e.Succeeded || e.Args != nil || !e.Created.Equal(time.Date(2026, 11, 19, 18, 0, 4, 0, time.UTC)) {
		t.Errorf("성공한 실행 %+v", e)
	}
	if e := list[2]; e.Running() || e.Succeeded {
		t.Errorf("실패한 실행 %+v", e)
	}
}

func TestRun(t *testing.T) {
	var got struct {
		Overrides struct {
			ContainerOverrides []struct {
				Args []string `json:"args"`
				Env  []struct {
					Name, Value string
				} `json:"env"`
			} `json:"containerOverrides"`
		} `json:"overrides"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != jobPath+":run" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("요청 %s %s", r.Method, r.URL)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"name":"projects/p1/locations/asia-northeast3/operations/op1",
			"metadata":{"@type":"type.googleapis.com/google.cloud.run.v2.Execution","name":"projects/p1/locations/asia-northeast3/jobs/abc-batch/executions/abc-batch-x1"}}`))
	}))
	defer ts.Close()
	name, err := newJob(ts.URL).Run(context.Background(), []string{"run"}, map[string]string{"TELEGRAM_REPORT": "1"})
	if err != nil || name != "abc-batch-x1" {
		t.Fatalf("실행 %q, 오류 %v", name, err)
	}
	o := got.Overrides.ContainerOverrides
	if len(o) != 1 || len(o[0].Args) != 1 || o[0].Args[0] != "run" || len(o[0].Env) != 1 || o[0].Env[0].Name != "TELEGRAM_REPORT" || o[0].Env[0].Value != "1" {
		t.Errorf("덮어쓰기 %+v", got)
	}
}

func TestErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Permission 'run.jobs.runWithOverrides' denied"}}`))
	}))
	defer ts.Close()
	_, err := newJob(ts.URL).Run(context.Background(), []string{"run"}, nil)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "runWithOverrides") {
		t.Errorf("권한 오류: %v", err)
	}
	j := newJob(ts.URL)
	j.Token = func(context.Context) (string, error) { return "", errors.New("토큰 없음") }
	if _, err := j.Executions(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "토큰 없음") {
		t.Errorf("토큰 오류: %v", err)
	}
	j.Token = nil
	if _, err := j.Executions(context.Background(), 5); err == nil {
		t.Error("토큰 함수가 없으면 오류여야 한다")
	}
}
