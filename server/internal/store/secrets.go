// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/vars"
)

// Errors of variables and secrets.
var (
	ErrNameConflict    = errors.New("store: a variable or a secret with this name exists in the scope")
	ErrBadScope        = errors.New("store: the environment or the service is not in the project")
	ErrManagedByGit    = errors.New("store: the value is set by polipan.yaml")
	ErrVersionInUse    = errors.New("store: the version is current or used by a kept release")
	ErrVersionNotFound = errors.New("store: the version does not exist")
)

// Actor is who makes a change, for the audit record.
type Actor struct {
	UserID    string
	IP        string
	RequestID string
}

func (a Actor) audit(orgID, projectID, action, targetType, targetID string, diff any) secrets.AuditEvent {
	typ := "user"
	if a.UserID == "" {
		typ = "system"
	}
	return secrets.AuditEvent{OrgID: orgID, ActorType: typ, ActorID: a.UserID, Action: action, TargetType: targetType,
		TargetID: targetID, ProjectID: projectID, Diff: diff, IP: a.IP, RequestID: a.RequestID}
}

// Scope is the place of a variable or a secret. Nil ids mean the wider scope.
type Scope struct {
	EnvironmentID *string
	ServiceID     *string
	Protected     bool // the environment is protected
}

// ResolveScope checks that the environment and the service belong to the project of the org.
// A service scope also gets the environment of the service.
func (s *Store) ResolveScope(ctx context.Context, orgID, projectID string, envID, serviceID *string) (*Scope, error) {
	if _, err := s.GetProject(ctx, orgID, projectID); err != nil {
		return nil, err
	}
	sc := &Scope{}
	if serviceID != nil && *serviceID != "" {
		svc, err := s.GetService(ctx, orgID, *serviceID)
		if errors.Is(err, ErrNotFound) || (err == nil && svc.ProjectID != projectID) {
			return nil, ErrBadScope
		}
		if err != nil {
			return nil, err
		}
		if envID != nil && *envID != "" && *envID != svc.EnvironmentID {
			return nil, ErrBadScope
		}
		sc.ServiceID, sc.EnvironmentID, sc.Protected = &svc.ID, &svc.EnvironmentID, svc.EnvProtected
		return sc, nil
	}
	if envID != nil && *envID != "" {
		env, err := s.GetEnvironment(ctx, orgID, *envID)
		if errors.Is(err, ErrNotFound) || (err == nil && env.ProjectID != projectID) {
			return nil, ErrBadScope
		}
		if err != nil {
			return nil, err
		}
		sc.EnvironmentID, sc.Protected = &env.ID, env.Protected
	}
	return sc, nil
}

// lockProject serializes the writes of variables and secrets of one project.
func lockProject(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "vars:"+projectID)
	return err
}

