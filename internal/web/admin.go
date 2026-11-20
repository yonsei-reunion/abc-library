package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yonsei-alumni/abc-library/internal/book"
	"github.com/yonsei-alumni/abc-library/internal/store"
)

const (
	sessionTTL = 30 * 24 * time.Hour
	loginTTL   = 10 * time.Minute
	maxForm    = 64 << 10
	maxSummary = 3000
)

type adminKey struct{}

// adminEmail은 요청한 사람이 관리자이면 그 메일 주소를, 아니면 빈 문자열을 돌려준다.
func adminEmail(r *http.Request) string {
	email, _ := r.Context().Value(adminKey{}).(string)
	return email
}

// requireAdmin은 관리자만 h를 쓰게 한다. 로그인하지 않았으면 GET 요청은 로그인 화면으로 보낸다.
func (s *Server) requireAdmin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		switch {
		case s.login == nil:
			s.status(w, r, http.StatusNotFound, "찾을 수 없습니다", "관리자 로그인이 설정되지 않았습니다.")
		case adminEmail(r) != "":
			r.Body = http.MaxBytesReader(w, r.Body, maxForm)
			h(w, r)
		case r.Method == http.MethodGet:
			http.Redirect(w, r, "/login?admin=1&next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		default:
			s.status(w, r, http.StatusForbidden, "권한이 없습니다", "관리자만 할 수 있습니다.")
		}
	})
}

func (s *Server) adminRoutes(mux *http.ServeMux) {
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAdmin(h)) }
	admin("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/tags", http.StatusSeeOther)
	})
	admin("GET /admin/tags", func(w http.ResponseWriter, r *http.Request) { s.renderTags(w, r, http.StatusOK, "") })
	admin("POST /admin/tags", s.addTag)
	admin("POST /admin/tags/topic", s.setTagTopic)
	admin("POST /admin/tags/rename", s.renameTag)
	admin("POST /admin/tags/move", s.moveTag)
	admin("POST /admin/tags/delete", s.deleteTag)
	admin("GET /admin/books/{id}", s.adminBook)
	admin("POST /admin/books/{id}", s.saveBook)
	admin("POST /admin/books/{id}/merge", s.mergeBook)
	admin("POST /admin/books/{id}/delete", s.deleteBook)
	admin("GET /admin/mentions/{id}", s.adminMention)
	admin("POST /admin/mentions/{id}", s.saveMention)
	admin("POST /admin/mentions/{id}/move", s.moveMention)
	admin("POST /admin/mentions/{id}/delete", s.deleteMention)
	admin("GET /admin/admins", func(w http.ResponseWriter, r *http.Request) { s.renderAdmins(w, r, http.StatusOK, "") })
	admin("POST /admin/admins", s.addAdmin)
	admin("POST /admin/admins/delete", s.deleteAdmin)
}

// notices는 고친 뒤 돌아온 화면에 보여 줄 안내다. 주소의 done 값으로 고른다.
var notices = map[string]string{
	"tag-added":       "태그를 더했습니다.",
	"tag-topic-on":    "모임 주제로 정했습니다. 모임 주제의 맨 앞으로 옮겼습니다.",
	"tag-topic-off":   "모임 주제에서 뺐습니다. 목록의 맨 뒤로 옮겼습니다.",
	"tag-renamed":     "태그 이름을 바꿨습니다. 책에 붙은 태그도 함께 바뀌었습니다.",
	"tag-moved":       "태그 순서를 바꿨습니다.",
	"tag-deleted":     "태그를 지웠습니다. 책에 붙은 태그도 함께 빠졌습니다.",
	"book-saved":      "책 정보를 저장했습니다.",
	"book-merged":     "책을 합쳤습니다.",
	"book-deleted":    "책의 언급을 모두 지웠습니다. 이 책은 목록에서 사라지고 관리자에게만 보입니다.",
	"mention-saved":   "언급을 저장했습니다.",
	"mention-moved":   "언급을 이 책으로 옮겼습니다.",
	"mention-deleted": "언급을 지웠습니다.",
	"admin-added":     "관리자를 더했습니다.",
	"admin-deleted":   "관리자를 지웠습니다.",

	"bookmark-saved":       "북마크를 저장했습니다.",
	"bookmark-saved-alone": "북마크를 저장했습니다. 고른 카테고리가 없어서 '카테고리 없음'으로 표시됩니다.",
	"bookmark-deleted":     "북마크를 뺐습니다.",
	"category-added":       "카테고리를 만들었습니다. 본문과 공개 여부를 정해 주세요.",
	"category-saved":       "카테고리를 저장했습니다.",
	"category-deleted":     "카테고리를 지웠습니다. 넣어 둔 북마크는 남아 있습니다.",
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, code int, page string, data map[string]any) {
	data["Nav"] = "admin"
	if _, ok := data["AdminNav"]; !ok {
		data["AdminNav"] = ""
	}
	s.renderPage(w, r, code, page, data)
}

