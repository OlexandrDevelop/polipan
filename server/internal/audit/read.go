// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stored is a row as it is in the database.
type Stored struct {
	Seq        int64           `json:"seq"`
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id,omitempty"`
	ActorType  string          `json:"actor_type"`
	ActorID    string          `json:"actor_id,omitempty"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Diff       json.RawMessage `json:"diff,omitempty"`
	IP         string          `json:"ip,omitempty"`
	UserAgent  string          `json:"user_agent,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	PrevHash   string          `json:"prev_hash"`
	Hash       string          `json:"hash"`
	CreatedAt  time.Time       `json:"created_at"`

	prev, hash []byte
}

const selectRows = `SELECT seq, id, org_id, actor_type, coalesce(actor_id,''), action, target_type, coalesce(target_id,''),
	coalesce(project_id,''), coalesce(diff::text,'null'), coalesce(host(ip),''), coalesce(user_agent,''), coalesce(request_id,''),
	prev_hash, hash, created_at FROM audit_events`

func scanRow(rows pgx.Rows) (*Stored, error) {
	var s Stored
	var diff string
	if err := rows.Scan(&s.Seq, &s.ID, &s.OrgID, &s.ActorType, &s.ActorID, &s.Action, &s.TargetType, &s.TargetID,
		&s.ProjectID, &diff, &s.IP, &s.UserAgent, &s.RequestID, &s.prev, &s.hash, &s.CreatedAt); err != nil {
		return nil, err
	}
	s.Diff = json.RawMessage(diff)
	s.PrevHash, s.Hash = hex.EncodeToString(s.prev), hex.EncodeToString(s.hash)
	s.CreatedAt = s.CreatedAt.UTC()
	return &s, nil
}

// recompute is the hash the row must have.
func (s *Stored) recompute() []byte {
	diff, err := canonicalJSON(s.Diff)
	if err != nil {
		return nil
	}
	return hashRow(s.prev, row{ID: s.ID, OrgID: s.OrgID, ActorType: s.ActorType, ActorID: s.ActorID, Action: s.Action,
		TargetType: s.TargetType, TargetID: s.TargetID, ProjectID: s.ProjectID, Diff: diff, IP: canonicalIP(s.IP),
		UserAgent: s.UserAgent, RequestID: s.RequestID, CreatedAt: s.CreatedAt.Format(time.RFC3339Nano)})
}

// Filter is the search of the audit log. Empty fields do not filter.
type Filter struct {
	ActorID    string
	Action     string // exact, or a prefix when it ends with *
	TargetType string
	TargetID   string
	ProjectID  string
	From, To   *time.Time
}

