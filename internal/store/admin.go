package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/yonsei-alumni/abc-library/internal/book"
)

// DuplicateError는 고친 책 정보(ISBN이나 제목과 대표 저자)가 다른 책과 겹친다는 뜻이다.
type DuplicateError struct {
	BookID int64
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("같은 책이 이미 있습니다(%d번)", e.BookID)
}

// Tags는 태그 목록을 정해진 순서(모임 주제가 먼저)로 돌려준다.
func (s *Store) Tags(ctx context.Context) (book.TagList, error) {
	var rows []tagRow
	err := s.db.NewRaw(`SELECT g.name, g.topic FROM tags g `+tagOrder).Scan(ctx, &rows)
	return convert(rows, tagRow.toTag), err
}

type tagRow struct {
	Name  string `bun:"name"`
	Topic bool   `bun:"topic"`
}

func (r tagRow) toTag() book.Tag {
	return book.Tag{Name: r.Name, Topic: r.Topic}
}

// tagPosition은 모임 주제(topic)면 모임 주제의 맨 앞, 아니면 그 밖의 태그의 맨 뒤 순서를 계산하는 식이다.
// 인자는 topic을 두 번 받는다.
const tagPosition = `(SELECT CASE WHEN ? THEN coalesce(min(position), 1) - 1 ELSE coalesce(max(position), 0) + 1 END
	FROM tags WHERE topic = ?)`

// AddTag는 태그를 더한다. 모임 주제는 맨 앞에, 그 밖의 태그는 맨 뒤에 둔다. 같은 이름이 있으면 ErrConflict다.
func (s *Store) AddTag(ctx context.Context, t book.Tag) error {
	res, err := s.db.NewRaw(`
		INSERT INTO tags (name, topic, position) VALUES (?, ?, `+tagPosition+`)
		ON CONFLICT DO NOTHING`, t.Name, t.Topic, t.Topic, t.Topic).Exec(ctx)
	return conflictIfNone(res, err)
}

// SetTagTopic은 태그를 모임 주제로 정하거나 모임 주제에서 뺀다. 정하면 모임 주제의 맨 앞에, 빼면 그 밖의 태그의 맨 뒤에 둔다.
// 이미 그렇게 정해져 있으면 순서를 그대로 둔다. 없는 태그면 ErrNotFound다.
func (s *Store) SetTagTopic(ctx context.Context, name string, topic bool) error {
	return notFoundIfNone(s.db.NewRaw(`
		UPDATE tags SET topic = ?, position = CASE WHEN topic = ? THEN position ELSE `+tagPosition+` END
		WHERE name = ?`, topic, topic, topic, topic, name).Exec(ctx))
}

// RenameTag는 태그 이름을 바꾼다. 책에 붙은 태그도 함께 바뀐다.
func (s *Store) RenameTag(ctx context.Context, old, name string) error {
	if old == name {
		return nil
	}
	res, err := s.db.NewRaw(`UPDATE tags SET name = ? WHERE name = ?`, name, old).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return notFoundIfNone(res, err)
}

// MoveTag는 태그를 한 칸 앞(up)이나 뒤로 옮긴다. 모임 주제는 모임 주제끼리, 그 밖의 태그는 그 밖의 태그끼리 옮긴다.
// 이미 끝에 있으면 그대로 둔다.
func (s *Store) MoveTag(ctx context.Context, name string, up bool) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		type tagPos struct {
			Name     string `bun:"name"`
			Position int    `bun:"position"`
		}
		var cur []tagPos
		if err := tx.NewRaw(`SELECT name, position FROM tags WHERE name = ? FOR UPDATE`, name).Scan(ctx, &cur); err != nil {
			return err
		}
		if len(cur) == 0 {
			return ErrNotFound
		}
		cmp, order := ">", "ASC"
		if up {
			cmp, order = "<", "DESC"
		}
		var next []tagPos
		err := tx.NewRaw(`
			SELECT name, position FROM tags
			WHERE topic = (SELECT topic FROM tags WHERE name = ?) AND (position, name) `+cmp+` (?, ?)
			ORDER BY position `+order+`, name `+order+` LIMIT 1 FOR UPDATE`, name, cur[0].Position, name).Scan(ctx, &next)
		if err != nil || len(next) == 0 {
			return err
		}
		pos := cur[0].Position
		if next[0].Position == pos {
			// 순서가 같은 태그끼리는 자리를 바꿔도 순서가 그대로이므로 한 칸 벌린다.
			if up {
				pos++
			} else {
				pos--
			}
		}
		if _, err := tx.NewRaw(`UPDATE tags SET position = ? WHERE name = ?`, next[0].Position, name).Exec(ctx); err != nil {
			return err
		}
		_, err = tx.NewRaw(`UPDATE tags SET position = ? WHERE name = ?`, pos, next[0].Name).Exec(ctx)
		return err
	})
}

