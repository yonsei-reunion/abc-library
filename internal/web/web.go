// Package web은 책과 언급 요약을 보여 주는 HTTP 서버다. 화면은 html/template으로 그리고 상호작용은 HTMX로 처리한다.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yonsei-alumni/abc-library/internal/auth"
	"github.com/yonsei-alumni/abc-library/internal/band"
	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/kyobo"
	"github.com/yonsei-alumni/abc-library/internal/store"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// pageSize는 목록 한 쪽의 항목 수다. 목록은 한 쪽보다 하나 더 읽어서 다음 쪽이 있는지 안다.
const pageSize = 24

// Store는 화면에 필요한 조회와 관리 화면의 수정이다. *store.Store가 구현한다.
type Store interface {
	Timeline(ctx context.Context, limit, offset int) ([]book.Entry, error)
	Books(ctx context.Context, f store.Filter, limit, offset int) ([]book.Shelved, error)
	BookCount(ctx context.Context, f store.Filter) (int, error)
	Book(ctx context.Context, id int64) (book.Book, []book.Mention, error)
	TagCounts(ctx context.Context) ([]book.Count, error)
	AuthorCounts(ctx context.Context) ([]book.Count, error)
	Tags(ctx context.Context) (book.TagList, error)

	AddTag(ctx context.Context, t book.Tag) error
	SetTagTopic(ctx context.Context, name string, topic bool) error
	RenameTag(ctx context.Context, old, name string) error
	MoveTag(ctx context.Context, name string, up bool) error
	DeleteTag(ctx context.Context, name string) error
	UpdateBook(ctx context.Context, b book.Book) error
	MergeBook(ctx context.Context, from, into int64) error
	DeleteBook(ctx context.Context, id int64) error
	Mention(ctx context.Context, id int64) (book.Mention, error)
	UpdateMention(ctx context.Context, id int64, summary string, links []book.Link) error
	MoveMention(ctx context.Context, id, toBook int64) error
	DeleteMention(ctx context.Context, id int64) error
	Admins(ctx context.Context) ([]store.Admin, error)
	IsAdmin(ctx context.Context, email string) (bool, error)
	AddAdmin(ctx context.Context, email, addedBy string) error
	DeleteAdmin(ctx context.Context, email string) error

	UserID(ctx context.Context, subjectHash string) (int64, error)
	UserExists(ctx context.Context, id int64) (bool, error)
	DeleteUser(ctx context.Context, id int64) error
	Categories(ctx context.Context, userID int64) ([]book.Category, error)
	Category(ctx context.Context, userID, id int64) (book.Category, error)
	CategoryByCode(ctx context.Context, code string) (book.Category, []book.Book, error)
	PublicCategories(ctx context.Context) ([]book.Category, error)
	CategoryBooks(ctx context.Context, categoryID int64) ([]book.Book, error)
	AddCategory(ctx context.Context, userID int64, c book.Category) (int64, error)
	UpdateCategory(ctx context.Context, userID int64, c book.Category) error
	DeleteCategory(ctx context.Context, userID, id int64) error
	MoveCategory(ctx context.Context, userID, id int64, up bool) error
	Bookmark(ctx context.Context, userID, bookID int64) (bool, []int64, error)
	SetBookmark(ctx context.Context, userID, bookID int64, categoryIDs []int64) error
	DeleteBookmark(ctx context.Context, userID, bookID int64) error
	ReorderCategoryBooks(ctx context.Context, userID, categoryID int64, bookIDs []int64) error
	Bookmarks(ctx context.Context, userID int64) ([]book.Bookmark, error)
}

// Login은 Google 계정 로그인이다. *auth.Google이 구현한다.
type Login interface {
	AuthCodeURL(state string, email bool) string
	Exchange(ctx context.Context, code string) (auth.Account, error)
}

// Config는 Server의 설정이다.
type Config struct {
	BandNo string
	// Login과 Sessions가 nil이면 로그인을 쓰지 않는다.
	Login    Login
	Sessions *auth.Sessions
	// Subjects가 nil이면 관리자 로그인만 쓰고, 북마크를 위한 사용자 로그인은 쓰지 않는다.
	Subjects *auth.Subjects
	// SiteURL은 사이트의 공개 주소다. 다른 사이트에서 보낸 POST 요청을 막을 때 이 주소는 믿는다.
	SiteURL string
	// Products가 nil이면 관리 화면에서 ISBN 없는 상품의 바코드를 찾지 않는다.
	Products Products
}

