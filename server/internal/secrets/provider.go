// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/vars"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SecretProvider stores and reads secret values. q is the transaction of the caller, so the
// value is written together with the secret row. Only the built-in DBProvider exists in v1.
type SecretProvider interface {
	// Put stores the value of a new version.
	Put(ctx context.Context, q Querier, projectID, secretID string, version int, value []byte, actorID string) error
	// Get returns the value of a version. A destroyed version gives ErrVersionDestroyed.
	Get(ctx context.Context, q Querier, projectID, secretID string, version int) ([]byte, error)
	// Destroy removes the value of a version for good.
	Destroy(ctx context.Context, q Querier, secretID string, version int) error
}

// DBProvider keeps the values encrypted in secret_versions (envelope encryption with the KEK).
type DBProvider struct {
	kek   []byte
	kekID string
}

var _ SecretProvider = (*DBProvider)(nil)

// NewDBProvider makes the built-in provider. kek is the master key (32 bytes).
func NewDBProvider(kek []byte) (*DBProvider, error) {
	if len(kek) != KeySize {
		return nil, ErrBadKey
	}
	return &DBProvider{kek: append([]byte{}, kek...), kekID: KEKID(kek)}, nil
}

// dek returns the plain DEK of a project. With create it makes the DEK when there is none.
func (p *DBProvider) dek(ctx context.Context, q Querier, projectID string, create bool) ([]byte, error) {
	for try := 0; try < 2; try++ {
		var wrapped []byte
		var kekID string
		// A write locks the key row, so rotate-dek waits for it. A read (also in a read-only transaction) does not.
		lock := ""
		if create {
			lock = " FOR SHARE"
		}
		err := q.QueryRow(ctx, `SELECT dek_enc, kek_id FROM data_keys WHERE project_id = $1`+lock, projectID).Scan(&wrapped, &kekID)
		if err == nil {
			if kekID != p.kekID {
				return nil, ErrWrongKEK
			}
			return UnwrapDEK(p.kek, wrapped, projectID)
		}
		if !errors.Is(err, pgx.ErrNoRows) || !create {
			return nil, err
		}
		// A server that still runs with an old KEK after rotate-kek must not make new keys.
		var other bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM data_keys WHERE kek_id <> $1)`, p.kekID).Scan(&other); err != nil {
			return nil, err
		}
		if other {
			return nil, ErrWrongKEK
		}
		dek, err := NewDEK()
		if err != nil {
			return nil, err
		}
		enc, err := WrapDEK(p.kek, dek, projectID)
		if err != nil {
			return nil, err
		}
		if _, err := q.Exec(ctx, `INSERT INTO data_keys (project_id, dek_enc, kek_id) VALUES ($1, $2, $3) ON CONFLICT (project_id) DO NOTHING`,
			projectID, enc, p.kekID); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("secrets: cannot make the data key")
}

// Put encrypts the value with the project DEK and stores the version.
func (p *DBProvider) Put(ctx context.Context, q Querier, projectID, secretID string, version int, value []byte, actorID string) error {
	dek, err := p.dek(ctx, q, projectID, true)
	if err != nil {
		return err
	}
	nonce, ct, err := Seal(dek, secretID, version, value)
	if err != nil {
		return err
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	_, err = q.Exec(ctx, `INSERT INTO secret_versions (secret_id, version, nonce, ciphertext, created_by) VALUES ($1, $2, $3, $4, $5)`,
		secretID, version, nonce, ct, actor)
	return err
}

// Get decrypts a version.
func (p *DBProvider) Get(ctx context.Context, q Querier, projectID, secretID string, version int) ([]byte, error) {
	var nonce, ct []byte
	var destroyed *time.Time
	err := q.QueryRow(ctx, `SELECT nonce, ciphertext, destroyed_at FROM secret_versions WHERE secret_id = $1 AND version = $2`,
		secretID, version).Scan(&nonce, &ct, &destroyed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("secrets: version %d of %s does not exist", version, secretID)
	}
	if err != nil {
		return nil, err
	}
	if destroyed != nil {
		return nil, ErrVersionDestroyed
	}
	dek, err := p.dek(ctx, q, projectID, false)
	if err != nil {
		return nil, err
	}
	return Open(dek, secretID, version, nonce, ct)
}

// Destroy removes the ciphertext and the nonce of a version. The row stays for the history.
func (p *DBProvider) Destroy(ctx context.Context, q Querier, secretID string, version int) error {
	_, err := q.Exec(ctx, `UPDATE secret_versions SET nonce = $3, ciphertext = $3, destroyed_at = now()
		WHERE secret_id = $1 AND version = $2 AND destroyed_at IS NULL`, secretID, version, []byte{})
	return err
}

// Manager is the secrets store of the server: the provider and the database.
type Manager struct {
	DB       *pgxpool.Pool
	Provider SecretProvider
}

// NewManager makes a Manager with the built-in provider.
func NewManager(pool *pgxpool.Pool, kek []byte) (*Manager, error) {
	p, err := NewDBProvider(kek)
	if err != nil {
		return nil, err
	}
	return &Manager{DB: pool, Provider: p}, nil
}

// Ref is a secret version.
type Ref struct {
	SecretID string
	Version  int
}

// ReleaseRefs reads the secret versions of a release: name -> (secret id, version).
func ReleaseRefs(ctx context.Context, q Querier, releaseID string) (map[string]Ref, error) {
	var refsRaw, versionsRaw []byte
	err := q.QueryRow(ctx, `SELECT COALESCE(config_snapshot->'`+vars.KeySecretRefs+`', '{}'::jsonb), secret_versions FROM releases WHERE id = $1`,
		releaseID).Scan(&refsRaw, &versionsRaw)
	if err != nil {
		return nil, err
	}
	return parseRefs(refsRaw, versionsRaw)
}

func parseRefs(refsRaw, versionsRaw []byte) (map[string]Ref, error) {
	var refs map[string]string
	var versions map[string]int
	if err := json.Unmarshal(refsRaw, &refs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(versionsRaw, &versions); err != nil {
		return nil, err
	}
	out := map[string]Ref{}
	for name, v := range versions {
		id, ok := refs[name]
		if !ok {
			return nil, fmt.Errorf("secrets: the release has no secret id for %s", name)
		}
		out[name] = Ref{SecretID: id, Version: v}
	}
	return out, nil
}

// ValuesForService returns the values of the secrets of a service: the current versions of the
// secrets the service sees now, and the versions used by its releases that are kept.
// Use them to build a mask.Masker for build and deploy logs (REQ-SEC-007). Destroyed versions are skipped.
func (m *Manager) ValuesForService(ctx context.Context, serviceID string) ([]string, error) {
	if m == nil {
		return nil, nil
	}
	scope, err := vars.LoadScope(ctx, m.DB, serviceID)
	if err != nil {
		return nil, err
	}
	entries, err := vars.LoadEntries(ctx, m.DB, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	want := map[Ref]bool{}
	// All secrets of the service scope (also those hidden from previews), current version.
	dbEntries, err := vars.DatabaseEntries(ctx, m.DB, scope.EnvironmentID)
	if err != nil {
		return nil, err
	}
	view := vars.Effective(entries, scope.EnvironmentID, serviceID, false)
	for _, e := range dbEntries {
		view[e.Name] = e
	}
	for _, e := range view {
		if e.Secret {
			want[Ref{e.ID, e.Version}] = true
		}
	}
	rows, err := m.DB.Query(ctx, `SELECT COALESCE(config_snapshot->'`+vars.KeySecretRefs+`', '{}'::jsonb), secret_versions
		FROM releases WHERE service_id = $1 AND pruned_at IS NULL`, serviceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var refsRaw, versionsRaw []byte
		if err := rows.Scan(&refsRaw, &versionsRaw); err != nil {
			rows.Close()
			return nil, err
		}
		refs, err := parseRefs(refsRaw, versionsRaw)
		if err != nil {
			continue
		}
		for _, r := range refs {
			want[r] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for r := range want {
		v, err := m.Provider.Get(ctx, m.DB, scope.ProjectID, r.SecretID, r.Version)
		if errors.Is(err, ErrVersionDestroyed) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, string(v))
	}
	return out, nil
}

// Masker returns a mask.Masker with the secret values of a service.
func (m *Manager) Masker(ctx context.Context, serviceID string) (*mask.Masker, error) {
	values, err := m.ValuesForService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	return mask.New(values), nil
}