// done은 고친 뒤 path의 화면으로 돌아가서 notices의 안내를 보이게 한다.
func done(w http.ResponseWriter, r *http.Request, path, notice string) {
	http.Redirect(w, r, path+"?done="+notice, http.StatusSeeOther)
}

// adminBookPath는 책 편집 화면의 주소다.
func adminBookPath(id int64) string {
	return fmt.Sprintf("/admin/books/%d", id)
}

// 태그

type tagRow struct {
	Name  string
	Topic bool
	Books int
}

func (s *Server) renderTags(w http.ResponseWriter, r *http.Request, code int, errMsg string) {
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts, err := s.store.TagCounts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	books := map[string]int{}
	for _, c := range counts {
		books[c.Name] = c.Books
	}
	rows := make([]tagRow, len(tags))
	for i, t := range tags {
		rows[i] = tagRow{Name: t.Name, Topic: t.Topic, Books: books[t.Name]}
	}
	s.renderAdmin(w, r, code, "admin-tags", map[string]any{
		"Title": "태그 관리", "AdminNav": "tags", "Tags": rows, "Error": errMsg,
	})
}

func tagError(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict, "같은 이름의 태그가 이미 있습니다."
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "없는 태그입니다."
	}
	return 0, ""
}

// tagAction은 태그를 고친 결과에 따라 오류를 보이거나 태그 화면으로 돌아간다.
func (s *Server) tagAction(w http.ResponseWriter, r *http.Request, err error, notice string) {
	if code, msg := tagError(err); code != 0 {
		s.renderTags(w, r, code, msg)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	done(w, r, "/admin/tags", notice)
}

func (s *Server) addTag(w http.ResponseWriter, r *http.Request) {
	name, err := book.CleanTagName(r.PostFormValue("name"))
	if err != nil {
		s.renderTags(w, r, http.StatusBadRequest, err.Error()+".")
		return
	}
	s.tagAction(w, r, s.store.AddTag(r.Context(), book.Tag{Name: name, Topic: r.PostFormValue("topic") == "1"}), "tag-added")
}

func (s *Server) setTagTopic(w http.ResponseWriter, r *http.Request) {
	topic := r.PostFormValue("topic") == "1"
	notice := "tag-topic-off"
	if topic {
		notice = "tag-topic-on"
	}
	s.tagAction(w, r, s.store.SetTagTopic(r.Context(), r.PostFormValue("name"), topic), notice)
}

// ensureTag는 책 편집 화면에서 적은 태그가 목록에 없으면 더하고, 목록에 적힌 이름을 돌려준다.
// 대소문자만 다른 태그가 이미 있으면 모임 주제인지와 상관없이 그 태그를 쓴다.
func (s *Server) ensureTag(r *http.Request, t book.Tag) (string, error) {
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		return "", err
	}
	if name, ok := tags.Same(t.Name); ok {
		return name, nil
	}
	// 그사이 다른 관리자가 같은 이름을 더했으면 그 태그를 쓴다.
	if err := s.store.AddTag(r.Context(), t); err != nil && !errors.Is(err, store.ErrConflict) {
		return "", err
	}
	return t.Name, nil
}

func (s *Server) renameTag(w http.ResponseWriter, r *http.Request) {
	name, err := book.CleanTagName(r.PostFormValue("new_name"))
	if err != nil {
		s.renderTags(w, r, http.StatusBadRequest, err.Error()+".")
		return
	}
	s.tagAction(w, r, s.store.RenameTag(r.Context(), r.PostFormValue("name"), name), "tag-renamed")
}

