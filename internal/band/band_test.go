package band

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/yonsei-alumni/abc-library/internal/book"
)

func TestParseTime(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, book.Seoul)
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026년 10월 23일 오후 8:09", time.Date(2026, 10, 23, 20, 9, 0, 0, book.Seoul)},
		{"2026년 10월 21일 오전 1:24", time.Date(2026, 10, 21, 1, 24, 0, 0, book.Seoul)},
		{"2026년 3월 5일 오후 12:30", time.Date(2026, 3, 5, 12, 30, 0, 0, book.Seoul)},
		{"2026년 3월 5일 오전 12:30", time.Date(2026, 3, 5, 0, 30, 0, 0, book.Seoul)},
		{"10월 23일 오후 8:09", time.Date(2026, 10, 23, 20, 9, 0, 0, book.Seoul)},
		// 연도가 없고 올해로 보면 미래가 되는 날짜는 작년 글이다.
		{"12월 31일 오후 11:00", time.Date(2025, 12, 31, 23, 0, 0, 0, book.Seoul)},
	}
	for _, tt := range tests {
		got, err := ParseTime(tt.in, now)
		if err != nil {
			t.Errorf("ParseTime(%q): %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseTime(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"", "16시간 전", "2026-10-23 20:09"} {
		if _, err := ParseTime(bad, now); err == nil {
			t.Errorf("ParseTime(%q)는 오류여야 한다", bad)
		}
	}
}

func TestPostURL(t *testing.T) {
	if got := PostURL("102765569", 187); got != "https://band.us/band/102765569/post/187" {
		t.Error(got)
	}
}

// evaluate는 testdata의 HTML을 브라우저에 띄우고 스크립트를 실행한다.
// Chromium이 설치되어 있지 않으면 건너뛴다(make band-login이 설치한다).
func evaluate(t *testing.T, file, script string, args ...any) any {
	t.Helper()
	v, err := openPage(t, file).Evaluate(script, args...)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// openPage는 고정 HTML을 띄운 헤드리스 브라우저 페이지를 연다.
func openPage(t *testing.T, file string) playwright.Page {
	t.Helper()
	pw, err := playwright.Run(&playwright.RunOptions{Verbose: false})
	if err != nil {
		t.Skip("Playwright 드라이버가 없다:", err)
	}
	t.Cleanup(func() { _ = pw.Stop() })
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true)})
	if err != nil {
		t.Skip("Chromium이 없다:", err)
	}
	t.Cleanup(func() { _ = browser.Close() })
	page, err := browser.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := page.SetContent(string(html)); err != nil {
		t.Fatal(err)
	}
	return page
}

// TestCardsScript는 게시글 목록 화면을 흉내 낸 HTML에서 카드의 글 번호와 댓글 수를 읽는다.
func TestCardsScript(t *testing.T) {
	v := evaluate(t, "testdata/list.html", cardsScript, "1")
	var cards []Activity
	if err := json.Unmarshal([]byte(raw(v)), &cards); err != nil {
		t.Fatal(err)
	}
	// 카드 밖의 링크, 본문 속 다른 글 링크, 다른 밴드의 글은 빼고, 표정 수를 댓글 수로 읽지 않는다.
	want := []Activity{{No: 30, Comments: 2}, {No: 12, Comments: 1024}, {No: 5, Comments: 0}}
	if !slices.Equal(cards, want) {
		t.Errorf("카드 = %+v, want %+v", cards, want)
	}
	if strings.Contains(raw(v), "회원") || strings.Contains(raw(v), "미리보기") {
		t.Errorf("작성자와 댓글은 읽지 않아야 한다: %s", raw(v))
	}
}