// Products는 교보문고 상품 찾기다. *kyobo.Client가 구현한다.
type Products interface {
	Product(ctx context.Context, pid string) (kyobo.Edition, bool, error)
}

// Server는 웹 화면이다.
type Server struct {
	store    Store
	bandNo   string
	login    Login
	sessions *auth.Sessions
	subjects *auth.Subjects
	siteURL  string
	products Products
	log      *slog.Logger
	pages    map[string]*template.Template
}

// New는 템플릿을 읽어 Server를 만든다.
func New(st Store, cfg Config, log *slog.Logger) (*Server, error) {
	s := &Server{
		store: st, bandNo: cfg.BandNo, login: cfg.Login, sessions: cfg.Sessions, subjects: cfg.Subjects, siteURL: cfg.SiteURL,
		products: cfg.Products, log: log, pages: map[string]*template.Template{},
	}
	if (s.login == nil) != (s.sessions == nil) {
		return nil, errors.New("로그인에는 Login과 Sessions가 모두 있어야 합니다")
	}
	if s.subjects != nil && s.login == nil {
		return nil, errors.New("사용자 로그인에는 Login과 Sessions가 있어야 합니다")
	}
	funcs := template.FuncMap{
		"date":     func(t time.Time) string { return t.In(book.Seoul).Format("2006년 1월 2일") },
		"isoDate":  func(t time.Time) string { return t.In(book.Seoul).Format("2006-01-02") },
		"isoTime":  func(t time.Time) string { return t.In(book.Seoul).Format(time.RFC3339) },
		"cover":    func(b book.Book) string { return coverURL(b, 240) },
		"coverBig": func(b book.Book) string { return coverURL(b, 458) },
		"buyURL":   buyURL,
		"postURL":  func(no int) string { return band.PostURL(s.bandNo, no) },
		"authors":  func(as []string) string { return strings.Join(as, ", ") },
		"static":   staticURL,
		"join":     strings.Join,
		"excerpt":  excerpt,
		"markdown": markdown,
		"add":      func(a, b int) int { return a + b },
	}
	for _, page := range []string{"timeline", "books", "book", "tags", "authors", "error", "login",
		"admin-tags", "admin-book", "admin-mention", "admin-admins", "me", "category", "shared", "shared-list", "privacy", "help"} {
		t, err := template.New("").Funcs(funcs).ParseFS(templateFiles, "templates/layout.html", "templates/parts.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	return s, nil
}

// staticURL은 정적 파일 주소에 내용 해시를 붙인다. 파일이 바뀌면 주소가 바뀌어서 브라우저 캐시를 피한다.
func staticURL(name string) string {
	data, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		return "/static/" + name
	}
	sum := sha256.Sum256(data)
	return "/static/" + name + "?v=" + hex.EncodeToString(sum[:4])
}

// excerptLen은 소식에 보여 줄 요약의 최대 글자 수다.
const excerptLen = 120

// excerpt는 요약의 줄을 이어 붙이고 앞부분만 남긴다. 줄였으면 끝에 말줄임표를 붙인다.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= excerptLen {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:excerptLen])) + "…"
}

// coverURL은 교보문고 표지 주소다. ISBN도 바코드도 없는 책은 빈 문자열이다.
func coverURL(b book.Book, width int) string {
	code := b.CoverCode()
	if code == "" {
		return ""
	}
	return kyobo.CoverURL(code, width)
}

// buyURL은 교보문고 상품 페이지 주소다. 상품을 확정하지 못한 책은 교보문고 검색 주소를 쓴다.
func buyURL(b book.Book) string {
	if b.KyoboPID != "" {
		return kyobo.ProductURL(b.KyoboPID)
	}
	q := b.Title
	if len(b.Authors) > 0 {
		q += " " + b.Authors[0]
	}
	return kyobo.SearchURL(q)
}

