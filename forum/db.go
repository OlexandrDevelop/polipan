// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrate applies the numbered SQL files that are not applied yet. One lock, one transaction per file.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(7203001)`); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(7203001)`) }()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %s", name)
		}
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sqlText, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

type User struct {
	ID       int64
	Username string
	Role     string
	Banned   bool
	Created  time.Time
}

func (u *User) IsStaff() bool { return u != nil && (u.Role == "moderator" || u.Role == "admin") }

type Category struct {
	ID          int
	Slug        string
	Name        string
	Description string
	StaffOnly   bool
	Topics      int
	LastPostAt  *time.Time
}

type TopicRow struct {
	ID           int64
	Title        string
	CategoryID   int
	CategorySlug string
	CategoryName string
	Author       string
	CreatedAt    time.Time
	LastPostAt   time.Time
	Replies      int
	Pinned       bool
	Locked       bool
	Deleted      bool
}

type PostRow struct {
	ID        int64
	TopicID   int64
	UserID    int64
	Author    string
	Role      string
	Body      string
	CreatedAt time.Time
	EditedAt  *time.Time
	Deleted   bool
	First     bool
	CanEdit   bool
	CanDelete bool
}

type SearchRow struct {
	TopicID  int64
	Title    string
	Category string
	When     time.Time
	Snippet  string
}

const topicSelect = `
SELECT t.id, t.title, t.category_id, c.slug, c.name, u.username, t.created_at, t.last_post_at,
       t.reply_count, t.pinned, t.locked, t.deleted
FROM topics t
JOIN categories c ON c.id = t.category_id
JOIN users u ON u.id = t.user_id`

func scanTopic(row pgx.Row) (TopicRow, error) {
	var t TopicRow
	err := row.Scan(&t.ID, &t.Title, &t.CategoryID, &t.CategorySlug, &t.CategoryName, &t.Author, &t.CreatedAt,
		&t.LastPostAt, &t.Replies, &t.Pinned, &t.Locked, &t.Deleted)
	return t, err
}

