// SPDX-License-Identifier: Apache-2.0

// Package audit writes the hash-chained audit log (10-security, section 5).
// Every row has hash = SHA-256(prev_hash || canonical_json(the row without hash)).
// Writes of one org are serialized with an advisory lock, so the chain has no fork.
package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Event is one audit record. Diff must never hold a secret value: use "***".
// Empty ActorType, IP, UserAgent and RequestID are filled from the request info in the context.
type Event struct {
	OrgID      string
	ActorType  string // user, token, system, agent, sso
	ActorID    string
	Action     string // e.g. secret.write, role.change
	TargetType string
	TargetID   string
	ProjectID  string
	Diff       any
	IP         string
	UserAgent  string
	RequestID  string
}

// Request is what the HTTP layer knows about the caller. It goes in the context, so a
// write deep in the store has the actor, address and request id without extra arguments.
type Request struct {
	ActorType string
	ActorID   string
	// UserID is the user behind the request. For an API token ActorID is the token id.
	UserID    string
	IP        string
	UserAgent string
	RequestID string

	wrote bool
}

type ctxKey struct{}

// WithRequest puts the request info in the context.
func WithRequest(ctx context.Context, r *Request) context.Context {
	return context.WithValue(ctx, ctxKey{}, r)
}

// RequestFrom returns the request info of the context, or nil.
func RequestFrom(ctx context.Context) *Request {
	r, _ := ctx.Value(ctxKey{}).(*Request)
	return r
}

// Wrote tells if an event was written with this request info.
func (r *Request) Wrote() bool { return r.wrote }

// ActorType values.
const (
	ActorUser   = "user"
	ActorToken  = "token"
	ActorSystem = "system"
	ActorAgent  = "agent"
	ActorSSO    = "sso"
)

// row is the part of an event that is hashed. The field order is fixed.
type row struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	ActorType  string          `json:"actor_type"`
	ActorID    string          `json:"actor_id"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	ProjectID  string          `json:"project_id"`
	Diff       json.RawMessage `json:"diff"`
	IP         string          `json:"ip"`
	UserAgent  string          `json:"user_agent"`
	RequestID  string          `json:"request_id"`
	CreatedAt  string          `json:"created_at"`
}

// Hash is the chain hash of a row.
func hashRow(prev []byte, r row) []byte {
	raw, _ := json.Marshal(r)
	h := sha256.New()
	h.Write(prev)
	h.Write(raw)
	return h.Sum(nil)
}

// canonicalJSON re-encodes JSON with sorted keys, so the text of the jsonb column and the text
// that was hashed are the same. Numbers keep their text.
func canonicalJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// canonicalIP gives one text form of an address: IPv4 stays dotted, IPv6 is compressed.
func canonicalIP(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "ip:")
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	out := make([]byte, 26)
	for i := range out {
		out[i] = alphabet[int(b[i%16]^byte(i*7))%len(alphabet)]
	}
	return "aud_" + string(out)
}

// ErrNotInTx is returned when Write gets something that cannot start a transaction.
var ErrNotInTx = errors.New("audit: Write needs a transaction")

// Write appends an event to the chain of its org, in the transaction q (or in its own transaction when q is a pool).
// With a transaction the caller commits. When the transaction rolls back, the event is gone too.
func Write(ctx context.Context, q Querier, e Event) error {
	if _, ok := q.(pgx.Tx); !ok {
		// A pool: the event gets its own transaction.
		b, ok := q.(interface {
			Begin(context.Context) (pgx.Tx, error)
		})
		if !ok {
			return ErrNotInTx
		}
		tx, err := b.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := Write(ctx, tx, e); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if e.OrgID == "" || e.Action == "" || e.TargetType == "" {
		return errors.New("audit: org, action and target type are needed")
	}
	req := RequestFrom(ctx)
	if req != nil {
		switch {
		case e.ActorType == "":
			e.ActorType, e.ActorID = req.ActorType, req.ActorID
		case e.ActorType == ActorUser && req.ActorType == ActorToken && e.ActorID == req.UserID:
			// A change made with an API token is the action of the token.
			e.ActorType, e.ActorID = req.ActorType, req.ActorID
		}
		if e.IP == "" {
			e.IP = req.IP
		}
		if e.UserAgent == "" {
			e.UserAgent = req.UserAgent
		}
		if e.RequestID == "" {
			e.RequestID = req.RequestID
		}
	}
	if e.ActorType == "" {
		e.ActorType = ActorSystem
	}
	diff := []byte("null")
	if e.Diff != nil {
		raw, err := json.Marshal(e.Diff)
		if err != nil {
			return fmt.Errorf("audit: diff: %w", err)
		}
		if diff, err = canonicalJSON(raw); err != nil {
			return fmt.Errorf("audit: diff: %w", err)
		}
	}
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('audit:' || $1::text, 0))`, e.OrgID); err != nil {
		return err
	}
	prev := make([]byte, 32)
	err := q.QueryRow(ctx, `SELECT hash FROM audit_events WHERE org_id = $1 ORDER BY seq DESC LIMIT 1`, e.OrgID).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		// After an archive the rows are gone: the checkpoint has the last hash.
		err = q.QueryRow(ctx, `SELECT last_hash FROM audit_archives WHERE org_id = $1 ORDER BY last_seq DESC LIMIT 1`, e.OrgID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			prev, err = make([]byte, 32), nil
		}
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	r := row{ID: newID(), OrgID: e.OrgID, ActorType: e.ActorType, ActorID: e.ActorID, Action: e.Action,
		TargetType: e.TargetType, TargetID: e.TargetID, ProjectID: e.ProjectID, Diff: diff,
		IP: canonicalIP(e.IP), UserAgent: e.UserAgent, RequestID: e.RequestID,
		CreatedAt: now.Format(time.RFC3339Nano)}
	hash := hashRow(prev, r)
	_, err = q.Exec(ctx, `INSERT INTO audit_events
		(id, org_id, actor_type, actor_id, action, target_type, target_id, project_id, diff, ip, user_agent, request_id, prev_hash, hash, created_at)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, NULLIF($7,''), NULLIF($8,''), $9::jsonb, NULLIF($10,'')::inet, NULLIF($11,''), NULLIF($12,''), $13, $14, $15)`,
		r.ID, r.OrgID, r.ActorType, r.ActorID, r.Action, r.TargetType, r.TargetID, r.ProjectID, string(diff),
		r.IP, r.UserAgent, r.RequestID, prev, hash, now)
	if err == nil && req != nil {
		req.wrote = true
	}
	return err
}