// Handler는 라우팅을 설정한 http.Handler다.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.timeline)
	mux.HandleFunc("GET /books", s.books)
	mux.HandleFunc("GET /books/{id}", s.book)
	mux.HandleFunc("GET /tags", s.tags)
	mux.HandleFunc("GET /authors", s.authors)
	mux.HandleFunc("GET /privacy", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, "privacy", "layout", map[string]any{"Title": "개인정보 처리방침", "Nav": ""})
	})
	mux.HandleFunc("GET /help", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, "help", "layout", map[string]any{"Title": "도움말", "Nav": "help"})
	})
	s.accountRoutes(mux)
	s.adminRoutes(mux)
	s.bookmarkRoutes(mux)
	s.pwaRoutes(mux)
	// Cloud Run 앞단의 Google Frontend가 정확히 /healthz만 가로채서 자체 404를 돌려주므로 /health도 연다.
	health := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /health", health)
	static := http.FileServerFS(staticFiles)
	staticGzip, err := gzipFiles(staticFiles)
	if err != nil {
		s.log.Warn("정적 파일을 미리 압축하지 못해서 요청마다 압축한다", "오류", err)
	}
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		name := strings.TrimPrefix(r.URL.Path, "/")
		if data, ok := staticGzip[name]; ok && acceptsGzip(r) {
			// Content-Encoding이 붙어 있으므로 compress가 다시 압축하지 않는다. Content-Type은 이름의 확장자로 정한다.
			w.Header().Set("Content-Encoding", "gzip")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
			return
		}
		static.ServeHTTP(w, r)
	}))
	// 비공개 밴드에서 나온 요약을 싣는 사이트라서 검색엔진이 수집하지 않게 한다.
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
	})
	// 다른 사이트에서 보낸 POST 요청(CSRF)을 막는다. 쿠키의 SameSite=Lax에 더해 Sec-Fetch-Site와 Origin을 본다.
	cop := http.NewCrossOriginProtection()
	if s.siteURL != "" {
		if err := cop.AddTrustedOrigin(s.siteURL); err != nil {
			s.log.Warn("SITE_URL을 믿을 수 있는 출처로 더하지 못했다", "오류", err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.status(w, r, http.StatusForbidden, "요청을 처리할 수 없습니다", "다른 사이트에서 보낸 요청은 받지 않습니다.")
	}))
	handler := cop.Handler(s.withAccount(mux))
	return compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		handler.ServeHTTP(w, r)
	}))
}

