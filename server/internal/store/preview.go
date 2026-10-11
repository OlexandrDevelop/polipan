// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/secrets"
)

// ErrPreviewLimit means the project has preview_max_active active previews already (REQ-PREV-008).
var ErrPreviewLimit = errors.New("store: the project has the largest number of previews")

// ErrPreviewExists means the pull request has a preview environment already.
var ErrPreviewExists = errors.New("store: the pull request has a preview")

// PreviewCreate is the data of a new preview environment (REQ-PREV-003).
type PreviewCreate struct {
	OrgID, ProjectID, BaseEnvID string
	Slug                        string
	Repo                        string // the repository of the PR (lower case full name)
	Number                      int
	// Fork: the PR is from a fork. The preview gets no secret of the project or of the base environment (REQ-PREV-009).
	Fork     bool
	Settings map[string]any // the settings of the environment: preview_state and more
	TTL      time.Duration
	Now      time.Time
	// MaxActive is the limit of previews that are not closing. 0 means no limit.
	MaxActive int
	// HostFor gives the host of the domain of a service in the preview. Empty means no domain.
	HostFor func(service string) string
	// Mutate changes the config of a copied service (strategy, placement, git branch). The input is the config of the base.
	Mutate func(name, sourceType string, cfg map[string]any) map[string]any
	// Tracks tells if a service builds the commit of the PR.
	Tracks func(name, sourceType string, cfg map[string]any) bool
	Actor  Actor
}

// PreviewService is a service that a preview got from the base environment.
type PreviewService struct {
	ID, Name, Type, SourceType string
	BaseID                     string
	BaseLiveRelease            *string
	Tracks                     bool
	DBEngine                   string
}