func (s *Server) moveTag(w http.ResponseWriter, r *http.Request) {
	s.tagAction(w, r, s.store.MoveTag(r.Context(), r.PostFormValue("name"), r.PostFormValue("dir") == "up"), "tag-moved")
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	s.tagAction(w, r, s.store.DeleteTag(r.Context(), r.PostFormValue("name")), "tag-deleted")
}

// 책

type tagOption struct {
	Name    string
	Checked bool
}

type bookForm struct {
	Book    book.Book
	Error   string
	DupID   int64
	MergeTo string
	// NewTag와 NewTagTopic은 태그 목록에 없어서 새로 더하며 붙일 태그의 이름과 모임 주제인지다.
	NewTag      string
	NewTagTopic bool
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) adminBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	b, _, err := s.store.Book(r.Context(), id)
	if s.failed(w, r, err) {
		return
	}
	s.renderBook(w, r, http.StatusOK, bookForm{Book: b})
}

// renderBook은 책 편집 화면을 그린다. 입력에 오류가 있으면 고친 값을 그대로 두고 오류를 보인다.
func (s *Server) renderBook(w http.ResponseWriter, r *http.Request, code int, f bookForm) {
	stored, mentions, err := s.store.Book(r.Context(), f.Book.ID)
	if s.failed(w, r, err) {
		return
	}
	tags, err := s.store.Tags(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	checked := map[string]bool{}
	for _, t := range f.Book.Tags {
		checked[t] = true
	}
	var topics, others []tagOption
	for _, t := range tags {
		opt := tagOption{Name: t.Name, Checked: checked[t.Name]}
		if t.Topic {
			topics = append(topics, opt)
		} else {
			others = append(others, opt)
		}
	}
	s.renderAdmin(w, r, code, "admin-book", map[string]any{
		"Title": stored.Title + " 편집", "Stored": stored, "Form": f, "Topics": topics, "Others": others,
		"Mentions": mentions, "Error": f.Error,
	})
}

func splitAuthors(s string) []string {
	var authors []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			authors = append(authors, a)
		}
	}
	return authors
}

func (s *Server) saveBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	b := book.Book{
		ID:        id,
		Title:     strings.TrimSpace(r.PostFormValue("title")),
		Authors:   splitAuthors(r.PostFormValue("authors")),
		Publisher: strings.TrimSpace(r.PostFormValue("publisher")),
		ISBN13:    book.CleanISBN(r.PostFormValue("isbn13")),
		KyoboPID:  strings.TrimSpace(r.PostFormValue("kyobo_pid")),
		Tags:      r.PostForm["tag"],
	}
	f := bookForm{Book: b, NewTag: r.PostFormValue("new_tag"), NewTagTopic: r.PostFormValue("new_tag_topic") == "1"}
	var newTag string
	var tagErr error
	if strings.TrimSpace(f.NewTag) != "" {
		newTag, tagErr = book.CleanTagName(f.NewTag)
	}
	switch {
	case b.Title == "":
		f.Error = "제목을 입력해 주세요."
	case b.ISBN13 != "" && !book.ValidISBN13(b.ISBN13):
		f.Error = "ISBN-13이 올바르지 않습니다. 978이나 979로 시작하는 13자리 숫자를 입력해 주세요."
	case tagErr != nil:
		f.Error = tagErr.Error() + "."
	}
	if f.Error != "" {
		s.renderBook(w, r, http.StatusBadRequest, f)
		return
	}
	// 교보문고 표지는 상품 번호가 아니라 바코드로 찾는다. ISBN이 없는 상품과, ISBN이 있어도 바코드가 다른 전자책은
	// 상품 번호로 바코드를 찾아 둔다.
	if b.KyoboPID != "" && (b.ISBN13 == "" || strings.HasPrefix(b.KyoboPID, "E")) && s.products != nil {
		ed, found, err := s.products.Product(r.Context(), b.KyoboPID)
		switch {
		case err != nil:
			s.log.Warn("교보문고 상품을 찾지 못했다", "상품 번호", b.KyoboPID, "오류", err)
			f.Error = "교보문고에 연결하지 못했습니다. 잠시 뒤 다시 저장해 주세요."
			s.renderBook(w, r, http.StatusBadGateway, f)
			return
		case !found:
			f.Error = "교보문고에서 상품 번호 " + b.KyoboPID + "를 찾지 못했습니다. 상품 페이지 주소의 /detail/ 뒤 번호를 확인해 주세요."
			s.renderBook(w, r, http.StatusBadRequest, f)
			return
		}
		if ed.Barcode != b.ISBN13 {
			b.KyoboBarcode = ed.Barcode
		}
	}
	if newTag != "" {
		name, err := s.ensureTag(r, book.Tag{Name: newTag, Topic: f.NewTagTopic})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !slices.Contains(b.Tags, name) {
			b.Tags = append(b.Tags, name)
		}
		// 태그는 이미 목록에 들어갔으므로, 아래에서 저장에 실패해 화면을 다시 그려도 목록에서 체크된 채로 보인다.
		f.Book.Tags, f.NewTag = b.Tags, ""
	}
	err := s.store.UpdateBook(r.Context(), b)
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		f.Error, f.DupID = "같은 ISBN이나 같은 제목과 대표 저자의 책이 이미 있습니다. 같은 책이면 아래에서 그 책으로 합쳐 주세요.", dup.BookID
		f.MergeTo = strconv.FormatInt(dup.BookID, 10)
		s.renderBook(w, r, http.StatusConflict, f)
		return
	}
	if s.failed(w, r, err) {
		return
	}
	done(w, r, adminBookPath(id), "book-saved")
}

