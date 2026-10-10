// SPDX-License-Identifier: Apache-2.0

// Package store has the SQL of the HTTP API: users, sessions, the hierarchy
// (projects, environments, services), domains, deployments and nodes (read only).
// Every query that reads an object of an org is scoped by org_id (REQ-SEC-106).
package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors of the store.
var (
	ErrNotFound    = errors.New("store: not found")
	ErrSetupClosed = errors.New("store: setup is closed, a user exists")
)

// Store is the data access of the API.
type Store struct {
	Pool *pgxpool.Pool
}

// New creates a store.
func New(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID makes an id "<prefix>_<ULID>": 10 characters of time (ms) and 16 random characters.
func NewID(prefix string) string {
	out := make([]byte, 26)
	ms := uint64(time.Now().UnixMilli())
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms&31]
		ms >>= 5
	}
	var r [10]byte
	_, _ = rand.Read(r[:])
	// 80 random bits are 16 groups of 5 bits.
	var acc uint64
	for _, v := range r[:5] {
		acc = acc<<8 | uint64(v)
	}
	for i := 17; i >= 10; i-- {
		out[i] = crockford[acc&31]
		acc >>= 5
	}
	acc = 0
	for _, v := range r[5:] {
		acc = acc<<8 | uint64(v)
	}
	for i := 25; i >= 18; i-- {
		out[i] = crockford[acc&31]
		acc >>= 5
	}
	return prefix + "_" + string(out)
}

// IsUnique tells if the error is a unique violation. The name of the constraint is in the second value.
func IsUnique(err error) (bool, string) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return true, pg.ConstraintName
	}
	return false, ""
}

// Cursor is the position after the last item of a list page.
type Cursor struct {
	Time time.Time
	ID   string
}

// Encode makes the opaque cursor string.
func (c Cursor) Encode() string {
	return c.Time.UTC().Format(time.RFC3339Nano) + "|" + c.ID
}

// DecodeCursor reads a cursor string. An empty string gives nil.
func DecodeCursor(s string) (*Cursor, error) {
	if s == "" {
		return nil, nil
	}
	ts, id, ok := strings.Cut(s, "|")
	if !ok || id == "" {
		return nil, errors.New("bad cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errors.New("bad cursor")
	}
	return &Cursor{Time: t, ID: id}, nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func jsonOrEmpty(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// Org is the organization.
type Org struct {
	ID          string
	Name        string
	Slug        string
	SSORequired bool
	Settings    json.RawMessage
}

// GetOrg reads the org.
func (s *Store) GetOrg(ctx context.Context, orgID string) (*Org, error) {
	var o Org
	err := s.Pool.QueryRow(ctx,
		`SELECT id, name, slug, sso_required, settings FROM orgs WHERE id = $1 AND deleted_at IS NULL`, orgID).
		Scan(&o.ID, &o.Name, &o.Slug, &o.SSORequired, &o.Settings)
	if err != nil {
		return nil, notFound(err)
	}
	return &o, nil
}

// User is a user with the org role.
type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash *string
	TotpEnabled  bool
	Disabled     bool
	CreatedAt    time.Time
	OrgID        string
	Role         string
	// Require2FA is the org policy: the user must set up two-factor login.
	Require2FA bool
}

const userSelect = `SELECT u.id, u.email, u.name, u.password_hash, u.totp_enabled, u.disabled, u.created_at, m.org_id, m.role
	FROM users u JOIN memberships m ON m.user_id = u.id`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.TotpEnabled, &u.Disabled, &u.CreatedAt, &u.OrgID, &u.Role); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// UserByEmail finds a user by email (case does not matter, the column is citext).
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, userSelect+` WHERE u.email = $1 AND u.deleted_at IS NULL LIMIT 1`, email))
}

// UserByID finds a user by id.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx, userSelect+` WHERE u.id = $1 AND u.deleted_at IS NULL LIMIT 1`, id))
}

