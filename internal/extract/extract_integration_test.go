//go:build integration

package extract

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/claude"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
)

// 실제 Claude API와 교보문고 검색을 호출한다. 게시글과 댓글은 지어낸 것이고,
// 지어낸 개인 정보가 요약에 남지 않는지, 댓글에서 소개한 책을 찾는지 본다.
// 실행: make test-integration
func TestExtractWithClaude(t *testing.T) {
	client, err := claude.NewClient()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	posted := time.Now().Add(-24 * time.Hour)
	in := Input{
		PostedAt: posted,
		Text: `#책_소개
홍길동(경영 97, 판교 IT회사 다님) 선배가 지난주 신촌 모임에서 추천해 준 테드 창의 '당신 인생의 이야기'를 다 읽었어요.
표제작은 외계 언어를 배우면서 시간을 다르게 인식하게 되는 언어학자의 이야기인데, 딸 지민이를 키우는 제 처지와 겹쳐서 많이 울었네요.
'바빌론의 탑'도 신화를 과학적으로 풀어 내는 상상력이 대단했습니다. SF를 어려워하는 분들께도 권합니다.`,
		Links: []book.Link{
			{URL: "https://product.kyobobook.co.kr/detail/S000001981838", Title: "당신 인생의 이야기 - 교보문고"},
			{URL: "https://www.hani.co.kr/arti/culture/book/1.html", Title: "테드 창 인터뷰 - 한겨레"},
		},
		Comments: []Comment{
			{At: posted.Add(time.Hour), Text: "저는 김철수(법학 98)인데요, 같은 작가의 '숨'이 더 좋았어요. 인공지능 이야기도 있어서 이번 달 AI 주제랑도 맞아요.",
				Links: []book.Link{{URL: "https://www.youtube.com/watch?v=ted-chiang", Title: ""}}},
			{At: posted.Add(2 * time.Hour), Text: "다음 모임은 언제인가요?"},
		},
	}
	got, err := New(&client.Beta.Messages, kyobo.NewClient(), testTags).Extract(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var story, breath *book.Finding
	for i, f := range got {
		t.Logf("책: %+v", f.Book)
		for _, m := range f.Mentions {
			t.Logf("  [%s] %s\n%s\n  링크: %+v", m.Kind, m.At.Format(time.DateTime), m.Summary, m.Links)
			for _, private := range []string{"홍길동", "길동", "김철수", "97", "98", "판교", "IT회사", "지민", "신촌", "법학", "경영"} {
				if strings.Contains(m.Summary, private) {
					t.Errorf("요약에 개인 정보 %q가 남았다", private)
				}
			}
			for _, l := range m.Links {
				if strings.Contains(l.URL, "kyobobook") {
					t.Errorf("서점 구매 링크가 남았다: %s", l.URL)
				}
			}
		}
		switch {
		case strings.Contains(f.Book.Title, "당신 인생의 이야기"):
			story = &got[i]
		case strings.HasPrefix(f.Book.Title, "숨"):
			breath = &got[i]
		}
	}
	if story == nil || !book.ValidISBN13(story.Book.ISBN13) || len(story.Mentions) == 0 || story.Mentions[0].Kind != book.InPost {
		t.Fatalf("본문의 책을 교보문고 판본으로 찾아야 한다: %+v", story)
	}
	if len(story.Mentions[0].Links) != 1 || !strings.Contains(story.Mentions[0].Links[0].URL, "hani.co.kr") {
		t.Errorf("본문 링크는 서점 링크만 빼고 모두 붙어야 한다: %+v", story.Mentions[0].Links)
	}
	if breath == nil {
		t.Fatal("댓글에서 소개한 책을 찾아야 한다")
	}
	var commented bool
	for _, m := range breath.Mentions {
		if m.Kind == book.InComment && len(m.Links) == 1 && strings.Contains(m.Links[0].URL, "youtube") {
			commented = true
		}
	}
	if !commented {
		t.Errorf("댓글 언급과 그 댓글의 링크가 있어야 한다: %+v", breath.Mentions)
	}
}

// 여러 권으로 된 책은 언급마다 하나만 넣고(짚은 권 또는 세트), 그 책과 직접 관련 없는 링크는 빼는지 본다.
func TestExtractSeriesAndUnrelatedLinksWithClaude(t *testing.T) {
	client, err := claude.NewClient()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	posted := time.Now().Add(-24 * time.Hour)
	in := Input{
		PostedAt: posted,
		Text: `#독서_인증
넬레 노이하우스의 신작 '몬스터' 1, 2권을 주말에 다 읽었어요. 호프하임 경찰서 시리즈답게 결말이 궁금해서 손을 놓을 수 없었습니다.
자세한 감상은 블로그에 적었어요. 이번 모임 장소 지도와 요즘 화제인 AI 기사도 같이 올립니다.`,
		Links: []book.Link{
			{URL: "https://blog.example.com/monster-review", Title: "넬레 노이하우스 '몬스터' 읽고 - 독서 일기"},
			{URL: "https://naver.me/map-example", Title: "네이버지도"},
			{URL: "https://news.example.com/gpu", Title: "젠슨 황 \"새 GPU로 AI 시대 연다\""},
		},
		Comments: []Comment{
			{At: posted.Add(time.Hour), Text: "닐 셔스터먼의 수확자 시리즈도 추천해요. 죽음이 사라진 미래 이야기예요."},
		},
	}
	got, err := New(&client.Beta.Messages, kyobo.NewClient(), testTags).Extract(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var monsters, reapers []book.Finding
	for _, f := range got {
		t.Logf("책: %+v", f.Book)
		for _, m := range f.Mentions {
			t.Logf("  [%s] 링크: %+v", m.Kind, m.Links)
		}
		switch {
		case strings.Contains(f.Book.Title, "몬스터"):
			monsters = append(monsters, f)
		case strings.Contains(f.Book.Title, "수확자"):
			reapers = append(reapers, f)
		}
	}
	if len(monsters) != 1 {
		t.Fatalf("언급 하나가 1, 2권을 함께 다루면 한 권만 넣어야 한다: %d권", len(monsters))
	}
	if title := monsters[0].Book.Title; strings.Contains(title, "2") || strings.Contains(title, "세트") {
		t.Errorf("여러 권을 짚어 다루면 앞선 낱권을 골라야 한다: %s", title)
	}
	if links := monsters[0].Mentions[0].Links; len(links) != 1 || !strings.Contains(links[0].URL, "monster-review") {
		t.Errorf("그 책의 감상 링크만 남고 지도와 다른 주제 기사는 빠져야 한다: %+v", links)
	}
	// 교보문고에 '수확자 시리즈 세트'가 있다.
	if len(reapers) != 1 || !strings.Contains(reapers[0].Book.Title, "세트") || !book.ValidISBN13(reapers[0].Book.ISBN13) {
		t.Errorf("시리즈를 통째로 다루면 세트 ISBN으로 한 번만 넣어야 한다: %+v", reapers)
	}
}

// 대화방 주간 요약처럼 본문이 책 한 권과 책이 아닌 여러 이야기를 함께 담으면,
// 책 이야기 밖에 있는 링크는 그 책에 붙지 않아야 한다.
func TestExtractDigestDropsLinksOutsideBookWithClaude(t *testing.T) {
	client, err := claude.NewClient()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	posted := time.Now().Add(-24 * time.Hour)
	in := Input{
		PostedAt: posted,
		Text: `#대화방_주간요약
1. 테드 창의 '당신 인생의 이야기'가 다시 화제였습니다. 원작 영화 '컨택트'와 비교하는 이야기가 많았고, 독서 기록을 공유해 주신 분도 있었어요.
2. 카드사와 영화사의 캐릭터 카드 제휴 소식이 공유됐습니다.
3. 챗GPT가 접속부사가 들어간 문장을 자주 틀린다는 기사가 화제였어요.
4. AI 회사가 자사 AI를 함부로 대하지 말자는 원칙을 정했다는 기사도 나눴습니다.`,
		Links: []book.Link{
			{URL: "https://bookshelf.example.com/story-of-your-life", Title: "당신 인생의 이야기 | 독서 기록"},
			{URL: "https://news.example.com/card", Title: "카드사, 영화사와 독점 제휴 캐릭터 카드 출시"},
			{URL: "https://news.example.com/chatgpt", Title: "챗GPT, 접속부사 포함된 문장 5개 중 1개 틀렸다"},
			{URL: "https://news.example.com/ai-welfare", Title: "\"AI도 괴롭히면 안 된다\"…AI 회사 원칙 명문화"},
		},
	}
	got, err := New(&client.Beta.Messages, kyobo.NewClient(), testTags).Extract(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Book.Title, "당신 인생의 이야기") {
		t.Fatalf("책은 당신 인생의 이야기 하나여야 한다: %+v", got)
	}
	links := got[0].Mentions[0].Links
	t.Logf("링크: %+v", links)
	if len(links) != 1 || !strings.Contains(links[0].URL, "bookshelf") {
		t.Errorf("책 이야기 밖의 기사 링크는 빠져야 한다: %+v", links)
	}
}

// 제목 없는 독서 기록 링크만 있어도 주소에 든 책 제목으로 책을 찾는지 본다.
func TestExtractBooksFromReadingLogLinksWithClaude(t *testing.T) {
	client, err := claude.NewClient()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	in := Input{
		PostedAt: time.Now().Add(-24 * time.Hour),
		Text:     "요즘 달리기에 빠져서 달리기 책을 몇 권 읽고 독서 기록을 남겼어요.",
		Links: []book.Link{
			{URL: "https://bookshelf.example.com/%EC%97%B0%ED%9D%AC%EB%8F%99-%EB%9F%AC%EB%84%88/", Title: ""},
			{URL: "https://bookshelf.example.com/born-to-run/", Title: ""},
		},
	}
	got, err := New(&client.Beta.Messages, kyobo.NewClient(), testTags).Extract(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var runner, born bool
	for _, f := range got {
		t.Logf("책: %+v 링크: %+v", f.Book, f.Mentions[0].Links)
		title := strings.ToLower(strings.ReplaceAll(f.Book.Title, " ", ""))
		runner = runner || strings.Contains(title, "연희동러너")
		born = born || strings.Contains(title, "본투런") || strings.Contains(title, "borntorun")
	}
	if !runner || !born {
		t.Errorf("독서 기록 링크 주소로 '연희동 러너'와 '본 투 런'을 찾아야 한다: %+v", got)
	}
}

// 《 》나 『 』로 감싼 제목은 지나가듯 언급해도 책으로 넣는지 본다.
func TestExtractBracketedTitlesWithClaude(t *testing.T) {
	client, err := claude.NewClient()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	in := Input{
		PostedAt: time.Now().Add(-24 * time.Hour),
		Text: `#대화방_주간요약
1. 이번 주에는 달리기 이야기가 많았어요. 주말에 한강을 같이 뛰자는 이야기도 나왔습니다.
2. 산책하다가 《소설가 구보씨의 일일》 생각이 났다는 분이 있었어요.
3. 다음 모임 장소는 다음 주에 정하기로 했습니다.`,
	}
	got, err := New(&client.Beta.Messages, kyobo.NewClient(), testTags).Extract(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range got {
		t.Logf("책: %+v", f.Book)
		found = found || strings.Contains(strings.ReplaceAll(f.Book.Title, " ", ""), "소설가구보씨의일일")
	}
	if !found {
		t.Error("《 》로 감싼 제목은 책으로 넣어야 한다")
	}
}