func (f Filter) where(orgID string) (string, []any) {
	sql, args := " WHERE org_id = $1", []any{orgID}
	add := func(cond string, v any) {
		args = append(args, v)
		sql += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.ActorID != "" {
		add("actor_id = $%d", f.ActorID)
	}
	if f.Action != "" {
		if n := len(f.Action); f.Action[n-1] == '*' {
			add("action LIKE $%d", escapeLike(f.Action[:n-1])+"%")
		} else {
			add("action = $%d", f.Action)
		}
	}
	if f.TargetType != "" {
		add("target_type = $%d", f.TargetType)
	}
	if f.TargetID != "" {
		add("target_id = $%d", f.TargetID)
	}
	if f.ProjectID != "" {
		add("project_id = $%d", f.ProjectID)
	}
	if f.From != nil {
		add("created_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("created_at < $%d", *f.To)
	}
	return sql, args
}

func escapeLike(s string) string {
	var b bytes.Buffer
	for _, c := range s {
		if c == '%' || c == '_' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Search returns one page, newest first. cursor is the seq of the last row of the previous page (0 for the first page).
// next is 0 when there is no more.
func Search(ctx context.Context, q Querier, orgID string, f Filter, cursor int64, limit int) (items []Stored, next int64, err error) {
	where, args := f.where(orgID)
	if cursor > 0 {
		args = append(args, cursor)
		where += fmt.Sprintf(" AND seq < $%d", len(args))
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, selectRows+where+fmt.Sprintf(" ORDER BY seq DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items = []Stored{}
	for rows.Next() {
		s, err := scanRow(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(items) > limit {
		items = items[:limit]
		next = items[limit-1].Seq
	}
	return items, next, nil
}

// Export calls fn for every row of the time range, oldest first.
func Export(ctx context.Context, q Querier, orgID string, from, to *time.Time, fn func(Stored) error) error {
	where, args := Filter{From: from, To: to}.where(orgID)
	rows, err := q.Query(ctx, selectRows+where+" ORDER BY seq", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanRow(rows)
		if err != nil {
			return err
		}
		if err := fn(*s); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Result is the answer of Verify.
type Result struct {
	OK             bool
	FirstBrokenSeq int64
	CheckedRows    int64
}

// Verify walks the chain of an org and reports the first broken seq. The first row that is left after an
// archive must have the last hash of the newest archive as prev_hash.
func Verify(ctx context.Context, q Querier, orgID string) (Result, error) {
	want := make([]byte, 32)
	err := q.QueryRow(ctx, `SELECT last_hash FROM audit_archives WHERE org_id = $1 ORDER BY last_seq DESC LIMIT 1`, orgID).Scan(&want)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	rows, err := q.Query(ctx, selectRows+` WHERE org_id = $1 ORDER BY seq`, orgID)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	res := Result{OK: true}
	for rows.Next() {
		s, err := scanRow(rows)
		if err != nil {
			return Result{}, err
		}
		res.CheckedRows++
		if !bytes.Equal(s.prev, want) || !bytes.Equal(s.recompute(), s.hash) {
			return Result{OK: false, FirstBrokenSeq: s.Seq, CheckedRows: res.CheckedRows}, nil
		}
		want = s.hash
	}
	return res, rows.Err()
}

// OrgIDs lists the orgs that have audit rows.
func OrgIDs(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT org_id FROM audit_events`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Sign is the HMAC of an archive segment.
func Sign(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// ArchiveResult says what an archive run did.
type ArchiveResult struct {
	ArchiveID string
	Rows      int
}

// Archive moves the rows of an org that are older than cutoff to a signed segment and deletes them.
// It refuses when the rows do not verify: a broken chain must not be hidden. The checkpoint (the last hash)
// is in the archive row, and Write and Verify use it.
func Archive(ctx context.Context, pool *pgxpool.Pool, orgID string, cutoff time.Time, key []byte) (ArchiveResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('audit:' || $1::text, 0))`, orgID); err != nil {
		return ArchiveResult{}, err
	}
	if res, err := Verify(ctx, tx, orgID); err != nil || !res.OK {
		if err == nil {
			err = fmt.Errorf("audit: the chain is broken at seq %d, nothing is archived", res.FirstBrokenSeq)
		}
		return ArchiveResult{}, err
	}
	rows, err := tx.Query(ctx, selectRows+` WHERE org_id = $1 AND created_at < $2 ORDER BY seq`, orgID, cutoff)
	if err != nil {
		return ArchiveResult{}, err
	}
	var all []Stored
	for rows.Next() {
		s, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return ArchiveResult{}, err
		}
		all = append(all, *s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ArchiveResult{}, err
	}
	// Only a block from the start of the chain: stop at the first row that is not old enough.
	var firstKept int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(min(seq), 0) FROM audit_events WHERE org_id = $1 AND created_at >= $2`, orgID, cutoff).Scan(&firstKept); err != nil {
		return ArchiveResult{}, err
	}
	for i, s := range all {
		if firstKept > 0 && s.Seq > firstKept {
			all = all[:i]
			break
		}
	}
	if len(all) == 0 {
		return ArchiveResult{}, nil
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, s := range all {
		if err := enc.Encode(s); err != nil {
			return ArchiveResult{}, err
		}
	}
	if err := zw.Close(); err != nil {
		return ArchiveResult{}, err
	}
	first, last := all[0], all[len(all)-1]
	id := "aar_" + newID()[4:]
	if _, err := tx.Exec(ctx, `INSERT INTO audit_archives (id, org_id, first_seq, last_seq, row_count, prev_hash, last_hash, data, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, orgID, first.Seq, last.Seq, len(all), first.prev, last.hash, buf.Bytes(), Sign(key, buf.Bytes())); err != nil {
		return ArchiveResult{}, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL polipan.audit_archive = 'on'`); err != nil {
		return ArchiveResult{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM audit_events WHERE org_id = $1 AND seq >= $2 AND seq <= $3`, orgID, first.Seq, last.Seq); err != nil {
		return ArchiveResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{ArchiveID: id, Rows: len(all)}, nil
}

// SeqCursor reads a cursor of Search.
func SeqCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 0, errors.New("bad cursor")
	}
	return n, nil
}
