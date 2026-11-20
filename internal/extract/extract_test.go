package extract

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

// testTags는 처음 DB에 넣은 태그 목록과 같다.
var testTags = func() book.TagList {
	tags := book.TagList{{Name: "AI", Topic: true}, {Name: "SF", Topic: true}, {Name: "과학", Topic: true}}
	for _, name := range []string{
		"소설", "추리·미스터리", "판타지", "시", "에세이",
		"인문", "철학", "역사", "사회·정치", "경제·경영", "자기계발",
		"기술·IT", "예술", "여행", "종교", "어린이·청소년", "만화",
	} {
		tags = append(tags, book.Tag{Name: name})
	}
	return tags
}()

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

type fakeSearch struct{ queries []string }

var storyEdition = kyobo.Edition{
	PID: "S000001981838", Barcode: "9791196914806", ISBN13: "9791196914806", Title: "당신 인생의 이야기",
	Authors: []string{"테드 창"}, Publisher: "엘리", Category: "국내도서",
}

func (f *fakeSearch) Search(_ context.Context, q string) ([]kyobo.Edition, error) {
	f.queries = append(f.queries, q)
	return []kyobo.Edition{storyEdition}, nil
}

func (f *fakeSearch) Product(_ context.Context, pid string) (kyobo.Edition, bool, error) {
	f.queries = append(f.queries, pid)
	return storyEdition, pid == storyEdition.PID, nil
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

type m = map[string]any

func found(title, isbn string, tags []string, postSummary string, postLinks []int, comments ...m) m {
	if comments == nil {
		comments = []m{}
	}
	if postLinks == nil {
		postLinks = []int{}
	}
	return m{"title": title, "authors": []string{"테드 창"}, "isbn13": isbn, "kyobo_pid": "", "tags": tags,
		"post": m{"summary": postSummary, "links": postLinks}, "comments": comments, "unrelated_links": []int{}}
}

var (
	postedAt    = time.Date(2026, 10, 21, 1, 24, 0, 0, time.UTC)
	commentedAt = time.Date(2026, 10, 21, 10, 57, 0, 0, time.UTC)
	input       = Input{
		PostedAt: postedAt,
		Text:     "#책_소개 테드 창 소설집을 읽었다.",
		Links: []book.Link{
			{URL: "https://product.kyobobook.co.kr/detail/S000001981838", Title: "당신 인생의 이야기 - 교보문고"},
			{URL: "https://news.example.com/ted-chiang", Title: ""},
			{URL: "https://blog.example.com/review", Title: "리뷰"},
			{URL: "https://bookshelf.example.com/%EC%88%A8/", Title: ""},
		},
		Images: []Image{{MediaType: "image/jpeg", Data: []byte("jpeg")}},
		Comments: []Comment{
			{At: commentedAt, Text: "같은 작가의 '숨'도 좋아요", Links: []book.Link{
				{URL: "https://www.youtube.com/watch?v=abc", Title: ""},
				{URL: "https://www.aladin.co.kr/shop/wproduct.aspx?ItemId=1", Title: "알라딘"},
			}},
			{At: commentedAt.Add(time.Hour), Text: "다음 모임 언제예요?"},
		},
	}
)

func TestExtractBuildsMentionsAndAttachesLinks(t *testing.T) {
	final := m{"books": []m{
		// 본문이 다룬 책은 한 권뿐이라, Claude가 고른 링크와 상관없이 본문 링크를 모두 붙인다(서점 링크는 뺀다).
		found("당신 인생의 이야기 (양장)", "9791196914806", []string{"SF", "SF", "없는 태그", "소설"}, "첫 줄\n둘째 줄", []int{2}),
		// 댓글에서만 소개된 책. 검색 결과에 없던 ISBN은 버린다. 잘못된 댓글 번호와 중복 번호는 뺀다.
		found("숨", "9788932917245", []string{"SF"}, "", nil,
			m{"comment": 1, "summary": "같은 작가의 다른 소설집으로 추천"}, m{"comment": 1, "summary": "중복"}, m{"comment": 9, "summary": "없는 댓글"}),
		// 같은 작품이 또 나오면 뺀다.
		found("당신 인생의 이야기", "", []string{}, "중복", nil),
		// 언급이 없는 책은 뺀다.
		found("언급 없음", "", []string{}, " ", nil),
	}}
	api := &fakeAPI{responses: []string{
		message("tool_use", `{"type":"tool_use","id":"tu_1","name":"search_kyobo","input":{"query":"당신 인생의 이야기 테드 창"}}`),
		message("end_turn", textBlock(final)),
	}}
	search := &fakeSearch{}

	got, err := New(api, search, testTags).Extract(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	// 본문의 교보문고 상품 링크를 상품 번호로 먼저 찾고, Claude가 제목으로 한 번 더 검색한다.
	if !slices.Equal(search.queries, []string{"S000001981838", "당신 인생의 이야기 테드 창"}) || len(got) != 2 {
		t.Fatalf("검색 %v, 찾은 책 %+v", search.queries, got)
	}

	story := got[0]
	if b := story.Book; b.ISBN13 != "9791196914806" || b.Title != "당신 인생의 이야기" || b.Publisher != "엘리" || b.KyoboPID != "S000001981838" || len(b.Authors) != 1 {
		t.Errorf("교보문고 판본 정보를 따라야 한다: %+v", b)
	}
	if strings.Join(story.Book.Tags, ",") != "SF,소설" {
		t.Errorf("태그 = %v", story.Book.Tags)
	}
	if len(story.Mentions) != 1 || story.Mentions[0].Kind != book.InPost || !story.Mentions[0].At.Equal(postedAt) {
		t.Fatalf("본문 언급 = %+v", story.Mentions)
	}
	if links := story.Mentions[0].Links; len(links) != 3 || links[0].Title != "news.example.com" || links[1].Title != "리뷰" ||
		links[2].URL != "https://bookshelf.example.com/%EC%88%A8/" {
		t.Errorf("서점 링크만 빼고 본문 링크를 원래 주소 그대로 모두 붙여야 한다: %+v", links)
	}

	breath := got[1]
	if breath.Book.ISBN13 != "" || breath.Book.Title != "숨" {
		t.Errorf("확인되지 않은 ISBN은 버려야 한다: %+v", breath.Book)
	}
	if len(breath.Mentions) != 1 || breath.Mentions[0].Kind != book.InComment || !breath.Mentions[0].At.Equal(commentedAt) {
		t.Fatalf("댓글 언급 = %+v", breath.Mentions)
	}
	if links := breath.Mentions[0].Links; len(links) != 1 || links[0].URL != "https://www.youtube.com/watch?v=abc" {
		t.Errorf("댓글의 링크를 서점 링크만 빼고 붙여야 한다: %+v", links)
	}

	if n := len(api.calls[1].Messages); n != 3 {
		t.Errorf("두 번째 요청에는 사용자, 도구 호출, 도구 결과가 있어야 한다: %d", n)
	}
}

// productSearch는 검색어마다 정해 둔 결과를 돌려준다. 없는 검색어는 빈 결과다.
type productSearch struct {
	results map[string][]kyobo.Edition
	queries []string
}

func (f *productSearch) Search(_ context.Context, q string) ([]kyobo.Edition, error) {
	f.queries = append(f.queries, q)
	return f.results[q], nil
}

// Product는 상품 번호를 검색어로 한 결과에서 그 상품을 찾는다. S9로 시작하는 상품 번호는 검색에 실패한다.
func (f *productSearch) Product(_ context.Context, pid string) (kyobo.Edition, bool, error) {
	f.queries = append(f.queries, pid)
	if strings.HasPrefix(pid, "S9") {
		return kyobo.Edition{}, false, errors.New("교보문고 응답 코드 503")
	}
	for _, ed := range f.results[pid] {
		if ed.PID == pid {
			return ed, true, nil
		}
	}
	return kyobo.Edition{}, false, nil
}

func TestExtractUsesLinkedKyoboProduct(t *testing.T) {
	linked := kyobo.Edition{PID: "S000213812009", Barcode: "9788965966388", ISBN13: "9788965966388", Title: "세계 너머의 세계", Authors: []string{"에릭 호엘"}, Publisher: "흐름출판"}
	search := &productSearch{results: map[string][]kyobo.Edition{
		// 상품 번호로 검색하면 다른 상품이 함께 나와도 상품 번호가 같은 것만 쓴다.
		"S000213812009": {{PID: "S000000000001", ISBN13: "9791160077834", Title: "트렌드 너머의 세계"}, linked},
		// 제목으로 검색해서 같은 ISBN이 다른 상품 번호로 나와도 링크의 상품 번호를 쓴다.
		"세계 너머의 세계": {{PID: "S000000000002", ISBN13: "9788965966388", Title: "세계 너머의 세계(큰글자책)"}},
	}}
	in := Input{
		PostedAt: postedAt,
		Text:     "이번 달 과학 모임 책",
		Links:    []book.Link{{URL: "https://product.kyobobook.co.kr/detail/S000213812009", Title: "세계 너머의 세계 | 교보문고"}},
		Comments: []Comment{
			// 같은 상품을 다시 걸면 한 번만 찾는다. 찾다가 실패한 상품과 찾지 못한 상품은 정보를 붙이지 않는다.
			{At: commentedAt, Text: "저도 샀어요", Links: []book.Link{
				{URL: "https://product.kyobobook.co.kr/detail/S000213812009/"},
				{URL: "https://product.kyobobook.co.kr/detail/S900000000000", Title: "실패"},
				{URL: "https://product.kyobobook.co.kr/detail/S000000000404", Title: "없음"},
			}},
		},
	}
	story := found("세계 너머의 세계", "9788965966388", []string{"과학"}, "요약", nil, m{"comment": 1, "summary": "구매"})
	failed := found("실패한 상품", "9788900000001", []string{}, "", nil, m{"comment": 1, "summary": "실패"})
	api := &fakeAPI{responses: []string{
		message("tool_use", `{"type":"tool_use","id":"tu_1","name":"search_kyobo","input":{"query":"세계 너머의 세계"}}`),
		message("end_turn", textBlock(m{"books": []m{story, failed}})),
	}}

	got, err := New(api, search, testTags).Extract(context.Background(), in)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if !slices.Equal(search.queries, []string{"S000213812009", "S900000000000", "S000000000404", "세계 너머의 세계"}) {
		t.Errorf("검색어 = %v", search.queries)
	}
	if b := got[0].Book; b.KyoboPID != "S000213812009" || b.ISBN13 != "9788965966388" || b.Title != "세계 너머의 세계" ||
		b.Publisher != "흐름출판" || !slices.Equal(b.Authors, []string{"에릭 호엘"}) || b.KyoboBarcode != "" {
		t.Errorf("링크의 상품을 쓰고, ISBN과 같은 바코드는 따로 두지 않아야 한다: %+v", b)
	}
	// 찾지 못한 상품의 ISBN은 검색 결과에 없었으므로 버린다.
	if b := got[1].Book; b.ISBN13 != "" || b.KyoboPID != "" {
		t.Errorf("찾지 못한 상품의 ISBN은 버려야 한다: %+v", b)
	}
	req, _ := json.Marshal(api.calls[0])
	body := string(req)
	if !strings.Contains(body, "[1] 세계 너머의 세계 | 교보문고 https://product.kyobobook.co.kr/detail/S000213812009 → 교보문고 상품: 상품 번호 S000213812009, isbn13 9788965966388, 세계 너머의 세계, 에릭 호엘, 흐름출판") ||
		!strings.Contains(body, "링크 [2]  https://product.kyobobook.co.kr/detail/S000213812009/ → 교보문고 상품: 상품 번호 S000213812009, isbn13 9788965966388") {
		t.Errorf("상품 링크 뒤에 판본 정보를 붙여야 한다: %s", body)
	}
	for _, unwanted := range []string{"S900000000000 →", "S000000000404 →", "트렌드 너머의 세계"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("요청에 %q가 있으면 안 된다", unwanted)
		}
	}
	// 상품 링크는 서점 링크라서 언급에 붙이지 않는다.
	if l := got[0].Mentions[0].Links; len(l) != 0 {
		t.Errorf("본문 링크 = %+v", l)
	}
}

func TestExtractKeepsLinkedEditionApart(t *testing.T) {
	brief := kyobo.Edition{PID: "S000001128340", Barcode: "9788972914051", ISBN13: "9788972914051", Title: "시간의 역사", Authors: []string{"스티븐 호킹", "레오나르드 믈로디노프"}, Publisher: "까치"}
	definitive := kyobo.Edition{PID: "S000001128591", Barcode: "9788972917359", ISBN13: "9788972917359", Title: "시간의 역사(결정판)", Authors: []string{"스티븐 호킹"}, Publisher: "까치"}
	search := &productSearch{results: map[string][]kyobo.Edition{
		"S000001128340": {brief},
		"시간의 역사":        {definitive, brief},
	}}
	in := Input{
		PostedAt: postedAt,
		Text:     "시간의 역사 결정판을 소개합니다.",
		Comments: []Comment{{At: commentedAt, Text: "이 책도 같이 봤어요", Links: []book.Link{{URL: "https://product.kyobobook.co.kr/detail/S000001128340"}}}},
	}
	// 괄호 속 판본 표기를 빼면 작품 키가 같지만, 댓글의 링크가 가리킨 상품은 따로 넣는다.
	body := found("시간의 역사(결정판)", "9788972917359", []string{"과학"}, "결정판 소개", nil)
	comment := found("시간의 역사", "9788972914051", []string{"과학"}, "", nil, m{"comment": 1, "summary": "함께 소개"})
	comment["kyobo_pid"] = "S000001128340"
	// 같은 판본이 두 번 나오면 처음 것만 쓴다.
	again := found("시간의 역사", "9788972914051", []string{}, "", nil, m{"comment": 1, "summary": "중복"})
	api := &fakeAPI{responses: []string{
		message("tool_use", `{"type":"tool_use","id":"tu_1","name":"search_kyobo","input":{"query":"시간의 역사"}}`),
		message("end_turn", textBlock(m{"books": []m{body, comment, again}})),
	}}

	got, err := New(api, search, testTags).Extract(context.Background(), in)
	if err != nil || len(got) != 2 {
		t.Fatalf("결정판과 링크의 판본 두 권이어야 한다: %+v, %v", got, err)
	}
	if b := got[0]; b.Book.ISBN13 != "9788972917359" || b.Linked {
		t.Errorf("검색으로 고른 결정판 = %+v", b)
	}
	if b := got[1]; b.Book.ISBN13 != "9788972914051" || b.Book.KyoboPID != "S000001128340" || !b.Linked || b.Mentions[0].Summary != "함께 소개" {
		t.Errorf("링크의 판본 = %+v", b)
	}
}

func TestExtractUsesLinkedProductWithoutISBN(t *testing.T) {
	magazine := kyobo.Edition{PID: "S000221152938", Barcode: "3904000631271", Title: "스켑틱(Skeptic)(47호)", Authors: []string{"바다출판사 편집부"}, Publisher: "바다출판사"}
	search := &productSearch{results: map[string][]kyobo.Edition{"S000221152938": {magazine}}}
	in := Input{PostedAt: postedAt, Text: "과학 잡지 최신호", Links: []book.Link{{URL: "https://product.kyobobook.co.kr/detail/S000221152938", Title: "스켑틱 47호"}}}
	skeptic := found("한국 스켑틱 47호", "", []string{"과학"}, "요약", nil)
	skeptic["kyobo_pid"] = "S000221152938"
	// 링크에 없던 상품 번호는 지어낸 것일 수 있으므로 버린다.
	other := found("다른 잡지", "", []string{}, "요약", nil)
	other["kyobo_pid"] = "S000000000777"
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(m{"books": []m{skeptic, other}}))}}

	got, err := New(api, search, testTags).Extract(context.Background(), in)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if b := got[0].Book; b.KyoboPID != "S000221152938" || b.KyoboBarcode != "3904000631271" || b.ISBN13 != "" ||
		b.Title != "스켑틱(Skeptic)(47호)" || b.Publisher != "바다출판사" {
		t.Errorf("ISBN 없는 상품의 상품 번호와 바코드를 써야 한다: %+v", b)
	}
	if b := got[1].Book; b.KyoboPID != "" || b.KyoboBarcode != "" || b.Title != "다른 잡지" {
		t.Errorf("링크에 없던 상품 번호는 버려야 한다: %+v", b)
	}
	req, _ := json.Marshal(api.calls[0])
	if !strings.Contains(string(req), "→ 교보문고 상품: 상품 번호 S000221152938, isbn13 없음, 스켑틱(Skeptic)(47호), 바다출판사 편집부, 바다출판사") {
		t.Errorf("ISBN 없는 상품도 링크 뒤에 붙여야 한다: %s", req)
	}
}