// DeleteTag는 태그를 지운다. 책에 붙은 태그도 함께 빠진다.
func (s *Store) DeleteTag(ctx context.Context, name string) error {
	return notFoundIfNone(s.db.NewRaw(`DELETE FROM tags WHERE name = ?`, name).Exec(ctx))
}

// UpdateBook은 관리자가 고친 책 정보와 태그를 저장한다. 그 뒤로 배치는 이 책의 정보와 태그를 바꾸지 않는다.
// 예전 ISBN과 작품 키는 남겨서 배치가 다시 추출해도 이 책을 찾게 한다.
// ISBN이나 작품 키가 다른 책과 겹치면 *DuplicateError다. 목록에 없는 태그는 버린다.
func (s *Store) UpdateBook(ctx context.Context, b book.Book) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var ids []int64
		if err := tx.NewRaw(`SELECT id FROM books WHERE id = ? FOR UPDATE`, b.ID).Scan(ctx, &ids); err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNotFound
		}
		keys := bookKeys(b)
		var others []int64
		err := tx.NewRaw(`
			SELECT id FROM books WHERE id <> ? AND isbn13 = nullif(?, '')
			UNION
			SELECT book_id FROM book_keys WHERE book_id <> ? AND key IN (?)
			LIMIT 1`, b.ID, b.ISBN13, b.ID, bun.List(keys)).Scan(ctx, &others)
		if err != nil {
			return err
		}
		if len(others) > 0 {
			return &DuplicateError{BookID: others[0]}
		}

		_, err = tx.NewUpdate().Model((*bookRow)(nil)).
			Set("title = ?", b.Title).
			Set("authors = ?", pgdialect.Array(nonNil(b.Authors))).
			Set("publisher = ?", b.Publisher).
			Set("isbn13 = nullif(?, '')", b.ISBN13).
			Set("kyobo_pid = nullif(?, '')", b.KyoboPID).
			Set("kyobo_barcode = nullif(?, '')", b.KyoboBarcode).
			Set("edited_at = now()").
			Set("updated_at = now()").
			Where("id = ?", b.ID).
			Exec(ctx)
		if err != nil {
			return err
		}
		if err := addKeys(ctx, tx, b.ID, keys); err != nil {
			return err
		}
		if _, err := tx.NewRaw(`DELETE FROM book_tags WHERE book_id = ?`, b.ID).Exec(ctx); err != nil {
			return err
		}
		return addBookTags(ctx, tx, b.ID, b.Tags)
	})
}

// MergeBook은 책 from을 책 into로 합친다. 언급, 태그, 책 키, 북마크를 옮기고 into에 빠진 ISBN, 상품 번호, 출판사를 채운 뒤 from을 지운다.
func (s *Store) MergeBook(ctx context.Context, from, into int64) error {
	if from == into {
		return errors.New("같은 책끼리는 합칠 수 없습니다")
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var rows []bookRow
		if err := tx.NewSelect().Model(&rows).Where("id IN (?)", bun.List([]int64{from, into})).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if len(rows) != 2 {
			return ErrNotFound
		}
		src := rows[0]
		if src.ID != from {
			src = rows[1]
		}
		for _, q := range []string{
			`UPDATE mentions SET book_id = ? WHERE book_id = ?`,
			`INSERT INTO book_tags (book_id, tag) SELECT ?, tag FROM book_tags WHERE book_id = ? ON CONFLICT DO NOTHING`,
			`UPDATE book_keys SET book_id = ? WHERE book_id = ?`,
			// 두 책을 모두 북마크한 사용자도 있으므로 옮기지 않고 복사한다. from의 것은 책을 지울 때 함께 지워진다.
			`INSERT INTO bookmarks (user_id, book_id, created_at)
			 SELECT user_id, ?, created_at FROM bookmarks WHERE book_id = ? ON CONFLICT DO NOTHING`,
			`INSERT INTO bookmark_category_books (category_id, user_id, book_id, created_at, position)
			 SELECT category_id, user_id, ?, created_at, position FROM bookmark_category_books WHERE book_id = ? ON CONFLICT DO NOTHING`,
		} {
			if _, err := tx.NewRaw(q, into, from).Exec(ctx); err != nil {
				return err
			}
		}
		// from을 먼저 지워야 from의 ISBN을 into에 옮겨도 겹치지 않는다.
		if _, err := tx.NewRaw(`DELETE FROM books WHERE id = ?`, from).Exec(ctx); err != nil {
			return err
		}
		return fillMissingInfo(ctx, tx, into, book.Book{ISBN13: src.ISBN13, KyoboPID: src.KyoboPID, KyoboBarcode: src.Barcode, Publisher: src.Publisher})
	})
}

