// Package book은 책과 책을 다룬 게시글을 표현하는 도메인 코드다.
package book

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Seoul은 모임과 밴드의 시간대다. 밴드 화면의 시각을 읽고 화면에 날짜를 그릴 때 쓴다.
var Seoul = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		return time.FixedZone("KST", 9*60*60)
	}
	return loc
}()

// Book은 책 한 권이다. 같은 작품의 판본은 하나의 Book으로 묶는다. 태그는 여러 개 붙는다.
type Book struct {
	ID        int64
	ISBN13    string
	Title     string
	Authors   []string
	Publisher string
	KyoboPID  string
	// KyoboBarcode는 잡지나 전자책처럼 바코드가 ISBN-13과 다른 교보문고 상품의 바코드다. 표지 이미지에 쓴다.
	KyoboBarcode string
	Tags         []string
}

// CoverCode는 교보문고 표지 이미지를 찾는 바코드다. ISBN과 다른 상품 바코드가 있으면 그것이고, 없으면 ISBN이다.
// 전자책은 ISBN이 있어도 표지를 전자책 바코드로만 준다. 둘 다 없으면 빈 문자열이다.
func (b Book) CoverCode() string {
	if b.KyoboBarcode != "" {
		return b.KyoboBarcode
	}
	return b.ISBN13
}

// Link는 요약에 덧붙이는 책 관련 링크다.
type Link struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// MentionKind는 책을 언급한 곳이다.
type MentionKind string

const (
	// InPost는 게시글 본문에서 다룬 것이다.
	InPost MentionKind = "post"
	// InComment는 게시글의 댓글에서 다룬 것이다.
	InComment MentionKind = "comment"
)

// Mention은 게시글 본문이나 댓글 하나가 책에 대해 한 이야기를 개인 정보를 빼고 요약한 것이다.
// 책 상세 화면에 댓글처럼 쌓인다.
type Mention struct {
	// ID와 BookID는 저장된 언급에만 있다. 관리 화면에서 언급을 고칠 때 쓴다.
	ID      int64
	BookID  int64
	PostNo  int
	Kind    MentionKind
	At      time.Time
	Summary string
	Links   []Link
}

// Finding은 게시글 하나(본문과 댓글)에서 찾은 책과 그 책에 대한 언급이다.
type Finding struct {
	Book     Book
	Mentions []Mention
	// Linked는 글쓴이가 교보문고 상품 링크로 가리킨 상품을 골랐다는 뜻이다.
	// 작품 키가 같아도 ISBN이 다른 책과는 묶지 않는다(예: 시간의 역사와 시간의 역사(결정판)).
	Linked bool
}

// Post는 저장할 게시글의 식별 정보다. 본문과 댓글은 저장하지 않는다.
type Post struct {
	No          int
	PostedAt    time.Time
	ContentHash string
	// CommentCount는 밴드 화면에 나온 댓글 수다. 새 댓글이 달린 글을 찾는 데 쓴다.
	CommentCount int
}

// Thread는 책 상세 화면에서 게시글 하나에 속한 언급의 묶음이다.
// 본문 언급이 있으면 Post에 두고, 댓글 언급은 그 아래에 답글처럼 붙인다.
type Thread struct {
	PostNo   int
	Post     *Mention
	Comments []Mention
}

// Threads는 시각 순으로 정렬된 언급을 게시글별로 묶는다. 묶음의 순서는 각 게시글의 첫 언급 순서다.
func Threads(mentions []Mention) []Thread {
	var threads []Thread
	index := map[int]int{}
	for _, m := range mentions {
		i, ok := index[m.PostNo]
		if !ok {
			i = len(threads)
			index[m.PostNo] = i
			threads = append(threads, Thread{PostNo: m.PostNo})
		}
		if m.Kind == InPost && threads[i].Post == nil {
			post := m
			threads[i].Post = &post
		} else {
			threads[i].Comments = append(threads[i].Comments, m)
		}
	}
	return threads
}

// Shelved는 책 목록에 보여 줄 책과 마지막으로 다뤄진 시각이다.
type Shelved struct {
	Book         Book
	LastPostedAt time.Time
	PostCount    int
}

// Entry는 타임라인의 소식 하나다. IsNew가 참이면 이 게시글로 책이 처음 등록됐다는 뜻이다.
// Summary는 그 글이 책에 대해 한 이야기의 요약이다. 본문 언급이 있으면 그것을, 없으면 첫 댓글 언급을 쓴다.
// Comments는 그 글의 댓글 중 이 책을 다룬 언급의 수, Mentions는 이 책의 모든 언급 수다.
type Entry struct {
	Book     Book
	PostNo   int
	PostedAt time.Time
	IsNew    bool
	Summary  string
	Comments int
	Mentions int
}

// Count는 태그나 저자별 책 수다.
type Count struct {
	Name  string
	Books int
}

var bracketed = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// WorkKey는 판본과 표기 차이를 무시하고 같은 작품을 가리키는 키를 만든다.
// 괄호 안의 판본 표기와 콜론 뒤의 부제를 빼고, 글자와 숫자만 남긴 제목에 대표 저자를 붙인다.
func WorkKey(title string, authors []string) string {
	key := normalize(title, true)
	if len(authors) > 0 {
		key += "|" + normalize(authors[0], false)
	}
	return key
}

// SameWork는 한 글 안에서 두 책이 같은 책인지 알려 준다. 작품 키가 같으면 같은 책이지만,
// 한쪽이 글쓴이가 상품 링크로 가리킨 판본(linked)이고 둘의 ISBN이 다르면 다른 책이다(예: 시간의 역사와 시간의 역사(결정판)).
func SameWork(a Book, aLinked bool, b Book, bLinked bool) bool {
	if WorkKey(a.Title, a.Authors) != WorkKey(b.Title, b.Authors) {
		return false
	}
	separate := (aLinked || bLinked) && a.ISBN13 != "" && b.ISBN13 != "" && a.ISBN13 != b.ISBN13
	return !separate
}

func normalize(s string, dropSubtitle bool) string {
	s = bracketed.ReplaceAllString(s, "")
	if dropSubtitle {
		if i := strings.IndexAny(s, ":："); i > 0 {
			s = s[:i]
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CleanISBN은 ISBN 사이의 하이픈과 공백을 뺀다.
func CleanISBN(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// ValidISBN13은 s가 체크섬까지 맞는 ISBN-13인지 검사한다.
func ValidISBN13(s string) bool {
	if len(s) != 13 || (!strings.HasPrefix(s, "978") && !strings.HasPrefix(s, "979")) {
		return false
	}
	sum := 0
	for i, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return sum%10 == 0
}