func TestExtractUsesChosenLinksWhenPostCoversSeveralBooks(t *testing.T) {
	final := m{"books": []m{
		found("당신 인생의 이야기", "", []string{"SF"}, "요약", []int{1, 3, 7}),
		found("숨", "", []string{"SF"}, "요약", []int{2}),
	}}
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(final))}}
	got, err := New(api, &fakeSearch{}, testTags).Extract(context.Background(), input)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	// 1번(교보문고)은 서점 링크라서, 7번은 없는 번호라서 빠진다.
	if l := got[0].Mentions[0].Links; len(l) != 1 || l[0].URL != "https://blog.example.com/review" {
		t.Errorf("첫 책 링크 = %+v", l)
	}
	if l := got[1].Mentions[0].Links; len(l) != 1 || l[0].URL != "https://news.example.com/ted-chiang" {
		t.Errorf("둘째 책 링크 = %+v", l)
	}
}

func TestExtractDropsUnrelatedLinks(t *testing.T) {
	in := input
	in.Comments = append(slices.Clone(input.Comments), Comment{At: commentedAt, Text: "숨 리뷰와 모임 장소예요", Links: []book.Link{
		{URL: "https://blog.example.com/breath", Title: "숨 리뷰"},
		{URL: "https://naver.me/map", Title: "네이버지도"},
	}})
	// 링크 번호: 본문 1~4, 1번 댓글 5~6, 3번 댓글 7~8
	story := found("당신 인생의 이야기", "", []string{"SF"}, "요약", nil)
	story["unrelated_links"] = []int{2, 4, 0, 99}
	breath := found("숨", "", []string{"SF"}, "", nil, m{"comment": 1, "summary": "추천"}, m{"comment": 3, "summary": "리뷰 공유"})
	breath["unrelated_links"] = []int{5, 8}
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(m{"books": []m{story, breath}}))}}

	got, err := New(api, &fakeSearch{}, testTags).Extract(context.Background(), in)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	// 본문이 한 권만 다뤄서 본문 링크를 모두 붙이지만, 관련 없다고 판정한 2번과 4번은 뺀다. 없는 번호는 무시한다.
	if l := got[0].Mentions[0].Links; len(l) != 1 || l[0].URL != "https://blog.example.com/review" {
		t.Errorf("본문 링크 = %+v", l)
	}
	// 1번 댓글의 5번(유튜브)은 관련 없고 6번은 서점 링크라서 남는 링크가 없다.
	if l := got[1].Mentions[0].Links; len(l) != 0 {
		t.Errorf("1번 댓글 링크 = %+v", l)
	}
	// 3번 댓글은 8번(지도)만 빠진다.
	if l := got[1].Mentions[1].Links; len(l) != 1 || l[0].URL != "https://blog.example.com/breath" {
		t.Errorf("3번 댓글 링크 = %+v", l)
	}
	req, _ := json.Marshal(api.calls[0])
	for _, want := range []string{"링크 [7] 숨 리뷰", "링크 [8] 네이버지도"} {
		if !strings.Contains(string(req), want) {
			t.Errorf("요청에 %s가 없다", want)
		}
	}
}

