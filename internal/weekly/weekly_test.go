package weekly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/kakao"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

type fakeAPI struct {
	responses []string
	calls     []anthropic.BetaMessageNewParams
}

func (f *fakeAPI) New(_ context.Context, p anthropic.BetaMessageNewParams, _ ...option.RequestOption) (*anthropic.BetaMessage, error) {
	f.calls = append(f.calls, p)
	if len(f.calls) > len(f.responses) {
		return nil, errors.New("응답이 더 없다")
	}
	var m anthropic.BetaMessage
	if err := json.Unmarshal([]byte(f.responses[len(f.calls)-1]), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

var (
	storyEdition = kyobo.Edition{PID: "S000001981838", Barcode: "9791196914806", ISBN13: "9791196914806",
		Title: "당신 인생의 이야기", Authors: []string{"테드 창"}, Publisher: "엘리", Category: "국내도서"}
	breathEdition = kyobo.Edition{PID: "S000000555555", Barcode: "9791191114225", ISBN13: "9791191114225",
		Title: "숨", Authors: []string{"테드 창"}, Publisher: "엘리", Category: "국내도서"}
)

type fakeSearch struct{ queries []string }

func (f *fakeSearch) Search(_ context.Context, q string) ([]kyobo.Edition, error) {
	f.queries = append(f.queries, q)
	return []kyobo.Edition{storyEdition}, nil
}

func (f *fakeSearch) Product(_ context.Context, pid string) (kyobo.Edition, bool, error) {
	f.queries = append(f.queries, "pid:"+pid)
	return breathEdition, pid == breathEdition.PID, nil
}

type fakeTitles map[string]string

func (f fakeTitles) Title(_ context.Context, u string) string { return f[u] }

type fakeBooks struct{ asked []book.Book }

func (f *fakeBooks) FindBook(_ context.Context, b book.Book) (int64, bool, error) {
	f.asked = append(f.asked, b)
	return 7, b.ISBN13 == storyEdition.ISBN13, nil
}

func message(stop string, content ...string) string {
	return `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"` + stop +
		`","content":[` + strings.Join(content, ",") + `],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func textBlock(v any) string {
	b, _ := json.Marshal(v)
	s, _ := json.Marshal(string(b))
	return `{"type":"text","text":` + string(s) + `}`
}

const toolUse = `{"type":"tool_use","id":"t1","name":"search_kyobo","input":{"query":"당신 인생의 이야기 테드 창"}}`

type m = map[string]any

func at(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", s, book.Seoul)
	return t
}

func week() Input {
	return Input{Room: "ABC(아카라카북클럽)", Start: "2026-10-19", End: "2026-10-25", Messages: []kakao.Message{
		{Date: "2026-10-19", Time: "21:03", Sender: "홍길동", Text: "《당신 인생의 이야기》 추천해요 https://m.yes24.com/goods/detail/1234.",
			CollectedAt: at("2026-10-19 21:05")},
		{Date: "2026-10-20", Sender: "김철수", Text: "서평 남겼어요 https://blog.example.com/review\n그리고 https://product.kyobobook.co.kr/detail/S000000555555",
			CollectedAt: at("2026-10-20 08:00")},
		{Sender: "이영희", Text: "모임 장소 기사입니다 (https://news.example.com/a)", CollectedAt: at("2026-10-21 12:00")},
		{Date: "2026-10-22", Time: "10:00", Sender: "홍길동", Text: "[사진 속 글자] 숨 테드 창", CollectedAt: at("2026-10-22 10:01")},
	}}
}

func TestSummarize(t *testing.T) {
	api := &fakeAPI{responses: []string{
		message("tool_use", toolUse),
		message("end_turn", textBlock(m{
			"books": []m{
				{"title": "당신 인생의 이야기", "authors": []string{"테드 창"}, "isbn13": "9791196914806", "kyobo_pid": "",
					"context": []string{"SF 단편집으로 추천했다.", " "}, "links": []int{1, 2, 99, 0}},
				// 상품 링크의 책은 상품 번호로 고른다.
				{"title": "숨", "authors": []string{}, "isbn13": "", "kyobo_pid": "S000000555555",
					"context": []string{"사진으로 공유했다."}, "links": []int{3, 2}},
				// 같은 작품은 한 번만 넣는다.
				{"title": "당신 인생의 이야기", "authors": []string{"테드 창"}, "isbn13": "", "kyobo_pid": "",
					"context": []string{"중복"}, "links": []int{}},
				// 검색 결과에 없던 ISBN은 버리고 교보문고 검색 주소를 단다.
				{"title": "지어낸 책", "authors": []string{}, "isbn13": "9780000000000", "kyobo_pid": "S999",
					"context": []string{}, "links": []int{}},
			},
			"links":    []m{{"link": 4, "title": "모임 장소 기사", "context": "모임 장소를 안내했다."}, {"link": 1, "title": "무시", "context": ""}},
			"keywords": []string{"SF", "테드 창", "SF", "단편", "서평", "모임", "사진", "기사", "추천", "독서", "소설", "열둘"},
		})),
	}}
	search := &fakeSearch{}
	books := &fakeBooks{}
	titles := fakeTitles{"https://blog.example.com/review": "내 서평", "https://news.example.com/a": "기사 제목"}
	sum, err := New(api, search, titles, books, "https://abc-lib.web.app/").Summarize(context.Background(), week())
	if err != nil {
		t.Fatal(err)
	}

	// Claude에게 작성자 이름을 보내지 않는다.
	sent, _ := json.Marshal(api.calls[0].Messages)
	for _, name := range []string{"홍길동", "김철수", "이영희"} {
		if strings.Contains(string(sent), name) {
			t.Errorf("요청에 작성자 %s가 들어갔다", name)
		}
	}
	for _, want := range []string{
		"[1] 10-19(월) 21:03 《당신 인생의 이야기》", "[2] 10-20(화) ~08:00 서평 남겼어요", "\\n    그리고",
		"[L1] https://m.yes24.com/goods/detail/1234\\n", "[L2] https://blog.example.com/review - 내 서평",
		"[L3] https://product.kyobobook.co.kr/detail/S000000555555 → 교보문고 상품: 상품 번호 S000000555555",
		"[L4] https://news.example.com/a - 기사 제목",
	} {
		if !strings.Contains(string(sent), want) {
			t.Errorf("요청에 %q가 없다", want)
		}
	}
	if len(api.calls) != 2 || strings.Join(search.queries, "|") != "pid:S000000555555|당신 인생의 이야기 테드 창" {
		t.Errorf("calls=%d queries=%v", len(api.calls), search.queries)
	}

	text := sum.Text()
	for _, want := range []string{
		"ABC 대화방 주간 요약\n기간: 2026-10-19(월) ~ 2026-10-25(일)\n기준: 수집된 ABC(아카라카북클럽) 메시지 4건\n",
		"■ 《당신 인생의 이야기》 - 테드 창\n- SF 단편집으로 추천했다.\n- 관련 링크: https://blog.example.com/review (내 서평)\n" +
			"- 구매 링크: https://product.kyobobook.co.kr/detail/S000001981838\n- ABC 도서관: https://abc-lib.web.app/books/7\n",
		"■ 《숨》 - 테드 창\n- 사진으로 공유했다.\n- 구매 링크: https://product.kyobobook.co.kr/detail/S000000555555\n\n",
		"■ 《지어낸 책》 - 저자 미확인\n- 구매 링크(교보문고 검색): https://search.kyobobook.co.kr/search?keyword=",
		"[공유된 URL 모음]\n\n■ 모임 장소 기사\n- 맥락: 모임 장소를 안내했다.\n- URL: https://news.example.com/a\n",
		"[핵심 키워드 10개]\n- SF\n- 테드 창\n- 단편\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("요약에 %q가 없다:\n%s", want, text)
		}
	}
	// 밴드는 #으로 시작하는 말을 해시태그로 바꾼다.
	for _, unwanted := range []string{"중복", "yes24", "열둘", "무시", "#"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("요약에 %q가 있다:\n%s", unwanted, text)
		}
	}
	if len(books.asked) != 3 {
		t.Errorf("사이트 책 조회 = %+v", books.asked)
	}
}

// breathSearch는 상품 링크의 숨과 ISBN이 다른 숨의 다른 판본을 검색 결과로 돌려준다.
type breathSearch struct{ fakeSearch }

var otherBreath = kyobo.Edition{PID: "S000000666666", Barcode: "9791191114999", ISBN13: "9791191114999", Title: "숨", Authors: []string{"테드 창"}}

func (b *breathSearch) Search(context.Context, string) ([]kyobo.Edition, error) {
	return []kyobo.Edition{otherBreath}, nil
}

func TestSummarizeKeepsLinkedEditionApart(t *testing.T) {
	api := &fakeAPI{responses: []string{
		message("tool_use", toolUse),
		message("end_turn", textBlock(m{
			"books": []m{
				{"title": "숨", "authors": []string{}, "isbn13": "", "kyobo_pid": "S000000555555", "context": []string{}, "links": []int{}},
				{"title": "숨", "authors": []string{"테드 창"}, "isbn13": otherBreath.ISBN13, "kyobo_pid": "", "context": []string{}, "links": []int{}},
				{"title": "숨", "authors": []string{"테드 창"}, "isbn13": "", "kyobo_pid": "", "context": []string{}, "links": []int{}},
			},
			"links": []m{}, "keywords": []string{},
		})),
	}}
	sum, err := New(api, &breathSearch{}, nil, nil, "").Summarize(context.Background(), week())
	if err != nil {
		t.Fatal(err)
	}
	// 상품 링크로 가리킨 판본은 작품 키가 같아도 ISBN이 다른 판본과 묶지 않는다. ISBN이 없는 같은 작품은 묶는다.
	if len(sum.Books) != 2 || sum.Books[0].Purchase != kyobo.ProductURL(breathEdition.PID) || sum.Books[1].Purchase != kyobo.ProductURL(otherBreath.PID) {
		t.Errorf("books = %+v", sum.Books)
	}
}

func TestSummarizeKeepsUnassignedLinks(t *testing.T) {
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(m{"books": []m{}, "links": []m{}, "keywords": []string{}}))}}
	sum, err := New(api, &fakeSearch{}, nil, nil, "").Summarize(context.Background(), week())
	if err != nil {
		t.Fatal(err)
	}
	// Claude가 링크를 빠뜨려도 코드가 모두 넣는다. 제목이 없으면 미확인으로 쓴다.
	if len(sum.Links) != 4 {
		t.Fatalf("links = %+v", sum.Links)
	}
	text := sum.Text()
	for _, want := range []string{"- 이번 주에 공유된 책이 없습니다.", "■ 제목 미확인\n- URL: https://m.yes24.com/goods/detail/1234\n", "[핵심 키워드 0개]"} {
		if !strings.Contains(text, want) {
			t.Errorf("요약에 %q가 없다:\n%s", want, text)
		}
	}
}

func TestSummarizeWithoutMessages(t *testing.T) {
	api := &fakeAPI{}
	sum, err := New(api, &fakeSearch{}, nil, nil, "").Summarize(context.Background(), Input{Room: "ABC", Start: "2026-10-19", End: "2026-10-25"})
	if err != nil || len(api.calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(api.calls))
	}
	if text := sum.Text(); !strings.Contains(text, "메시지 0건") || !strings.Contains(text, "수집된 메시지가 없습니다") {
		t.Errorf("요약 = %s", text)
	}
}

func TestSummarizeFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []string
		want      string
	}{
		{"거절", []string{message("refusal")}, ErrRefused.Error()},
		{"JSON 아님", []string{message("end_turn", `{"type":"text","text":"요약입니다"}`)}, "요약 결과를 읽지 못했습니다"},
		{"알 수 없는 stop_reason", []string{message("max_tokens")}, "max_tokens"},
		{"API 오류", nil, "응답이 더 없다"},
	} {
		_, err := New(&fakeAPI{responses: tc.responses}, &fakeSearch{}, nil, nil, "").Summarize(context.Background(), week())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}

	var loop []string
	for range maxTurns {
		loop = append(loop, message("tool_use", toolUse))
	}
	if _, err := New(&fakeAPI{responses: loop}, &fakeSearch{}, nil, nil, "").Summarize(context.Background(), week()); err == nil {
		t.Error("도구 호출이 끝나지 않는데 오류가 없다")
	}
}

func TestExtractURLs(t *testing.T) {
	got := ExtractURLs([]kakao.Message{
		{Text: "보세요 https://a.example.com/x. 그리고 (https://b.example.com/y)"},
		{Text: "https://ko.wikipedia.org/wiki/숨_(소설)! 또 https://a.example.com/x"},
		{Text: "《https://c.example.com/z》, http://d.example.com…"},
		{Text: "주소 아님 https:// 와 http://"},
	})
	want := []string{
		"https://a.example.com/x", "https://b.example.com/y", "https://ko.wikipedia.org/wiki/숨_(소설)",
		"https://c.example.com/z", "http://d.example.com",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %q", got)
	}
}

func TestParseTitle(t *testing.T) {
	for _, tc := range []struct{ page, want string }{
		{`<head><meta content="OG &amp; 제목" property="og:title"><title>무시</title></head>`, "OG & 제목"},
		{`<meta property='og:title' content='작은따옴표'>`, "작은따옴표"},
		{`<meta name="twitter:title" content="트위터">`, "트위터"},
		{`<meta property="og:title" content=""><title>
  긴   제목
</title>`, "긴 제목"},
		{`<p>제목 없음</p>`, ""},
		{`<title>` + strings.Repeat("가", 250) + `</title>`, strings.Repeat("가", 200) + "…"},
	} {
		if got := ParseTitle(tc.page); got != tc.want {
			t.Errorf("ParseTitle(%.40q) = %q, want %q", tc.page, got, tc.want)
		}
	}
}

func TestPageTitles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<title>페이지</title>`))
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte(`<title>아님</title>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := NewPageTitles()
	ctx := context.Background()
	if got := p.Title(ctx, srv.URL+"/html"); got != "페이지" {
		t.Errorf("html = %q", got)
	}
	for _, path := range []string{"/pdf", "/missing"} {
		if got := p.Title(ctx, srv.URL+path); got != "" {
			t.Errorf("%s = %q", path, got)
		}
	}
	if got := p.Title(ctx, "http://127.0.0.1:1/"); got != "" {
		t.Errorf("연결 실패 = %q", got)
	}
}

func TestFetchTitlesSkipsStoreLinks(t *testing.T) {
	titles := fakeTitles{"https://product.kyobobook.co.kr/detail/S1": "교보", "https://x.example.com": "엑스"}
	got := fetchTitles(context.Background(), titles, []string{"https://product.kyobobook.co.kr/detail/S1", "https://x.example.com"})
	if got[0].Title != "" || got[1].Title != "엑스" {
		t.Errorf("got %+v", got)
	}
}

func TestPeriod(t *testing.T) {
	for _, tc := range []struct {
		now        time.Time
		end        string
		start, got string
	}{
		{at("2026-10-26 07:00"), "", "2026-10-19", "2026-10-25"}, // 월요일 아침
		{at("2026-10-26 00:05"), "", "2026-10-19", "2026-10-25"},
		{at("2026-10-25 23:59"), "", "2026-10-12", "2026-10-18"},                         // 일요일이면 지난 일요일까지
		{at("2026-10-28 12:00"), "", "2026-10-19", "2026-10-25"},                         // 수요일
		{time.Date(2026, 10, 25, 22, 0, 0, 0, time.UTC), "", "2026-10-19", "2026-10-25"}, // UTC 일요일 22시 = 서울 월요일 7시
		{at("2026-10-26 07:00"), "2026-01-04", "2025-12-29", "2026-01-04"},               // 해를 넘는 기간
	} {
		start, end, err := Period(tc.now, tc.end)
		if err != nil || start != tc.start || end != tc.got {
			t.Errorf("Period(%s, %q) = %s, %s, %v", tc.now, tc.end, start, end, err)
		}
	}
	if _, _, err := Period(time.Now(), "2026/10/25"); err == nil {
		t.Error("잘못된 날짜인데 오류가 없다")
	}
}

// 밴드는 #으로 시작하는 말을 해시태그로 바꾸므로 글자 속 #은 전각으로 바꾸고 주소는 그대로 둔다.
func TestTextAvoidsHashtags(t *testing.T) {
	sum := Summary{
		Room: "ABC", Start: "2026-10-19", End: "2026-10-25", Count: 3,
		Books:    []Book{{Title: "C# 입문", Authors: []string{"저자"}, Context: []string{"#추천 도서였다."}, Purchase: "https://example.com/p#top"}},
		Links:    []SharedLink{{Link: Link{URL: "https://abc-lib.web.app/books?tag=%ea#x", Title: "ABC 도서관: #과학 태그"}, Context: "#과학 책 모음"}},
		Keywords: []string{"#교양과학"},
	}
	text := sum.Text()
	for _, want := range []string{"■ 《C＃ 입문》 - 저자", "- ＃추천 도서였다.", "https://example.com/p#top", "■ ABC 도서관: ＃과학 태그", "- 맥락: ＃과학 책 모음", "https://abc-lib.web.app/books?tag=%ea#x", "- ＃교양과학"} {
		if !strings.Contains(text, want) {
			t.Errorf("요약에 %q가 없다:\n%s", want, text)
		}
	}
}