func formID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue(name)), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) mergeBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	stored, _, err := s.store.Book(r.Context(), id)
	if s.failed(w, r, err) {
		return
	}
	f := bookForm{Book: stored, MergeTo: r.PostFormValue("into")}
	into, ok := formID(r, "into")
	switch {
	case !ok:
		f.Error = "합칠 책의 번호를 입력해 주세요."
	case into == id:
		f.Error = "같은 책끼리는 합칠 수 없습니다."
	}
	if f.Error != "" {
		s.renderBook(w, r, http.StatusBadRequest, f)
		return
	}
	err = s.store.MergeBook(r.Context(), id, into)
	switch {
	case errors.Is(err, store.ErrNotFound):
		f.Error = fmt.Sprintf("%d번 책이 없습니다.", into)
		s.renderBook(w, r, http.StatusNotFound, f)
	case err != nil:
		s.fail(w, r, err)
	default:
		done(w, r, adminBookPath(into), "book-merged")
	}
}

func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	if s.failed(w, r, s.store.DeleteBook(r.Context(), id)) {
		return
	}
	done(w, r, adminBookPath(id), "book-deleted")
}

// 언급

type mentionForm struct {
	Summary string
	Links   string
	MoveTo  string
}

// formatLinks는 링크를 한 줄에 하나씩 '주소 제목' 꼴로 적는다.
func formatLinks(links []book.Link) string {
	var b strings.Builder
	for _, l := range links {
		b.WriteString(l.URL)
		if l.Title != "" {
			b.WriteString(" " + l.Title)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// parseLinks는 한 줄에 하나씩 '주소 제목' 꼴로 적은 링크를 읽는다. 제목이 없으면 주소의 호스트를 제목으로 쓴다.
func parseLinks(s string) ([]book.Link, error) {
	var links []book.Link
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		raw, title, _ := strings.Cut(line, " ")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("%d번째 줄의 링크 주소가 올바르지 않습니다. http나 https로 시작해야 합니다", i+1)
		}
		title = strings.TrimSpace(title)
		if title == "" {
			title = u.Host
		}
		links = append(links, book.Link{URL: raw, Title: title})
	}
	return links, nil
}

// mention은 경로의 언급을 읽는다. 없으면 404 화면을 그리고 ok가 거짓이다.
func (s *Server) mention(w http.ResponseWriter, r *http.Request) (book.Mention, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return book.Mention{}, false
	}
	m, err := s.store.Mention(r.Context(), id)
	if s.failed(w, r, err) {
		return book.Mention{}, false
	}
	return m, true
}

func (s *Server) adminMention(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mention(w, r)
	if !ok {
		return
	}
	s.renderMention(w, r, http.StatusOK, m, mentionForm{Summary: m.Summary, Links: formatLinks(m.Links)}, "")
}