// CreateFirstUser makes the org, the owner and the membership in one transaction.
// It returns ErrSetupClosed when a user exists. A lock makes two calls at once safe.
func (s *Store) CreateFirstUser(ctx context.Context, orgName, orgSlug, email, name, passwordHash string) (*User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7301001)`); err != nil {
		return nil, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrSetupClosed
	}
	// The single org may exist already (the join-token command makes it). Use it, or create it.
	userID := NewID("usr")
	var orgID string
	err = tx.QueryRow(ctx, `SELECT id FROM orgs WHERE deleted_at IS NULL ORDER BY created_at, id LIMIT 1`).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		orgID = NewID("org")
		_, err = tx.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)`, orgID, orgName, orgSlug)
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, $3, $4)`,
		userID, email, name, passwordHash); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		NewID("mem"), orgID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// SetLastLogin stores the time of a good login.
func (s *Store) SetLastLogin(ctx context.Context, userID string, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, userID, at)
	return err
}

// UpdateUserName changes the name of the user.
func (s *Store) UpdateUserName(ctx context.Context, userID, name string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET name = $2, updated_at = now() WHERE id = $1`, userID, name)
	return err
}

// SetPassword stores a new password hash and ends all other sessions of the user.
func (s *Store) SetPassword(ctx context.Context, userID, hash, keepSessionID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, userID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id <> $2`, userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Member is a member of the org.
type Member struct {
	UserID      string
	Email       string
	Name        string
	Role        string
	Source      string
	Disabled    bool
	LastLoginAt *time.Time
}

// ListMembers lists the members of the org.
func (s *Store) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	rows, err := s.Pool.Query(ctx, `SELECT u.id, u.email, u.name, m.role, m.source, u.disabled, u.last_login_at
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND u.deleted_at IS NULL ORDER BY m.created_at, u.id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.Source, &m.Disabled, &m.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Session is a session with its user.
type Session struct {
	ID                string
	CSRFHash          string
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	User              User
}

// CreateSession stores a session. id is the hash of the cookie value.
func (s *Store) CreateSession(ctx context.Context, id, userID, csrfHash, ip, userAgent string, expires, absolute time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO sessions (id, user_id, csrf_token_hash, ip, user_agent, expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, NULLIF($4::text, '')::inet, NULLIF($5::text, ''), $6, $7)`,
		id, userID, csrfHash, ip, userAgent, expires, absolute)
	return err
}

// GetSession finds a session that has not ended at the time now.
func (s *Store) GetSession(ctx context.Context, id string, now time.Time) (*Session, error) {
	var se Session
	u := &se.User
	err := s.Pool.QueryRow(ctx, `SELECT s.id, s.csrf_token_hash, s.expires_at, s.absolute_expires_at,
		u.id, u.email, u.name, u.password_hash, u.totp_enabled, u.disabled, u.created_at, m.org_id, m.role,
		coalesce((o.settings->>'require_2fa')::boolean, false)
		FROM sessions s JOIN users u ON u.id = s.user_id JOIN memberships m ON m.user_id = u.id JOIN orgs o ON o.id = m.org_id
		WHERE s.id = $1 AND s.expires_at > $2 AND s.absolute_expires_at > $2 AND u.deleted_at IS NULL AND NOT u.disabled
		LIMIT 1`, id, now).
		Scan(&se.ID, &se.CSRFHash, &se.ExpiresAt, &se.AbsoluteExpiresAt,
			&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.TotpEnabled, &u.Disabled, &u.CreatedAt, &u.OrgID, &u.Role, &u.Require2FA)
	if err != nil {
		return nil, notFound(err)
	}
	return &se, nil
}

// ExtendSession moves the sliding end of a session.
func (s *Store) ExtendSession(ctx context.Context, id string, expires time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE sessions SET expires_at = $2 WHERE id = $1`, id, expires)
	return err
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// HasUsers tells if a user exists (then the first-user setup is closed).
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var has bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&has)
	return has, err
}