// CreatePreviewEnvironment makes the environment of a preview and copies the services of the base environment
// into it, in one transaction (REQ-PREV-003, REQ-PREV-010, REQ-PREV-011, REQ-VOL-008):
//   - a service gets its config, a new empty volume for each volume of the base, and a domain from HostFor;
//   - a database service gets a new generated password and no data: it never uses the database of the base;
//   - only variables and secrets with preview = true are copied. A fork preview gets no secret of the project
//     or of the base environment.
func (s *Store) CreatePreviewEnvironment(ctx context.Context, p secrets.SecretProvider, in PreviewCreate) (string, []PreviewService, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, in.ProjectID); err != nil {
		return "", nil, err
	}
	if in.MaxActive > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM environments WHERE project_id = $1 AND kind = 'preview' AND deleted_at IS NULL
			AND COALESCE(settings->>'preview_state','') NOT IN ('closing','deleted')`, in.ProjectID).Scan(&n); err != nil {
			return "", nil, err
		}
		if n >= in.MaxActive {
			return "", nil, ErrPreviewLimit
		}
	}
	if in.Settings == nil {
		in.Settings = map[string]any{}
	}
	in.Settings["preview_fork"] = in.Fork
	settings, _ := json.Marshal(in.Settings)
	envID := NewID("env")
	var expires *time.Time
	if in.TTL > 0 {
		t := in.Now.Add(in.TTL)
		expires = &t
	}
	if _, err := tx.Exec(ctx, `INSERT INTO environments (id, project_id, name, slug, kind, protected, settings, base_env_id, preview_repo, preview_pr_number,
		preview_last_push_at, preview_expires_at) VALUES ($1,$2,$3,$3,'preview',false,$4,$5,$6,$7,$8,$9)`,
		envID, in.ProjectID, in.Slug, settings, in.BaseEnvID, in.Repo, in.Number, in.Now, expires); err != nil {
		if u, _ := IsUnique(err); u {
			return "", nil, ErrPreviewExists
		}
		return "", nil, err
	}
	type baseSvc struct {
		id, name, typ, srcType string
		cfg                    map[string]any
		engine, version        *string
		live                   *string
	}
	rows, err := tx.Query(ctx, `SELECT id, name, type, source_type, config, db_engine, db_version, live_release_id FROM services
		WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY name`, in.BaseEnvID)
	if err != nil {
		return "", nil, err
	}
	var bases []baseSvc
	for rows.Next() {
		var b baseSvc
		var raw []byte
		if err := rows.Scan(&b.id, &b.name, &b.typ, &b.srcType, &raw, &b.engine, &b.version, &b.live); err != nil {
			rows.Close()
			return "", nil, err
		}
		b.cfg = map[string]any{}
		_ = json.Unmarshal(raw, &b.cfg)
		bases = append(bases, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	var out []PreviewService
	for _, b := range bases {
		id := NewID("svc")
		cfg := b.cfg
		tracks := false
		if b.typ != "database" {
			if in.Tracks != nil {
				tracks = in.Tracks(b.name, b.srcType, b.cfg)
			}
			if in.Mutate != nil {
				cfg = in.Mutate(b.name, b.srcType, cloneMap(b.cfg))
			}
		}
		raw, _ := json.Marshal(cfg)
		if _, err := tx.Exec(ctx, `INSERT INTO services (id, environment_id, name, type, source_type, config, db_engine, db_version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, envID, b.name, b.typ, b.srcType, raw, b.engine, b.version); err != nil {
			return "", nil, err
		}
		ps := PreviewService{ID: id, Name: b.name, Type: b.typ, SourceType: b.srcType, BaseID: b.id, BaseLiveRelease: b.live, Tracks: tracks}
		if b.engine != nil {
			ps.DBEngine = *b.engine
		}
		out = append(out, ps)

		// Volumes: the same definitions, new empty volumes (REQ-VOL-008). The node is not pinned.
		vols, err := tx.Query(ctx, `SELECT name, kind, mount_path, size_limit_bytes, access_mode FROM volumes WHERE service_id = $1 ORDER BY name`, b.id)
		if err != nil {
			return "", nil, err
		}
		var defs []Volume
		for vols.Next() {
			var v Volume
			if err := vols.Scan(&v.Name, &v.Kind, &v.MountPath, &v.SizeLimitBytes, &v.AccessMode); err != nil {
				vols.Close()
				return "", nil, err
			}
			defs = append(defs, v)
		}
		vols.Close()
		for _, v := range defs {
			if _, err := tx.Exec(ctx, `INSERT INTO volumes (id, service_id, name, kind, mount_path, size_limit_bytes, access_mode) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				NewID("vol"), id, v.Name, v.Kind, v.MountPath, v.SizeLimitBytes, v.AccessMode); err != nil {
				return "", nil, err
			}
		}
		// One domain with the preview host (REQ-PREV-005). The fixed hostnames of the base are never copied.
		if host := hostOf(in, b.name); host != "" && b.typ != "database" {
			port := portOf(cfg)
			var basePort *int
			_ = tx.QueryRow(ctx, `SELECT container_port FROM domains WHERE service_id = $1 ORDER BY created_at, id LIMIT 1`, b.id).Scan(&basePort)
			if basePort != nil {
				port = *basePort
			}
			if port > 0 {
				if _, err := tx.Exec(ctx, `INSERT INTO domains (id, service_id, host, container_port, https) VALUES ($1,$2,$3,$4,true)`,
					NewID("dom"), id, host, port); err != nil {
					if u, _ := IsUnique(err); u {
						return "", nil, &DomainUsedError{Host: host}
					}
					return "", nil, err
				}
			}
		}
		if b.typ == "database" {
			if err := s.newPreviewDatabase(ctx, tx, p, in, envID, id, b.id, ps.DBEngine); err != nil {
				return "", nil, err
			}
			continue
		}
		if err := copyPreviewEntries(ctx, tx, p, in, envID, id, b.id, in.Fork); err != nil {
			return "", nil, err
		}
	}
	// The variables and secrets of the base environment with preview = true. A fork gets no secret (REQ-PREV-009).
	if err := copyPreviewEntries(ctx, tx, p, in, envID, "", in.BaseEnvID, in.Fork); err != nil {
		return "", nil, err
	}
	if err := checkCycles(ctx, tx, in.ProjectID); err != nil {
		return "", nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, in.Actor.audit(in.OrgID, in.ProjectID, "preview.create", "environment", envID,
		map[string]any{"repo": in.Repo, "pr_number": in.Number, "base_environment_id": in.BaseEnvID, "fork": in.Fork})); err != nil {
		return "", nil, err
	}
	return envID, out, tx.Commit(ctx)
}

func hostOf(in PreviewCreate, service string) string {
	if in.HostFor == nil {
		return ""
	}
	return in.HostFor(service)
}

func portOf(cfg map[string]any) int {
	if f, ok := cfg["port"].(float64); ok {
		return int(f)
	}
	return 0
}

func cloneMap(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// newPreviewDatabase gives a database service of a preview its own user, name and a new generated password. All of them
// are marked preview, because the container of the database needs them. Nothing of the data or of the secret of
// the base environment is used (REQ-PREV-011).
func (s *Store) newPreviewDatabase(ctx context.Context, tx pgx.Tx, p secrets.SecretProvider, in PreviewCreate, envID, svcID, baseSvcID, engine string) error {
	eng := dbengine.Get(engine)
	if eng == nil {
		return errors.New("store: unknown database engine " + engine)
	}
	spec, err := eng.NewService("", "")
	if err != nil {
		return err
	}
	// The variables of the base database keep their values (user, name, image variables). The password is new.
	rows, err := tx.Query(ctx, `SELECT name, value FROM variables WHERE service_id = $1`, baseSvcID)
	if err != nil {
		return err
	}
	have := map[string]string{}
	for rows.Next() {
		var n, v string
		if err := rows.Scan(&n, &v); err != nil {
			rows.Close()
			return err
		}
		have[n] = v
	}
	rows.Close()
	sc := &Scope{EnvironmentID: &envID, ServiceID: &svcID}
	yes := true
	for _, e := range spec.Env {
		if e.Secret {
			if _, _, err := writeSecret(ctx, tx, p, in.OrgID, in.ProjectID, sc, NewSecret{Name: e.Name, Value: e.Value, Preview: &yes}, false, in.Actor); err != nil {
				return err
			}
			continue
		}
		v := e.Value
		if old, ok := have[e.Name]; ok {
			v = old
		}
		if _, _, err := writeVariable(ctx, tx, in.OrgID, in.ProjectID, sc, NewVariable{Name: e.Name, Value: v, Preview: &yes}, false, in.Actor); err != nil {
			return err
		}
	}
	return nil
}

// copyPreviewEntries copies the variables and secrets with preview = true of a scope (the base service, or the
// base environment when svcID is empty) into the scope of the preview. A fork copies no secret.
func copyPreviewEntries(ctx context.Context, tx pgx.Tx, p secrets.SecretProvider, in PreviewCreate, envID, svcID, baseScope string, fork bool) error {
	var svc *string
	where, args := `environment_id = $1 AND service_id IS NULL`, []any{baseScope}
	if svcID != "" {
		svc = &svcID
		where, args = `service_id = $1`, []any{baseScope}
	}
	sc := &Scope{EnvironmentID: &envID, ServiceID: svc}
	rows, err := tx.Query(ctx, `SELECT name, value, build_time FROM variables WHERE `+where+` AND preview = true ORDER BY name`, args...)
	if err != nil {
		return err
	}
	type v struct {
		name, value string
		build       bool
	}
	var list []v
	for rows.Next() {
		var x v
		if err := rows.Scan(&x.name, &x.value, &x.build); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	yes := true
	for _, x := range list {
		b := x.build
		if _, _, err := writeVariable(ctx, tx, in.OrgID, in.ProjectID, sc, NewVariable{Name: x.name, Value: x.value, Preview: &yes, BuildTime: &b}, false, in.Actor); err != nil {
			return err
		}
	}
	if fork {
		return nil
	}
	type sx struct {
		id, name string
		version  int
		build    bool
	}
	srows, err := tx.Query(ctx, `SELECT id, name, current_version, build_time FROM secrets WHERE `+where+` AND preview = true AND deleted_at IS NULL ORDER BY name`, args...)
	if err != nil {
		return err
	}
	var sl []sx
	for srows.Next() {
		var x sx
		if err := srows.Scan(&x.id, &x.name, &x.version, &x.build); err != nil {
			srows.Close()
			return err
		}
		sl = append(sl, x)
	}
	srows.Close()
	for _, x := range sl {
		val, err := p.Get(ctx, tx, in.ProjectID, x.id, x.version)
		if err != nil {
			return err
		}
		b := x.build
		if _, _, err := writeSecret(ctx, tx, p, in.OrgID, in.ProjectID, sc, NewSecret{Name: x.name, Value: string(val), Preview: &yes, BuildTime: &b}, false, in.Actor); err != nil {
			return err
		}
	}
	return nil
}

// PurgePreviewEntries removes the variables and the secrets that were copied into a preview environment.
// The variables are deleted, the secrets are marked deleted and their values are destroyed.
func (s *Store) PurgePreviewEntries(ctx context.Context, p secrets.SecretProvider, envID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM variables WHERE environment_id = $1`, envID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT s.id, v.version FROM secrets s JOIN secret_versions v ON v.secret_id = s.id
		WHERE s.environment_id = $1 AND v.destroyed_at IS NULL`, envID)
	if err != nil {
		return err
	}
	type sv struct {
		id string
		v  int
	}
	var list []sv
	for rows.Next() {
		var x sv
		if err := rows.Scan(&x.id, &x.v); err != nil {
			rows.Close()
			return err
		}
		list = append(list, x)
	}
	rows.Close()
	for _, x := range list {
		if err := p.Destroy(ctx, tx, x.id, x.v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE secrets SET deleted_at = now() WHERE environment_id = $1 AND deleted_at IS NULL`, envID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