// DeleteBook은 책의 언급을 모두 지운다. 책은 목록에서 사라지지만 지운 언급은 남겨 두어서,
// 배치가 같은 글을 다시 추출해도 이 책을 되살리지 않는다. 다른 글에서 새로 언급되면 다시 나타난다.
func (s *Store) DeleteBook(ctx context.Context, id int64) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := requireBook(ctx, tx, id); err != nil {
			return err
		}
		_, err := hideMentions(ctx, tx, "book_id = ?", id)
		return err
	})
}

// hideMentions는 조건에 맞는 언급을 지운 언급으로 남긴다. 지운 언급은 화면에서 빠지고, 고친 언급이기도 해서
// 배치가 같은 글을 다시 추출해도 되살리지 않는다.
func hideMentions(ctx context.Context, db bun.IDB, where string, args ...any) (sql.Result, error) {
	return db.NewUpdate().Model((*mentionRow)(nil)).
		Set("hidden = true").
		Set("edited = true").
		Where(where, args...).
		Where("NOT hidden").
		Exec(ctx)
}

// Mention은 지우지 않은 언급 하나다.
func (s *Store) Mention(ctx context.Context, id int64) (book.Mention, error) {
	ms, err := s.visibleMentions(ctx, `m.id = ?`, id)
	if err != nil {
		return book.Mention{}, err
	}
	if len(ms) == 0 {
		return book.Mention{}, ErrNotFound
	}
	return ms[0], nil
}

// UpdateMention은 관리자가 고친 언급의 요약과 링크를 저장한다. 배치가 그 글을 다시 추출해도 바꾸지 않는다.
func (s *Store) UpdateMention(ctx context.Context, id int64, summary string, links []book.Link) error {
	return notFoundIfNone(s.db.NewUpdate().Model((*mentionRow)(nil)).
		Set("summary = ?", summary).
		Set("links = ?", nonNil(links)).
		Set("edited = true").
		Where("id = ? AND NOT hidden", id).
		Exec(ctx))
}

// MoveMention은 언급을 다른 책으로 옮긴다. 원래 언급은 지운 언급으로 남겨서
// 배치가 같은 글을 다시 추출해도 원래 책에 같은 언급을 되살리지 않는다.
func (s *Store) MoveMention(ctx context.Context, id, toBook int64) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var m mentionRow
		err := tx.NewSelect().Model(&m).Where("id = ? AND NOT hidden", id).For("UPDATE").Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if m.BookID == toBook {
			return nil
		}
		if err := requireBook(ctx, tx, toBook); err != nil {
			return err
		}
		moved := m
		moved.ID, moved.BookID, moved.Edited = 0, toBook, true
		if _, err := tx.NewInsert().Model(&moved).Exec(ctx); err != nil {
			return err
		}
		_, err = hideMentions(ctx, tx, "id = ?", id)
		return err
	})
}

// DeleteMention은 언급을 지운다. 지운 언급은 남겨 두어서 배치가 같은 글을 다시 추출해도 되살리지 않는다.
func (s *Store) DeleteMention(ctx context.Context, id int64) error {
	return notFoundIfNone(hideMentions(ctx, s.db, "id = ?", id))
}

// Admin은 관리자다.
type Admin struct {
	bun.BaseModel `bun:"table:admins"`

	Email     string    `bun:",pk"`
	AddedBy   string    `bun:",notnull"`
	CreatedAt time.Time `bun:",nullzero,notnull,default:current_timestamp"`
}

// Admins는 관리자 목록이다.
func (s *Store) Admins(ctx context.Context) ([]Admin, error) {
	var admins []Admin
	err := s.db.NewSelect().Model(&admins).Order("created_at", "email").Scan(ctx)
	return admins, err
}

// IsAdmin은 그 메일 주소가 관리자인지 알려 준다. 대소문자는 가리지 않는다.
func (s *Store) IsAdmin(ctx context.Context, email string) (bool, error) {
	return s.db.NewSelect().Model((*Admin)(nil)).Where("email = ?", strings.ToLower(email)).Exists(ctx)
}

// AddAdmin은 관리자를 더한다. 이미 있으면 ErrConflict다.
func (s *Store) AddAdmin(ctx context.Context, email, addedBy string) error {
	return conflictIfNone(s.db.NewInsert().Model(&Admin{Email: strings.ToLower(email), AddedBy: addedBy}).On("CONFLICT DO NOTHING").Exec(ctx))
}

// DeleteAdmin은 관리자를 지운다. 마지막 관리자는 지우지 않는다.
func (s *Store) DeleteAdmin(ctx context.Context, email string) error {
	return notFoundIfNone(s.db.NewRaw(`DELETE FROM admins WHERE email = ? AND (SELECT count(*) FROM admins) > 1`, strings.ToLower(email)).Exec(ctx))
}
