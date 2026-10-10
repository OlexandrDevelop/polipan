// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"
)

// Release is a release of a service with its status (F02).
type Release struct {
	ID                   string
	ServiceID            string
	ProtectedEnv         bool
	Number               int
	ImageDigest          string
	ImageRef             string
	ConfigSnapshot       json.RawMessage
	Variables            json.RawMessage
	SecretVersions       json.RawMessage
	Source               json.RawMessage
	Note                 string
	Status               string // live | previous | available | pruned
	FastRollbackPossible bool
	CreatedBy            *string
	CreatedAt            time.Time
}

const relCols = `r.id, r.service_id, e.protected, r.number, r.image_digest, r.image_ref, r.config_snapshot, r.variable_snapshot, r.secret_versions, r.source,
	COALESCE(a.note, ''),
	CASE WHEN s.live_release_id = r.id THEN 'live'
	     WHEN r.pruned_at IS NOT NULL THEN 'pruned'
	     WHEN EXISTS (SELECT 1 FROM placements pl WHERE pl.release_id = r.id AND pl.role = 'stopped' AND pl.expires_at IS NULL) THEN 'previous'
	     ELSE 'available' END,
	(r.pruned_at IS NULL AND s.live_release_id IS DISTINCT FROM r.id AND EXISTS (SELECT 1 FROM placements pl JOIN nodes n ON n.id = pl.node_id
	   WHERE pl.release_id = r.id AND pl.role = 'stopped' AND pl.expires_at IS NULL AND pl.container_id IS NOT NULL AND n.state = 'ready')),
	r.created_by, r.created_at`
const relFrom = ` FROM releases r JOIN services s ON s.id = r.service_id JOIN environments e ON e.id = s.environment_id
	JOIN projects p ON p.id = e.project_id LEFT JOIN release_annotations a ON a.release_id = r.id`

type rowScanner interface{ Scan(dest ...any) error }

func scanRelease(row rowScanner) (*Release, error) {
	var r Release
	if err := row.Scan(&r.ID, &r.ServiceID, &r.ProtectedEnv, &r.Number, &r.ImageDigest, &r.ImageRef, &r.ConfigSnapshot, &r.Variables, &r.SecretVersions,
		&r.Source, &r.Note, &r.Status, &r.FastRollbackPossible, &r.CreatedBy, &r.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

// ListReleases returns one page of the releases of a service, newest first (limit+1 rows at most).
func (s *Store) ListReleases(ctx context.Context, orgID, serviceID string, limit int, after *Cursor) ([]Release, error) {
	var t time.Time
	var id string
	if after != nil {
		t, id = after.Time, after.ID
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+relCols+relFrom+`
		WHERE r.service_id = $1 AND p.org_id = $2 AND ($3::timestamptz IS NULL OR (r.created_at, r.id) < ($3, $4))
		ORDER BY r.created_at DESC, r.id DESC LIMIT $5`, serviceID, orgID, nullTime(after, t), id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRelease reads a release of the org.
func (s *Store) GetRelease(ctx context.Context, orgID, id string) (*Release, error) {
	return scanRelease(s.Pool.QueryRow(ctx, `SELECT `+relCols+relFrom+` WHERE r.id = $1 AND p.org_id = $2`, id, orgID))
}

// SetReleaseNote sets the note of a release. The release row is not changed (REQ-REL-002).
func (s *Store) SetReleaseNote(ctx context.Context, orgID, id, note, userID string) (*Release, error) {
	if _, err := s.GetRelease(ctx, orgID, id); err != nil {
		return nil, err
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO release_annotations (release_id, note, updated_by) VALUES ($1,$2,$3)
		ON CONFLICT (release_id) DO UPDATE SET note = $2, updated_by = $3, updated_at = now()`, id, note, userID); err != nil {
		return nil, err
	}
	return s.GetRelease(ctx, orgID, id)
}

// Event is a row of the events table.
type Event struct {
	ID        int64
	Topic     string
	ObjectID  string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// EventsAfter returns the events of the org with an id above after, oldest first. topic and objectID filter when not empty.
// A topic ending in "*" matches the prefix.
func (s *Store) EventsAfter(ctx context.Context, orgID string, after int64, topic, objectID string, limit int) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, topic, object_id, payload, created_at FROM events
		WHERE org_id = $1 AND id > $2 AND topic <> 'audit' AND ($3 = '' OR topic = $3 OR ($3 LIKE '%*' AND topic LIKE replace($3, '*', '%')))
		  AND ($4 = '' OR object_id = $4) ORDER BY id LIMIT $5`, orgID, after, topic, objectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Topic, &e.ObjectID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastEventID returns the highest event id of the org (0 when there is none).
func (s *Store) LastEventID(ctx context.Context, orgID string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM events WHERE org_id = $1`, orgID).Scan(&id)
	return id, err
}

// IdempotencyGet reads a stored answer of an idempotency key (24 h, REQ-API idempotency_key_ttl).
func (s *Store) IdempotencyGet(ctx context.Context, actorID, key string) (hash []byte, status int, body []byte, found bool, err error) {
	err = s.Pool.QueryRow(ctx, `SELECT request_hash, response_status, response_body FROM idempotency_keys
		WHERE actor_id = $1 AND key = $2 AND created_at > now() - interval '24 hours'`, actorID, key).Scan(&hash, &status, &body)
	if err != nil {
		if e := notFound(err); e == ErrNotFound {
			return nil, 0, nil, false, nil
		}
		return nil, 0, nil, false, err
	}
	return hash, status, body, true, nil
}

// IdempotencyPut stores the answer of a request with an idempotency key. It also drops keys older than 24 h.
func (s *Store) IdempotencyPut(ctx context.Context, actorID, key string, hash []byte, status int, body []byte) error {
	_, _ = s.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`)
	_, err := s.Pool.Exec(ctx, `INSERT INTO idempotency_keys (key, actor_id, request_hash, response_status, response_body) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (actor_id, key) DO UPDATE SET request_hash = $3, response_status = $4, response_body = $5, created_at = now()`,
		key, actorID, hash, status, body)
	return err
}