func (s *Server) renderMention(w http.ResponseWriter, r *http.Request, code int, m book.Mention, f mentionForm, errMsg string) {
	b, _, err := s.store.Book(r.Context(), m.BookID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderAdmin(w, r, code, "admin-mention", map[string]any{
		"Title": "언급 편집", "Book": b, "Mention": m, "Form": f, "Error": errMsg,
	})
}

func (s *Server) saveMention(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mention(w, r)
	if !ok {
		return
	}
	f := mentionForm{Summary: strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("summary"), "\r\n", "\n")), Links: r.PostFormValue("links")}
	links, err := parseLinks(strings.ReplaceAll(f.Links, "\r\n", "\n"))
	switch {
	case f.Summary == "":
		err = errors.New("요약을 입력해 주세요")
	case utf8.RuneCountInString(f.Summary) > maxSummary:
		err = fmt.Errorf("요약은 %d자까지 쓸 수 있습니다", maxSummary)
	}
	if err != nil {
		s.renderMention(w, r, http.StatusBadRequest, m, f, err.Error()+".")
		return
	}
	if err := s.store.UpdateMention(r.Context(), m.ID, f.Summary, links); err != nil {
		s.fail(w, r, err)
		return
	}
	done(w, r, adminBookPath(m.BookID), "mention-saved")
}

func (s *Server) moveMention(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mention(w, r)
	if !ok {
		return
	}
	f := mentionForm{Summary: m.Summary, Links: formatLinks(m.Links), MoveTo: r.PostFormValue("book")}
	to, ok := formID(r, "book")
	if !ok {
		s.renderMention(w, r, http.StatusBadRequest, m, f, "옮길 책의 번호를 입력해 주세요.")
		return
	}
	err := s.store.MoveMention(r.Context(), m.ID, to)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderMention(w, r, http.StatusNotFound, m, f, fmt.Sprintf("%d번 책이 없습니다.", to))
	case err != nil:
		s.fail(w, r, err)
	default:
		done(w, r, adminBookPath(to), "mention-moved")
	}
}

func (s *Server) deleteMention(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mention(w, r)
	if !ok {
		return
	}
	if err := ignoreNotFound(s.store.DeleteMention(r.Context(), m.ID)); err != nil {
		s.fail(w, r, err)
		return
	}
	done(w, r, adminBookPath(m.BookID), "mention-deleted")
}

// 관리자

func (s *Server) renderAdmins(w http.ResponseWriter, r *http.Request, code int, errMsg string) {
	admins, err := s.store.Admins(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderAdmin(w, r, code, "admin-admins", map[string]any{
		"Title": "관리자", "AdminNav": "admins", "Admins": admins, "Error": errMsg,
	})
}

// cleanEmail은 메일 주소 하나만 받아서 소문자로 바꾼다. 이름이 붙은 주소('이름 <주소>')는 받지 않는다.
func cleanEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return "", errors.New("메일 주소가 올바르지 않습니다")
	}
	return strings.ToLower(s), nil
}

func (s *Server) addAdmin(w http.ResponseWriter, r *http.Request) {
	email, err := cleanEmail(r.PostFormValue("email"))
	if err != nil {
		s.renderAdmins(w, r, http.StatusBadRequest, err.Error()+".")
		return
	}
	err = s.store.AddAdmin(r.Context(), email, adminEmail(r))
	switch {
	case errors.Is(err, store.ErrConflict):
		s.renderAdmins(w, r, http.StatusConflict, "이미 관리자입니다.")
	case err != nil:
		s.fail(w, r, err)
	default:
		done(w, r, "/admin/admins", "admin-added")
	}
}

func (s *Server) deleteAdmin(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	if email == strings.ToLower(adminEmail(r)) {
		s.renderAdmins(w, r, http.StatusBadRequest, "자기 자신은 관리자에서 뺄 수 없습니다.")
		return
	}
	err := s.store.DeleteAdmin(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.renderAdmins(w, r, http.StatusNotFound, "없는 관리자입니다.")
	case err != nil:
		s.fail(w, r, err)
	default:
		done(w, r, "/admin/admins", "admin-deleted")
	}
}
