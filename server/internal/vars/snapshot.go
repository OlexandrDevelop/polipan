// SPDX-License-Identifier: Apache-2.0

package vars

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/dbengine"
)

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Scope is a service with the ids of its environment and project.
type Scope struct {
	OrgID         string
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	ServiceName   string
	EnvSlug       string
	Preview       bool // the environment is a preview environment
	Config        json.RawMessage
}

// LoadScope reads the scope of a service (also of a deleted one, for old releases).
func LoadScope(ctx context.Context, q Querier, serviceID string) (*Scope, error) {
	var s Scope
	var kind string
	err := q.QueryRow(ctx, `SELECT p.org_id, e.project_id, s.environment_id, s.name, e.slug, e.kind, s.config
		FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		WHERE s.id = $1`, serviceID).Scan(&s.OrgID, &s.ProjectID, &s.EnvironmentID, &s.ServiceName, &s.EnvSlug, &kind, &s.Config)
	if err != nil {
		return nil, err
	}
	s.ServiceID = serviceID
	s.Preview = kind == "preview"
	return &s, nil
}

// LoadEntries reads all variables and all secrets (not deleted) of a project. Secret values are not read.
func LoadEntries(ctx context.Context, q Querier, projectID string) ([]Entry, error) {
	rows, err := q.Query(ctx, `
		SELECT id, name, value, false, 0, COALESCE(environment_id,''), COALESCE(service_id,''), preview, build_time
		  FROM variables WHERE project_id = $1
		UNION ALL
		SELECT id, name, '', true, current_version, COALESCE(environment_id,''), COALESCE(service_id,''), preview, build_time
		  FROM secrets WHERE project_id = $1 AND deleted_at IS NULL
		ORDER BY 2, 1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Name, &e.Value, &e.Secret, &e.Version, &e.EnvironmentID, &e.ServiceID, &e.Preview, &e.BuildTime); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Volume is a volume as it is stored in a release (config_snapshot.volumes).
type Volume struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	MountPath      string  `json:"mount_path"`
	NodeID         *string `json:"node_id,omitempty"`
	SizeLimitBytes *int64  `json:"size_limit_bytes,omitempty"`
	AccessMode     string  `json:"access_mode"`
}

// LoadVolumes reads the volumes of a service.
func LoadVolumes(ctx context.Context, q Querier, serviceID string) ([]Volume, error) {
	rows, err := q.Query(ctx, `SELECT id, name, kind, mount_path, node_id, size_limit_bytes, access_mode
		FROM volumes WHERE service_id = $1 ORDER BY name, id`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Volume{}
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ID, &v.Name, &v.Kind, &v.MountPath, &v.NodeID, &v.SizeLimitBytes, &v.AccessMode); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SecretFile is a secret written as a file into the container (config secret_files).
type SecretFile struct {
	Path   string `json:"path"`
	Secret string `json:"secret"`
}

// File is a config file mounted read-only (config files, REQ-VOL-006).
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Keys that a release adds to the service config in config_snapshot.
const (
	KeyVolumes      = "volumes"       // []Volume
	KeySecretRefs   = "secret_refs"   // name -> secret id, so a release finds the same secret later
	KeyEnvTemplates = "env_templates" // name -> value with ${{SECRET}} references
)

// Snapshot is what a release stores: config_snapshot, variable_snapshot and secret_versions.
type Snapshot struct {
	Scope          *Scope
	Config         json.RawMessage
	Variables      map[string]string
	SecretVersions map[string]int
	Secrets        map[string]Entry // by name, with id and version
	BuildTime      map[string]bool
}

// Build resolves the variables and secrets of a service and makes the release snapshot.
// A missing name gives *UnresolvedError, a cycle gives *CycleError.
func Build(ctx context.Context, q Querier, serviceID string) (*Snapshot, error) {
	scope, err := LoadScope(ctx, q, serviceID)
	if err != nil {
		return nil, err
	}
	entries, err := LoadEntries(ctx, q, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	dbEntries, err := DatabaseEntries(ctx, q, scope.EnvironmentID)
	if err != nil {
		return nil, err
	}
	view := Effective(entries, scope.EnvironmentID, serviceID, scope.Preview)
	for _, e := range dbEntries {
		view[e.Name] = e
	}
	res, err := Resolve(view)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if len(scope.Config) > 0 {
		if err := json.Unmarshal(scope.Config, &cfg); err != nil {
			return nil, err
		}
	}
	// Every secret file must name a secret of the view.
	var files []SecretFile
	if raw, ok := cfg["secret_files"]; ok {
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &files); err != nil {
			return nil, errors.New("config.secret_files is not valid")
		}
	}
	var missing []string
	for _, f := range files {
		if _, ok := res.Secrets[f.Secret]; !ok {
			missing = append(missing, f.Secret)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &UnresolvedError{Names: missing}
	}
	vols, err := LoadVolumes(ctx, q, serviceID)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Scope: scope, Variables: res.Plain, SecretVersions: map[string]int{}, Secrets: res.Secrets, BuildTime: res.BuildTime}
	refs := map[string]string{}
	for name, e := range res.Secrets {
		snap.SecretVersions[name] = e.Version
		refs[name] = e.ID
	}
	cfg[KeyVolumes] = vols
	cfg[KeySecretRefs] = refs
	cfg[KeyEnvTemplates] = res.Templates
	if snap.Config, err = json.Marshal(cfg); err != nil {
		return nil, err
	}
	return snap, nil
}

// Pending tells if the resolved config of the service differs from its live release (REQ-ENV-003).
// A service without a live release has no pending changes. A config that does not resolve is pending.
func Pending(ctx context.Context, q Querier, serviceID string, liveReleaseID *string) (bool, error) {
	if liveReleaseID == nil {
		return false, nil
	}
	var cfg, variables, versions []byte
	err := q.QueryRow(ctx, `SELECT config_snapshot, variable_snapshot, secret_versions FROM releases WHERE id = $1`, *liveReleaseID).
		Scan(&cfg, &variables, &versions)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	snap, err := Build(ctx, q, serviceID)
	var cycle *CycleError
	var unresolved *UnresolvedError
	if errors.As(err, &cycle) || errors.As(err, &unresolved) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	newVars, _ := json.Marshal(snap.Variables)
	newVersions, _ := json.Marshal(snap.SecretVersions)
	return !sameJSON(cfg, snap.Config) || !sameJSON(variables, newVars) || !sameJSON(versions, newVersions), nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// DatabaseEntries makes the reference entries ${{db.<name>.FIELD}} of the database services of an
// environment (REQ-DB-004). HOST is the stable network alias of the service. PASSWORD points to the
// secret DB_PASSWORD of the database, so it resolves through the secrets store and stays a secret.
func DatabaseEntries(ctx context.Context, q Querier, environmentID string) ([]Entry, error) {
	rows, err := q.Query(ctx, `SELECT s.id, s.name, s.db_engine,
		COALESCE((SELECT x.value FROM variables x WHERE x.service_id = s.id AND x.name = 'DB_USER'), ''),
		COALESCE((SELECT x.value FROM variables x WHERE x.service_id = s.id AND x.name = 'DB_NAME'), ''),
		COALESCE((SELECT x.id FROM secrets x WHERE x.service_id = s.id AND x.name = 'DB_PASSWORD' AND x.deleted_at IS NULL), ''),
		COALESCE((SELECT x.current_version FROM secrets x WHERE x.service_id = s.id AND x.name = 'DB_PASSWORD' AND x.deleted_at IS NULL), 0)
		FROM services s WHERE s.environment_id = $1 AND s.type = 'database' AND s.deleted_at IS NULL ORDER BY s.name`, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var id, name, user, dbname, secretID string
		var engineName *string
		var version int
		if err := rows.Scan(&id, &name, &engineName, &user, &dbname, &secretID, &version); err != nil {
			return nil, err
		}
		if engineName == nil || secretID == "" {
			continue
		}
		eng := dbengine.Get(*engineName)
		if eng == nil {
			continue
		}
		prefix := "db." + name + "."
		host := dbengine.Alias(id)
		out = append(out,
			Entry{Name: prefix + "HOST", Value: host, Ref: true},
			Entry{Name: prefix + "PORT", Value: strconv.Itoa(eng.Port), Ref: true},
			Entry{Name: prefix + "USER", Value: user, Ref: true},
			Entry{Name: prefix + "NAME", Value: dbname, Ref: true},
			Entry{Name: prefix + "PASSWORD", Secret: true, ID: secretID, Version: version, Ref: true},
			Entry{Name: prefix + "URL", Value: eng.URL(host, user, dbname, "${{"+prefix+"PASSWORD}}"), Ref: true})
	}
	return out, rows.Err()
}