// TestExtractScript는 밴드 게시글 화면을 흉내 낸 HTML에서 추출 스크립트를 실행한다.
func TestExtractScript(t *testing.T) {
	v := evaluate(t, "testdata/post.html", extractScript)
	var s scraped
	if err := json.Unmarshal([]byte(v.(string)), &s); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(s.Text, "테드 창의 소설집") || strings.Contains(s.Text, "가상회원") {
		t.Errorf("본문만 읽고 작성자는 읽지 않아야 한다: %q", s.Text)
	}
	if !strings.Contains(s.Text, "《소설가 구보씨의 일일》") {
		t.Errorf("사진 아래 이어지는 본문 덩어리도 읽어야 한다: %q", s.Text)
	}
	if s.Time != "2026년 10월 21일 오전 1:24" {
		t.Errorf("시각 = %q", s.Time)
	}
	// 본문의 교보문고 주소와 링크 미리보기는 하나로 합치고 미리보기 제목을 쓴다. 밴드 내부 링크는 뺀다.
	if len(s.Links) != 2 || s.Links[0].URL != "https://news.example.com/a" || s.Links[0].Title != "" ||
		s.Links[1].URL != "https://product.kyobobook.co.kr/detail/S000001981838" || s.Links[1].Title != "당신 인생의 이야기 - 교보문고" {
		t.Errorf("외부 링크와 링크 미리보기 제목을 읽어야 한다: %+v", s.Links)
	}
	if len(s.Images) != 1 || !strings.HasSuffix(s.Images[0], "/photo1.jpg") {
		t.Errorf("프로필 사진과 미리보기 썸네일을 뺀 첨부 사진만 읽어야 한다: %v", s.Images)
	}

	// 글자가 없는 댓글(스티커 등)은 빼고, 댓글 작성자는 읽지 않는다.
	if len(s.Comments) != 1 {
		t.Fatalf("댓글 = %+v", s.Comments)
	}
	c := s.Comments[0]
	if !strings.Contains(c.Text, "'숨'도 추천해요") || strings.Contains(c.Text, "댓글회원") || strings.Contains(raw(v), "가상학과") {
		t.Errorf("댓글 본문만 읽어야 한다: %+v", c)
	}
	if c.Time != "2026년 10월 21일 오전 10:57" || len(c.Links) != 1 || c.Links[0].URL != "https://www.youtube.com/watch?v=abc" {
		t.Errorf("댓글 시각과 링크 = %+v", c)
	}
	for _, l := range s.Links {
		if strings.Contains(l.URL, "youtube") {
			t.Error("댓글의 링크는 본문 링크에 섞이지 않아야 한다")
		}
	}
	// 댓글 수는 화면에 나온 수를 읽는다. 글자가 없어서 뺀 댓글도 세고, 표정 수와 헷갈리지 않는다.
	if s.CommentCount != 2 {
		t.Errorf("댓글 수 = %d", s.CommentCount)
	}
}

func raw(v any) string {
	s, _ := v.(string)
	return s
}

func TestIsLoginURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://auth.band.us/login?next_url=https%3A%2F%2Fwww.band.us%2Fband%2F102765569%2Fpost": true,
		"https://auth.band.us/email_login":                  true,
		"https://band.us/login":                             true,
		"https://band.us/band/102765569/post":               false,
		"https://band.us/band/102765569/post/188":           false,
		"https://www.band.us/band/102765569/post?sort=rcmt": false,
		"": false,
	} {
		if got := IsLoginURL(raw); got != want {
			t.Errorf("IsLoginURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestOnPostPage(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://band.us/band/102765569/post/187", true},
		{"https://www.band.us/band/102765569/post/187", true},
		{"https://www.band.us/band/102765569/post/187/", true},
		// 삭제된 글은 목록으로 옮겨 간다.
		{"https://www.band.us/band/102765569/post", false},
		{"https://www.band.us/band/102765569/post/1870", false},
		{"https://www.band.us/band/999/post/187", false},
		{"https://auth.band.us/login_page", false},
		{"https://evil.example.com/band/102765569/post/187", false},
	}
	for _, tt := range tests {
		if got := OnPostPage(tt.url, "102765569", 187); got != tt.want {
			t.Errorf("OnPostPage(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestContainsMarker(t *testing.T) {
	marker := "기간: 2026-10-19(월) ~ 2026-10-25(일)"
	texts := []string{"다른 글", "ABC 대화방 주간 요약\n\n기간: 2026-10-19(월)  ~ 2026-10-25(일)\n\n…"}
	if !ContainsMarker(texts, marker) {
		t.Error("공백이 달라도 같은 기간 줄을 찾아야 한다")
	}
	if ContainsMarker(texts, "기간: 2026-10-26(월) ~ 2026-11-01(일)") {
		t.Error("다른 기간을 찾았다")
	}
	if ContainsMarker(texts, "  ") || ContainsMarker(nil, marker) {
		t.Error("빈 표시나 빈 목록에서 찾았다")
	}
}

func TestSameLines(t *testing.T) {
	content := "제목\n기간: 2026-10-19(월)\n\n- 항목 \n- 끝"
	if !SameLines("제목\n기간: 2026-10-19(월)\n\n- 항목\n- 끝\n", content) {
		t.Error("줄 끝 공백과 마지막 줄바꿈은 무시해야 한다")
	}
	if !SameLines("제목\n기간: 2026-10-19(월)\n\n- 항목\n- 끝", content) {
		t.Error("편집기의 nbsp는 공백으로 본다")
	}
	if SameLines("제목\n기간: 2026-10-19(월)\n- 항목\n- 끝", content) {
		t.Error("빈 줄이 빠졌는데 같다고 했다")
	}
	if SameLines("제목", content) {
		t.Error("내용이 잘렸는데 같다고 했다")
	}
}

func TestLineDiff(t *testing.T) {
	for _, c := range []struct{ got, want, diff string }{
		{"가\n나\n다", "가\n나\n다", "4번째 줄부터 다름, 편집기 3줄, 요약 3줄"},
		{"가\n다", "가\n나\n다", "2번째 줄부터 다름, 편집기 2줄, 요약 3줄"},
		{"가 \n나", "가\n나", "3번째 줄부터 다름, 편집기 2줄, 요약 2줄"},
		{"", "가", "1번째 줄부터 다름, 편집기 1줄, 요약 1줄"},
		{"가\u00a0나\n다\n라", "가 나\n다", "3번째 줄부터 다름, 편집기 3줄, 요약 2줄"},
	} {
		if got := lineDiff(c.got, c.want); got != c.diff {
			t.Errorf("lineDiff(%q, %q) = %q, want %q", c.got, c.want, got, c.diff)
		}
	}
}

var writeStateArgs = map[string]any{"layer": writeLayer, "editor": writeEditor, "submit": writeSubmit}

// TestWriteStateScript는 게시가 막힌 글쓰기 창을 흉내 낸 HTML에서 화면 상태를 읽는다.
func TestWriteStateScript(t *testing.T) {
	got := raw(evaluate(t, "testdata/write.html", writeStateScript, writeStateArgs))
	var s struct {
		Submit struct {
			Disabled bool `json:"disabled"`
		} `json:"submit"`
		Chars      int      `json:"chars"`
		Paragraphs int      `json:"paragraphs"`
		Cover      string   `json:"cover"`
		Notes      []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(got), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Submit.Disabled || s.Paragraphs != 2 || s.Chars != len([]rune("가상 요약 첫 줄가상 요약 둘째 줄")) {
		t.Errorf("버튼과 글자 수를 읽어야 한다: %s", got)
	}
	if s.Cover == "" {
		t.Errorf("쓰기 창을 덮은 층을 찾아야 한다: %s", got)
	}
	for _, want := range []string{"본문이 너무 깁니다", "잠시 후 다시 시도하세요", "게시판을 고르세요"} {
		if !strings.Contains(got, want) {
			t.Errorf("안내 %q를 읽어야 한다: %s", want, got)
		}
	}
	for _, leak := range []string{"가상 요약", "회원의 글", "숨은 안내"} {
		if strings.Contains(got, leak) {
			t.Errorf("글 내용과 보이지 않는 안내 %q는 읽지 않아야 한다: %s", leak, got)
		}
	}
}

// TestWriteStateScriptWithoutEditor는 쓰기 창이 없는 게시글 목록에서 글을 읽지 않고 빈 상태를 돌려주는지 본다.
func TestWriteStateScriptWithoutEditor(t *testing.T) {
	got := raw(evaluate(t, "testdata/list.html", writeStateScript, writeStateArgs))
	if !strings.Contains(got, `"chars":0`) || !strings.Contains(got, `"submit":null`) || strings.Contains(got, "회원") {
		t.Errorf("상태 = %s", got)
	}
}

func TestWaitWriteClosed(t *testing.T) {
	c := &Crawler{browserSession: &browserSession{page: openPage(t, "testdata/write.html")}}
	c.watchDialogs()
	if _, err := c.page.Evaluate(`sel => setTimeout(() => document.querySelector(sel).remove(), 300)`, writeEditor); err != nil {
		t.Fatal(err)
	}
	if err := c.waitWriteClosed(5 * time.Second); err != nil {
		t.Errorf("편집기가 사라지면 성공해야 한다: %v", err)
	}
}

// TestWaitWriteClosedBlocked는 잠깐 뜬 안내와 알림창 문구가 오류에 남는지 본다.
func TestWaitWriteClosedBlocked(t *testing.T) {
	c := &Crawler{browserSession: &browserSession{page: openPage(t, "testdata/write.html")}}
	c.watchDialogs()
	if _, err := c.page.Evaluate(`() => {
		const e = document.createElement('div');
		e.className = 'toastLayer';
		e.textContent = '잠깐 뜬 안내';
		document.body.append(e);
		setTimeout(() => e.remove(), 1500);
		setTimeout(() => alert('가상 알림창 문구'), 200);
	}`); err != nil {
		t.Fatal(err)
	}
	err := c.waitWriteClosed(3 * time.Second)
	if err == nil {
		t.Fatal("편집기가 그대로면 실패해야 한다")
	}
	for _, want := range []string{"잠깐 뜬 안내", "alert: 가상 알림창 문구", "게시판을 고르세요"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("오류에 %q가 없다: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "가상 요약") {
		t.Errorf("오류에 글 내용이 들어가면 안 된다: %v", err)
	}
}

func TestDescribeWrite(t *testing.T) {
	if got := describeWrite(nil, nil); got != "화면 상태를 읽지 못함" {
		t.Errorf("빈 설명 = %q", got)
	}
	if got := describeWrite([]string{"alert: 가"}, []string{"{}", "{1}"}); got != "알림창 alert: 가, 화면 {} / {1}" {
		t.Errorf("설명 = %q", got)
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("가나다", 3); got != "가나다" {
		t.Errorf("경계값 = %q", got)
	}
	if got := shorten("가나다라", 3); got != "가나다…" {
		t.Errorf("줄임 = %q", got)
	}
}

func chosenBoard(t *testing.T, c *Crawler) string {
	t.Helper()
	v, err := c.page.Evaluate(`() => window.chosen || ''`)
	if err != nil {
		t.Fatal(err)
	}
	return raw(v)
}

func pageCount(t *testing.T, c *Crawler, name string) any {
	t.Helper()
	v, err := c.page.Evaluate(`name => window[name]`, name)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestWaitWriteClosedChoosesBoard는 게시판 선택 창이 늦게 떠도 게시판을 고르고 확인을 누른 뒤 게시 버튼을 한 번 더 누르는지 본다.
// 고정 HTML에는 숨은 확인 버튼이 뒤에 있어서 보이는 버튼을 눌러야 창이 닫힌다.
func TestWaitWriteClosedChoosesBoard(t *testing.T) {
	c := &Crawler{Board: "자유게시판", browserSession: &browserSession{page: openPage(t, "testdata/board.html")}}
	c.watchDialogs()
	if err := c.waitWriteClosed(6 * time.Second); err != nil {
		t.Fatal(err)
	}
	if got := chosenBoard(t, c); got != "#자유게시판" {
		t.Errorf("고른 게시판 = %q", got)
	}
	if got := pageCount(t, c, "submits"); got != 1 {
		t.Errorf("게시 버튼을 누른 수 = %v", got)
	}
}

func TestWaitWriteClosedUnknownBoard(t *testing.T) {
	c := &Crawler{Board: "없는게시판", browserSession: &browserSession{page: openPage(t, "testdata/board.html")}}
	c.watchDialogs()
	err := c.waitWriteClosed(8 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "#없는게시판 항목을 누르지 못했습니다") || !strings.Contains(err.Error(), "#자유게시판") {
		t.Fatalf("없는 게시판은 실패하고 화면의 게시판 목록을 남겨야 한다: %v", err)
	}
	if !strings.Contains(err.Error(), "lyBoardSelect") || !strings.Contains(err.Error(), `type="radio"`) {
		t.Errorf("게시판 창의 구조를 오류에 남겨야 한다: %v", err)
	}
	if strings.Contains(err.Error(), "가상 요약") {
		t.Errorf("오류에 글 내용이 들어가면 안 된다: %v", err)
	}
	if got := chosenBoard(t, c); got != "" {
		t.Errorf("다른 게시판을 고르면 안 된다: %q", got)
	}
}

func TestWaitWriteClosedNoBoardSet(t *testing.T) {
	c := &Crawler{browserSession: &browserSession{page: openPage(t, "testdata/board.html")}}
	c.watchDialogs()
	err := c.waitWriteClosed(3 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "게시판이 정해지지 않았습니다") {
		t.Errorf("게시판을 정하지 않았으면 실패해야 한다: %v", err)
	}
}

// TestWaitWriteClosedSubmitsOnce는 게시판을 고르고 게시해도 글쓰기 창이 닫히지 않을 때 게시 버튼을 되풀이해 누르지 않는지 본다.
func TestWaitWriteClosedSubmitsOnce(t *testing.T) {
	c := &Crawler{Board: "자유게시판", browserSession: &browserSession{page: openPage(t, "testdata/board.html")}}
	c.watchDialogs()
	if _, err := c.page.Evaluate(`() => { window.keepEditor = true; }`); err != nil {
		t.Fatal(err)
	}
	err := c.waitWriteClosed(5 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "게시판 고름: 자유게시판") {
		t.Fatalf("고른 사실을 오류에 남겨야 한다: %v", err)
	}
	if got := pageCount(t, c, "submits"); got != 1 {
		t.Errorf("게시 버튼을 누른 수 = %v", got)
	}
}

// TestWaitWriteClosedChoosesOnce는 확인을 눌러도 게시판 창이 닫히지 않으면 창을 덮인 채로 게시하지 않고 실패하는지 본다.
func TestWaitWriteClosedChoosesOnce(t *testing.T) {
	c := &Crawler{Board: "자유게시판", browserSession: &browserSession{page: openPage(t, "testdata/board.html")}}
	c.watchDialogs()
	if _, err := c.page.Evaluate(`() => { window.keepOpen = true; }`); err != nil {
		t.Fatal(err)
	}
	err := c.waitWriteClosed(4 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "게시 버튼을 누르지 못했습니다") || !strings.Contains(err.Error(), "checked") {
		t.Fatalf("실패와 게시판 창의 구조를 오류에 남겨야 한다: %v", err)
	}
	if got := pageCount(t, c, "confirms"); got != 1 {
		t.Errorf("확인을 누른 수 = %v", got)
	}
	if got := pageCount(t, c, "submits"); got != 0 {
		t.Errorf("덮인 게시 버튼이 눌리면 안 된다: %v", got)
	}
}

// TestClickTextScript는 편집기, 게시글 카드, 보이지 않는 요소의 글자는 누르지 않는지 본다.
func TestClickTextScript(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"본문이 너무 깁니다", true},
		{"본문이 너무", false},
		{"가상 요약 첫 줄", false},
		{"다른 회원의 글", false},
		{"숨은 안내", false},
	}
	for _, tc := range cases {
		if got := evaluate(t, "testdata/write.html", clickTextScript, tc.text); got != tc.want {
			t.Errorf("%q를 눌렀는지 = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestBoardLayerScript는 게시판 창이 없을 때 편집기나 글 내용을 읽지 않는지 본다.
func TestBoardLayerScript(t *testing.T) {
	if got := raw(evaluate(t, "testdata/write.html", boardLayerScript, boardPrompt)); got != "" {
		t.Errorf("창이 없으면 비어야 한다: %q", got)
	}
	if got := raw(evaluate(t, "testdata/write.html", boardLayerScript, "가상 요약 첫 줄")); got != "" {
		t.Errorf("편집기 안의 글은 읽지 않아야 한다: %q", got)
	}
	if got := raw(evaluate(t, "testdata/write.html", boardLayerScript, "다른 회원의 글")); got != "" {
		t.Errorf("게시글 카드는 읽지 않아야 한다: %q", got)
	}
}