func TestExtractRequest(t *testing.T) {
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(m{"books": []any{}}))}}
	got, err := New(api, &fakeSearch{}, testTags).Extract(context.Background(), input)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
	body, err := json.Marshal(api.calls[0])
	if err != nil {
		t.Fatal(err)
	}
	req := string(body)
	for _, want := range []string{
		`"model":"claude-opus-5-5"`, `"effort":"medium"`, `"type":"json_schema"`, `"fallbacks":"default"`,
		`"name":"search_kyobo"`, `"strict":true`, `"media_type":"image/jpeg"`, `게시일: 2026-10-21`,
		`[3] 리뷰 https://blog.example.com/review`, `[1] (2026-10-21) 같은 작가의 '숨'도 좋아요`,
		`링크 [5]  https://www.youtube.com/watch?v=abc`, `링크 [6] 알라딘 https://www.aladin.co.kr`, `[4]  https://bookshelf.example.com/숨/`, `[2] (2026-10-21) 다음 모임 언제예요?`, `"enum":["AI","SF","과학"`, `모임 주제 태그(AI, SF, 과학)`,
	} {
		if !strings.Contains(req, want) {
			t.Errorf("요청에 %s가 없다", want)
		}
	}
}

func TestExtractRefusal(t *testing.T) {
	api := &fakeAPI{responses: []string{message("refusal")}}
	if _, err := New(api, &fakeSearch{}, testTags).Extract(context.Background(), input); !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

func TestExtractStopsAfterTooManyToolCalls(t *testing.T) {
	var responses []string
	for range maxTurns + 1 {
		responses = append(responses, message("tool_use", `{"type":"tool_use","id":"tu","name":"search_kyobo","input":{"query":"q"}}`))
	}
	if _, err := New(&fakeAPI{responses: responses}, &fakeSearch{}, testTags).Extract(context.Background(), input); err == nil {
		t.Error("도구 호출이 끝없이 이어지면 오류여야 한다")
	}
}

func TestTagsDropScienceFromSF(t *testing.T) {
	for _, tt := range []struct{ in, want []string }{
		{[]string{"과학", "SF", "소설"}, []string{"SF", "소설"}},
		{[]string{"과학", "인문"}, []string{"과학", "인문"}},
		{[]string{"SF", "SF", "없는 태그", "AI", "소설", "철학", "역사", "인문"}, []string{"SF", "AI", "소설", "철학", "역사"}},
		{nil, nil},
	} {
		if got := pickTags(tt.in, testTags); !slices.Equal(got, tt.want) {
			t.Errorf("tags(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestExtractUsesGivenTags(t *testing.T) {
	// 관리 화면에서 더한 모임 주제가 프롬프트와 스키마에 들어가고, 목록에 없는 태그는 버린다.
	tags := book.TagList{{Name: "기후", Topic: true}, {Name: "AI", Topic: true}, {Name: "인문"}}
	final := m{"books": []m{found("무수면 사회", "", []string{"기후", "소설", "인문"}, "요약", nil)}}
	api := &fakeAPI{responses: []string{message("end_turn", textBlock(final))}}
	got, err := New(api, &fakeSearch{}, tags).Extract(context.Background(), input)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if strings.Join(got[0].Book.Tags, ",") != "기후,인문" {
		t.Errorf("태그 = %v", got[0].Book.Tags)
	}
	body, _ := json.Marshal(api.calls[0])
	for _, want := range []string{`"enum":["기후","AI","인문"]`, `모임 주제 태그(기후, AI)`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("요청에 %s가 없다", want)
		}
	}
}