func (a *App) categories(ctx context.Context) ([]Category, error) {
	rows, err := a.db.Query(ctx, `
SELECT c.id, c.slug, c.name, c.description, c.staff_only,
       (SELECT count(*) FROM topics t WHERE t.category_id = c.id AND NOT t.deleted),
       (SELECT max(t.last_post_at) FROM topics t WHERE t.category_id = c.id AND NOT t.deleted)
FROM categories c ORDER BY c.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.StaffOnly, &c.Topics, &c.LastPostAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *App) categoryBySlug(ctx context.Context, slug string) (*Category, error) {
	var c Category
	err := a.db.QueryRow(ctx, `SELECT id, slug, name, description, staff_only FROM categories WHERE slug = $1`, slug).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.StaffOnly)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (a *App) topicList(ctx context.Context, where string, args []any, limit, offset int) ([]TopicRow, error) {
	args = append(args, limit, offset)
	query := topicSelect + ` WHERE NOT t.deleted ` + where +
		fmt.Sprintf(` ORDER BY t.pinned DESC, t.last_post_at DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicRow
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (a *App) topicByID(ctx context.Context, id int64) (*TopicRow, error) {
	t, err := scanTopic(a.db.QueryRow(ctx, topicSelect+` WHERE t.id = $1`, id))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (a *App) posts(ctx context.Context, topicID int64, limit, offset int, viewer *User, now time.Time) ([]PostRow, error) {
	rows, err := a.db.Query(ctx, `
SELECT p.id, p.topic_id, p.user_id, u.username, u.role, p.body, p.created_at, p.edited_at, p.deleted,
       p.id = (SELECT min(id) FROM posts WHERE topic_id = p.topic_id)
FROM posts p JOIN users u ON u.id = p.user_id
WHERE p.topic_id = $1 ORDER BY p.id LIMIT $2 OFFSET $3`, topicID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PostRow
	for rows.Next() {
		var p PostRow
		if err := rows.Scan(&p.ID, &p.TopicID, &p.UserID, &p.Author, &p.Role, &p.Body, &p.CreatedAt, &p.EditedAt, &p.Deleted, &p.First); err != nil {
			return nil, err
		}
		if p.Deleted {
			p.Body = ""
		}
		p.CanEdit, p.CanDelete = canChange(viewer, p, now)
		out = append(out, p)
	}
	return out, rows.Err()
}

const editWindow = 30 * time.Minute

// canChange says who may edit or delete a post. The author may do it in the first 30 minutes
// and may not delete the first post of a topic. Staff may always do both.
func canChange(viewer *User, p PostRow, now time.Time) (edit, del bool) {
	if viewer == nil || viewer.Banned || p.Deleted {
		return false, false
	}
	if viewer.IsStaff() {
		return true, true
	}
	if viewer.ID == p.UserID && now.Sub(p.CreatedAt) <= editWindow {
		return true, !p.First
	}
	return false, false
}

func (a *App) postByID(ctx context.Context, id int64) (PostRow, error) {
	var p PostRow
	err := a.db.QueryRow(ctx, `
SELECT p.id, p.topic_id, p.user_id, u.username, u.role, p.body, p.created_at, p.edited_at, p.deleted,
       p.id = (SELECT min(id) FROM posts WHERE topic_id = p.topic_id)
FROM posts p JOIN users u ON u.id = p.user_id WHERE p.id = $1`, id).
		Scan(&p.ID, &p.TopicID, &p.UserID, &p.Author, &p.Role, &p.Body, &p.CreatedAt, &p.EditedAt, &p.Deleted, &p.First)
	return p, err
}

func (a *App) countTopics(ctx context.Context, where string, args []any) (int, error) {
	var n int
	err := a.db.QueryRow(ctx, `SELECT count(*) FROM topics t WHERE NOT t.deleted `+where, args...).Scan(&n)
	return n, err
}

func (a *App) createTopic(ctx context.Context, categoryID int, userID int64, title, body string) (int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO topics (category_id, user_id, title) VALUES ($1, $2, $3) RETURNING id`,
		categoryID, userID, strings.TrimSpace(title)).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO posts (topic_id, user_id, body) VALUES ($1, $2, $3)`, id, userID, strings.TrimSpace(body)); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (a *App) createReply(ctx context.Context, topicID, userID int64, body string) (int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO posts (topic_id, user_id, body) VALUES ($1, $2, $3) RETURNING id`,
		topicID, userID, strings.TrimSpace(body)).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE topics SET reply_count = reply_count + 1, last_post_at = now() WHERE id = $1`, topicID); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (a *App) deletePost(ctx context.Context, p PostRow) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if p.First {
		if _, err := tx.Exec(ctx, `UPDATE topics SET deleted = true WHERE id = $1`, p.TopicID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE posts SET deleted = true WHERE id = $1 AND NOT deleted`, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE topics SET reply_count = greatest(reply_count - 1, 0) WHERE id = $1`, p.TopicID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (a *App) search(ctx context.Context, q string, limit int) ([]SearchRow, error) {
	rows, err := a.db.Query(ctx, `
WITH hits AS (
  SELECT t.id AS topic_id, t.title, c.name AS category, t.last_post_at AS at, ''::text AS snippet
  FROM topics t JOIN categories c ON c.id = t.category_id
  WHERE NOT t.deleted AND to_tsvector('simple', t.title) @@ plainto_tsquery('simple', $1)
  UNION ALL
  SELECT t.id, t.title, c.name, p.created_at,
         ts_headline('simple', p.body, plainto_tsquery('simple', $1), 'StartSel=«, StopSel=», MaxFragments=1, MaxWords=24, MinWords=8')
  FROM posts p JOIN topics t ON t.id = p.topic_id JOIN categories c ON c.id = t.category_id
  WHERE NOT t.deleted AND NOT p.deleted AND to_tsvector('simple', p.body) @@ plainto_tsquery('simple', $1)
)
SELECT DISTINCT ON (topic_id) topic_id, title, category, at, snippet FROM hits ORDER BY topic_id, at DESC`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchRow
	for rows.Next() {
		var r SearchRow
		if err := rows.Scan(&r.TopicID, &r.Title, &r.Category, &r.When, &r.Snippet); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.After(out[j].When) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, rows.Err()
}