func page(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

// trimPage는 한 쪽보다 하나 더 읽은 목록을 한 쪽으로 자르고 다음 쪽 번호를 돌려준다. 다음 쪽이 없으면 0이다.
func trimPage[T any](items []T, p int) ([]T, int) {
	if len(items) > pageSize {
		return items[:pageSize], p + 1
	}
	return items, 0
}

// renderList는 HTMX의 '더 보기' 요청이면 이어 붙일 목록 조각(items)만, 아니면 화면 전체를 그린다.
func (s *Server) renderList(w http.ResponseWriter, r *http.Request, page, items string, data map[string]any) {
	if isMore(r) {
		s.render(w, r, page, items, data)
		return
	}
	s.render(w, r, page, "layout", data)
}

// isMore는 목록의 '더 보기'가 다음 쪽을 이어 붙이려고 보낸 HTMX 요청인지 알려 준다.
func isMore(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && strings.HasSuffix(r.Header.Get("HX-Target"), "-more")
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	p := page(r)
	entries, err := s.store.Timeline(r.Context(), pageSize+1, (p-1)*pageSize)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	entries, next := trimPage(entries, p)
	s.renderList(w, r, "timeline", "timeline-items", map[string]any{
		"Title": "최근 소식", "Nav": "timeline", "Entries": entries, "NextPage": next,
	})
}

func (s *Server) books(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.Filter{Tag: q.Get("tag"), Author: q.Get("author"), Query: strings.TrimSpace(q.Get("q"))}
	p := page(r)
	shelf, err := s.store.Books(r.Context(), f, pageSize+1, (p-1)*pageSize)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	shelf, next := trimPage(shelf, p)
	title := "모든 책"
	switch {
	case f.Query != "":
		title = "‘" + f.Query + "’ 검색 결과"
	case f.Tag != "":
		title = "#" + f.Tag
	case f.Author != "":
		title = f.Author + "의 책"
	}
	data := map[string]any{
		"Title": title, "Nav": "books", "Filter": f, "Query": f.Query, "Shelf": shelf, "NextPage": next,
	}
	// 책 수는 제목 옆에만 보이므로 '더 보기'로 이어 붙일 때는 세지 않는다.
	if !isMore(r) {
		total, err := s.store.BookCount(r.Context(), f)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data["Total"] = total
	}
	s.renderList(w, r, "books", "book-cards", data)
}

func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	b, mentions, err := s.store.Book(r.Context(), id)
	// 관리자가 언급을 모두 지운 책은 관리자에게만 보인다.
	if err == nil && len(mentions) == 0 && adminEmail(r) == "" {
		err = store.ErrNotFound
	}
	if s.failed(w, r, err) {
		return
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := map[string]any{
		"Title": b.Title, "Nav": "books", "Book": b, "Tags": tagViews(b.Tags, tags),
		"Count": len(mentions), "Threads": book.Threads(mentions),
	}
	if uid := userID(r); uid != 0 {
		panel, err := s.bookmarkPanel(r.Context(), uid, b.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data["Bookmark"] = panel
	}
	s.render(w, r, "book", "layout", data)
}

// tagView는 화면에 보여 줄 책의 태그다.
type tagView struct {
	Name  string
	Topic bool
}

func tagViews(names []string, tags book.TagList) []tagView {
	views := make([]tagView, len(names))
	for i, name := range names {
		views[i] = tagView{Name: name, Topic: tags.IsTopic(name)}
	}
	return views
}

func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.TagCounts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// 모임 주제는 정한 순서(최근 주제가 먼저)로 앞에 두고, 나머지 태그는 책이 많은 순서로 둔다.
	var topics, others []book.Count
	for _, topic := range tags.Topics() {
		if i := slices.IndexFunc(counts, func(c book.Count) bool { return c.Name == topic }); i >= 0 {
			topics = append(topics, counts[i])
		}
	}
	for _, c := range counts {
		if !tags.IsTopic(c.Name) {
			others = append(others, c)
		}
	}
	s.render(w, r, "tags", "layout", map[string]any{"Title": "태그", "Nav": "tags", "Topics": topics, "Others": others})
}

func (s *Server) authors(w http.ResponseWriter, r *http.Request) {
	counts, err := s.store.AuthorCounts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "authors", "layout", map[string]any{"Title": "저자", "Nav": "authors", "Counts": counts})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page, name string, data map[string]any) {
	data["Admin"] = adminEmail(r)
	data["User"] = userID(r) != 0
	data["LoginEnabled"] = s.login != nil
	data["UserLogin"] = s.subjects != nil
	data["Path"] = r.URL.RequestURI()
	if _, ok := data["Notice"]; !ok {
		data["Notice"] = notices[r.URL.Query().Get("done")]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("화면을 그리지 못했다", "path", r.URL.Path, "오류", err)
	}
}

// renderPage는 상태 코드와 함께 화면 전체를 그린다. 입력 오류를 보이는 화면처럼 200이 아닐 때 쓴다.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, code int, page string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	s.render(w, r, page, "layout", data)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.status(w, r, http.StatusNotFound, "찾을 수 없습니다", "찾는 책이 없습니다.")
}

// status는 상태 코드와 함께 안내 화면을 그린다.
func (s *Server) status(w http.ResponseWriter, r *http.Request, code int, title, message string) {
	s.renderPage(w, r, code, "error", map[string]any{"Title": title, "Message": message})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("요청 처리 실패", "path", r.URL.Path, "오류", err)
	s.status(w, r, http.StatusInternalServerError, "오류", "잠시 후 다시 시도해 주세요.")
}

// failed는 저장소가 돌려준 오류를 화면으로 알리고 참을 돌려준다. 찾는 것이 없으면 404, 그 밖의 오류는 500이다.
// 오류가 없으면 아무것도 그리지 않고 거짓을 돌려준다.
func (s *Server) failed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
	default:
		s.fail(w, r, err)
	}
	return true
}

// ignoreNotFound는 지울 것이 이미 없다는 오류(store.ErrNotFound)를 성공으로 본다. 같은 삭제 요청을 두 번 보내도 실패로 그리지 않는다.
func ignoreNotFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