// nameUsed tells if a variable (secret = false) or a secret (secret = true) has the name in the scope.
func nameUsed(ctx context.Context, tx pgx.Tx, secret bool, projectID string, envID, serviceID *string, name, exceptID string) (bool, error) {
	table, extra := "variables", ""
	if secret {
		table, extra = "secrets", " AND deleted_at IS NULL"
	}
	var used bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE project_id = $1 AND COALESCE(environment_id,'') = COALESCE($2,'')
		AND COALESCE(service_id,'') = COALESCE($3,'') AND name = $4 AND id <> $5`+extra+`)`,
		projectID, envID, serviceID, name, exceptID).Scan(&used)
	return used, err
}

// checkCycles looks for a reference cycle in every view of the project: the project, each
// environment and each service (REQ-ENV-005). It runs in the write transaction.
func checkCycles(ctx context.Context, tx pgx.Tx, projectID string) error {
	entries, err := vars.LoadEntries(ctx, tx, projectID)
	if err != nil {
		return err
	}
	type view struct{ env, svc string }
	views := []view{{}}
	rows, err := tx.Query(ctx, `SELECT e.id, COALESCE(s.id, '') FROM environments e LEFT JOIN services s ON s.environment_id = e.id AND s.deleted_at IS NULL
		WHERE e.project_id = $1 AND e.deleted_at IS NULL`, projectID)
	if err != nil {
		return err
	}
	seenEnv := map[string]bool{}
	for rows.Next() {
		var v view
		if err := rows.Scan(&v.env, &v.svc); err != nil {
			rows.Close()
			return err
		}
		if !seenEnv[v.env] {
			seenEnv[v.env] = true
			views = append(views, view{env: v.env})
		}
		if v.svc != "" {
			views = append(views, v)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, v := range views {
		if err := vars.CheckCycles(vars.Effective(entries, v.env, v.svc, false)); err != nil {
			return err
		}
	}
	return nil
}

// Secret is a secret without its value.
type Secret struct {
	ID             string
	ProjectID      string
	OrgID          string
	EnvironmentID  *string
	ServiceID      *string
	Name           string
	Preview        bool
	BuildTime      bool
	CurrentVersion int
	GitManaged     bool
	UpdatedBy      *string
	UpdatedAt      *time.Time
	EnvProtected   bool
}

const secretCols = `x.id, x.project_id, p.org_id, x.environment_id, x.service_id, x.name, x.preview, x.build_time, x.current_version,
	x.git_managed, v.created_by, v.created_at, COALESCE(e.protected, false)`
const secretFrom = ` FROM secrets x JOIN projects p ON p.id = x.project_id
	LEFT JOIN environments e ON e.id = x.environment_id
	LEFT JOIN secret_versions v ON v.secret_id = x.id AND v.version = x.current_version`

func scanSecret(row pgx.Row) (*Secret, error) {
	var x Secret
	if err := row.Scan(&x.ID, &x.ProjectID, &x.OrgID, &x.EnvironmentID, &x.ServiceID, &x.Name, &x.Preview, &x.BuildTime, &x.CurrentVersion,
		&x.GitManaged, &x.UpdatedBy, &x.UpdatedAt, &x.EnvProtected); err != nil {
		return nil, notFound(err)
	}
	return &x, nil
}

// ListSecrets lists the secrets of a project. A filter keeps only the secrets of that environment or service.
func (s *Store) ListSecrets(ctx context.Context, orgID, projectID string, envID, serviceID *string) ([]Secret, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+secretCols+secretFrom+`
		WHERE x.project_id = $1 AND p.org_id = $2 AND x.deleted_at IS NULL AND p.deleted_at IS NULL AND NOT `+systemProject+`
		  AND ($3::text IS NULL OR x.environment_id = $3) AND ($4::text IS NULL OR x.service_id = $4)
		ORDER BY x.name, x.id`, projectID, orgID, envID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Secret{}
	for rows.Next() {
		x, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// GetSecret reads a secret of the org.
func (s *Store) GetSecret(ctx context.Context, orgID, id string) (*Secret, error) {
	return scanSecret(s.Pool.QueryRow(ctx, `SELECT `+secretCols+secretFrom+`
		WHERE x.id = $1 AND p.org_id = $2 AND x.deleted_at IS NULL AND p.deleted_at IS NULL AND NOT `+systemProject, id, orgID))
}

// NewSecret is the data of a new secret.
type NewSecret struct {
	Name      string
	Value     string
	Preview   *bool // nil: false for a new secret, no change for an existing one
	BuildTime *bool
}

func deref(b *bool) bool { return b != nil && *b }

// writeSecret creates a secret (version 1) or, when one with the name exists in the scope, writes a new version.
// It returns the id and true when it created the secret.
func writeSecret(ctx context.Context, tx pgx.Tx, p secrets.SecretProvider, orgID, projectID string, sc *Scope, n NewSecret,
	upsert bool, a Actor) (string, bool, error) {
	if used, err := nameUsed(ctx, tx, false, projectID, sc.EnvironmentID, sc.ServiceID, n.Name, ""); err != nil || used {
		if err == nil {
			err = ErrNameConflict
		}
		return "", false, err
	}
	var id string
	var version int
	var managed bool
	err := tx.QueryRow(ctx, `SELECT id, current_version, git_managed FROM secrets WHERE project_id = $1 AND COALESCE(environment_id,'') = COALESCE($2,'')
		AND COALESCE(service_id,'') = COALESCE($3,'') AND name = $4 AND deleted_at IS NULL FOR UPDATE`,
		projectID, sc.EnvironmentID, sc.ServiceID, n.Name).Scan(&id, &version, &managed)
	switch {
	case err == nil && !upsert:
		return "", false, ErrNameConflict
	case err == nil && managed:
		return "", false, ErrManagedByGit
	case err == nil:
		if err := newVersion(ctx, tx, p, projectID, id, n.Value, a, n.Preview, n.BuildTime); err != nil {
			return "", false, err
		}
		return id, false, secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "secret.write", "secret", id,
			map[string]any{"after": map[string]any{"name": n.Name, "value": mask.Replacement, "version": version + 1}}))
	case !errors.Is(err, pgx.ErrNoRows):
		return "", false, err
	}
	id = NewID("sec")
	if _, err := tx.Exec(ctx, `INSERT INTO secrets (id, project_id, environment_id, service_id, name, preview, build_time, current_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1)`, id, projectID, sc.EnvironmentID, sc.ServiceID, n.Name, deref(n.Preview), deref(n.BuildTime)); err != nil {
		if u, _ := IsUnique(err); u {
			return "", false, ErrNameConflict
		}
		return "", false, err
	}
	if err := p.Put(ctx, tx, projectID, id, 1, []byte(n.Value), a.UserID); err != nil {
		return "", false, err
	}
	return id, true, secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "secret.create", "secret", id,
		map[string]any{"after": map[string]any{"name": n.Name, "value": mask.Replacement, "version": 1, "preview": deref(n.Preview), "build_time": deref(n.BuildTime)}}))
}

// newVersion writes value as the next version and makes it current. Flags that are not nil change too.
func newVersion(ctx context.Context, tx pgx.Tx, p secrets.SecretProvider, projectID, id, value string, a Actor, preview, buildTime *bool) error {
	var version int
	if err := tx.QueryRow(ctx, `UPDATE secrets SET current_version = (SELECT COALESCE(MAX(version),0)+1 FROM secret_versions WHERE secret_id = $1),
		preview = COALESCE($2, preview), build_time = COALESCE($3, build_time) WHERE id = $1 RETURNING current_version`,
		id, preview, buildTime).Scan(&version); err != nil {
		return err
	}
	return p.Put(ctx, tx, projectID, id, version, []byte(value), a.UserID)
}

// CreateSecret makes a secret with version 1. A variable or a secret with the name in the scope is ErrNameConflict.
func (s *Store) CreateSecret(ctx context.Context, p secrets.SecretProvider, orgID, projectID string, sc *Scope, n NewSecret, a Actor) (*Secret, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, projectID); err != nil {
		return nil, err
	}
	id, _, err := writeSecret(ctx, tx, p, orgID, projectID, sc, n, false, a)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetSecret(ctx, orgID, id)
}

// SetSecretValue writes a new version (REQ-SEC-003) and changes the flags that are not nil.
func (s *Store) SetSecretValue(ctx context.Context, p secrets.SecretProvider, orgID, id, value string, preview, buildTime *bool, a Actor) (*Secret, error) {
	cur, err := s.GetSecret(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if cur.GitManaged {
		return nil, ErrManagedByGit
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, cur.ProjectID); err != nil {
		return nil, err
	}
	if err := newVersion(ctx, tx, p, cur.ProjectID, id, value, a, preview, buildTime); err != nil {
		return nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "secret.write", "secret", id,
		map[string]any{"after": map[string]any{"name": cur.Name, "value": mask.Replacement, "version": cur.CurrentVersion + 1}})); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetSecret(ctx, orgID, id)
}

// DeleteSecret marks a secret as deleted. Its versions stay for the releases that use them (C05 edge cases).
func (s *Store) DeleteSecret(ctx context.Context, orgID, id string, a Actor) error {
	cur, err := s.GetSecret(ctx, orgID, id)
	if err != nil {
		return err
	}
	if cur.GitManaged {
		return ErrManagedByGit
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE secrets SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id); err != nil {
		return err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "secret.delete", "secret", id,
		map[string]any{"before": map[string]any{"name": cur.Name, "value": mask.Replacement}})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SecretVersion is one version of a secret, without its value.
type SecretVersion struct {
	Version        int
	CreatedBy      *string
	CreatedAt      time.Time
	Destroyed      bool
	UsedByReleases int
}

// usedByReleases counts the kept releases that use a version of a secret.
const usedByReleases = `(SELECT count(*) FROM releases r WHERE r.pruned_at IS NULL
	AND EXISTS (SELECT 1 FROM jsonb_each_text(COALESCE(r.config_snapshot->'secret_refs', '{}'::jsonb)) ref
	            WHERE ref.value = v.secret_id AND (r.secret_versions->>ref.key)::int = v.version))`

// ListSecretVersions lists the versions of a secret, newest first.
func (s *Store) ListSecretVersions(ctx context.Context, orgID, id string) ([]SecretVersion, error) {
	if _, err := s.GetSecret(ctx, orgID, id); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT v.version, v.created_by, v.created_at, v.destroyed_at IS NOT NULL, `+usedByReleases+`
		FROM secret_versions v WHERE v.secret_id = $1 ORDER BY v.version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecretVersion{}
	for rows.Next() {
		var v SecretVersion
		if err := rows.Scan(&v.Version, &v.CreatedBy, &v.CreatedAt, &v.Destroyed, &v.UsedByReleases); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RestoreSecretVersion writes the value of an earlier version as a new version (history stays linear).
func (s *Store) RestoreSecretVersion(ctx context.Context, p secrets.SecretProvider, orgID, id string, version int, a Actor) (*Secret, error) {
	cur, err := s.GetSecret(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if cur.GitManaged {
		return nil, ErrManagedByGit
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, cur.ProjectID); err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM secret_versions WHERE secret_id = $1 AND version = $2)`, id, version).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrVersionNotFound
	}
	value, err := p.Get(ctx, tx, cur.ProjectID, id, version)
	if err != nil {
		return nil, err
	}
	if err := newVersion(ctx, tx, p, cur.ProjectID, id, string(value), a, nil, nil); err != nil {
		return nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "secret.restore", "secret", id,
		map[string]any{"after": map[string]any{"name": cur.Name, "value": mask.Replacement, "restored_version": version, "version": cur.CurrentVersion + 1}})); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetSecret(ctx, orgID, id)
}

// DestroySecretVersion removes the ciphertext of an old version. The current version and versions
// used by kept releases cannot be destroyed (ErrVersionInUse).
func (s *Store) DestroySecretVersion(ctx context.Context, p secrets.SecretProvider, orgID, id string, version int, a Actor) error {
	cur, err := s.GetSecret(ctx, orgID, id)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, cur.ProjectID); err != nil {
		return err
	}
	var used int
	err = tx.QueryRow(ctx, `SELECT `+usedByReleases+` FROM secret_versions v WHERE v.secret_id = $1 AND v.version = $2`, id, version).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	}
	if err != nil {
		return err
	}
	if used > 0 || version == cur.CurrentVersion {
		return ErrVersionInUse
	}
	if err := p.Destroy(ctx, tx, id, version); err != nil {
		return err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "secret.destroy", "secret", id,
		map[string]any{"before": map[string]any{"name": cur.Name, "version": version}})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
