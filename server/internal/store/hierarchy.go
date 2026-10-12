// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Project is a project.
type Project struct {
	ID              string
	OrgID           string
	Name            string
	Slug            string
	Settings        json.RawMessage
	ConfigSyncState string
	// ConfigSource is {connection_id, repo, branch, path}, or empty when the project is not synced from Git.
	ConfigSource json.RawMessage
	CreatedAt    time.Time
}

const projectCols = `p.id, p.org_id, p.name, p.slug, p.settings, p.config_sync_state, COALESCE(p.config_source, 'null'::jsonb), p.created_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	if err := row.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Settings, &p.ConfigSyncState, &p.ConfigSource, &p.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// ListProjects returns one page, oldest first. It returns up to limit+1 rows so the caller can see if there is more.
func (s *Store) ListProjects(ctx context.Context, orgID string, limit int, after *Cursor) ([]Project, error) {
	var t time.Time
	var id string
	if after != nil {
		t, id = after.Time, after.ID
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+projectCols+` FROM projects p
		WHERE p.org_id = $1 AND p.deleted_at IS NULL AND NOT `+systemProject+` AND ($2::timestamptz IS NULL OR (p.created_at, p.id) > ($2, $3))
		ORDER BY p.created_at, p.id LIMIT $4`, orgID, nullTime(after, t), id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Slug, &p.Settings, &p.ConfigSyncState, &p.ConfigSource, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullTime(c *Cursor, t time.Time) *time.Time {
	if c == nil {
		return nil
	}
	return &t
}

// GetProject reads a project of the org.
func (s *Store) GetProject(ctx context.Context, orgID, id string) (*Project, error) {
	return scanProject(s.Pool.QueryRow(ctx, `SELECT `+projectCols+` FROM projects p
		WHERE p.id = $1 AND p.org_id = $2 AND p.deleted_at IS NULL AND NOT `+systemProject, id, orgID))
}

// systemProject is true for the hidden project that holds the secrets of backup targets and policies (not a user project).
const systemProject = `COALESCE(p.settings->>'system', '') = 'true'`

// NewEnv is the data for an environment that is created together with a project or on its own.
type NewEnv struct {
	Name      string
	Slug      string
	Kind      string
	Protected bool
}

// CreateProject makes a project and the given environments in one transaction.
func (s *Store) CreateProject(ctx context.Context, orgID, name, slug string, envs []NewEnv) (*Project, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := NewID("prj")
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, id, orgID, name, slug); err != nil {
		return nil, err
	}
	for _, e := range envs {
		if _, err := tx.Exec(ctx, `INSERT INTO environments (id, project_id, name, slug, kind, protected) VALUES ($1, $2, $3, $4, $5, $6)`,
			NewID("env"), id, e.Name, e.Slug, e.Kind, e.Protected); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetProject(ctx, orgID, id)
}

// UpdateProject changes the name and the settings. A nil value is not changed.
func (s *Store) UpdateProject(ctx context.Context, orgID, id string, name *string, settings json.RawMessage) (*Project, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE projects SET name = COALESCE($3, name), settings = COALESCE($4::jsonb, settings), updated_at = now()
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID, name, nullJSON(settings))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetProject(ctx, orgID, id)
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// A soft deleted row keeps its unique name or slug, so the name gets a suffix. The user can then use the name again.
const deletedSuffix = ` || '~deleted~' || id`

// SoftDeleteProject marks the project, its environments and its services as deleted.
// It returns the ids of the services, so the caller can remove their containers.
func (s *Store) SoftDeleteProject(ctx context.Context, orgID, id string) ([]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE projects SET deleted_at = now(), slug = slug`+deletedSuffix+`
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	ids, err := softDeleteServices(ctx, tx, `environment_id IN (SELECT id FROM environments WHERE project_id = $1)`, id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE environments SET deleted_at = now(), slug = slug`+deletedSuffix+`
		WHERE project_id = $1 AND deleted_at IS NULL`, id); err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
}

// softDeleteServices marks the services that match the condition and removes their domains.
func softDeleteServices(ctx context.Context, tx pgx.Tx, cond, arg string) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE services SET deleted_at = now(), name = name`+deletedSuffix+`, updated_at = now()
		WHERE deleted_at IS NULL AND `+cond+` RETURNING id`, arg)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, sid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM domains WHERE service_id = ANY($1)`, ids); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// Environment is an environment with the org it belongs to.
type Environment struct {
	ID        string
	ProjectID string
	OrgID     string
	Name      string
	Slug      string
	Kind      string
	Protected bool
	Settings  json.RawMessage
	BaseEnvID *string
}

const envCols = `e.id, e.project_id, p.org_id, e.name, e.slug, e.kind, e.protected, e.settings, e.base_env_id`
const envFrom = ` FROM environments e JOIN projects p ON p.id = e.project_id`

func scanEnv(row pgx.Row) (*Environment, error) {
	var e Environment
	if err := row.Scan(&e.ID, &e.ProjectID, &e.OrgID, &e.Name, &e.Slug, &e.Kind, &e.Protected, &e.Settings, &e.BaseEnvID); err != nil {
		return nil, notFound(err)
	}
	return &e, nil
}

// ListEnvironments lists the environments of a project.
func (s *Store) ListEnvironments(ctx context.Context, orgID, projectID string) ([]Environment, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+envCols+envFrom+`
		WHERE e.project_id = $1 AND p.org_id = $2 AND e.deleted_at IS NULL AND p.deleted_at IS NULL ORDER BY e.created_at, e.id`, projectID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Environment{}
	for rows.Next() {
		var e Environment
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.OrgID, &e.Name, &e.Slug, &e.Kind, &e.Protected, &e.Settings, &e.BaseEnvID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetEnvironment reads an environment of the org.
func (s *Store) GetEnvironment(ctx context.Context, orgID, id string) (*Environment, error) {
	return scanEnv(s.Pool.QueryRow(ctx, `SELECT `+envCols+envFrom+`
		WHERE e.id = $1 AND p.org_id = $2 AND e.deleted_at IS NULL AND p.deleted_at IS NULL`, id, orgID))
}

// CreateEnvironment adds an environment to a project of the org.
func (s *Store) CreateEnvironment(ctx context.Context, orgID, projectID string, e NewEnv) (*Environment, error) {
	if _, err := s.GetProject(ctx, orgID, projectID); err != nil {
		return nil, err
	}
	id := NewID("env")
	if _, err := s.Pool.Exec(ctx, `INSERT INTO environments (id, project_id, name, slug, kind, protected) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, projectID, e.Name, e.Slug, e.Kind, e.Protected); err != nil {
		return nil, err
	}
	return s.GetEnvironment(ctx, orgID, id)
}

// UpdateEnvironment changes an environment. A nil value is not changed.
func (s *Store) UpdateEnvironment(ctx context.Context, orgID, id string, name *string, protected *bool, settings json.RawMessage) (*Environment, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE environments SET name = COALESCE($3, name), protected = COALESCE($4, protected),
		settings = COALESCE($5::jsonb, settings), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL AND project_id IN (SELECT id FROM projects WHERE org_id = $2 AND deleted_at IS NULL)`,
		id, orgID, name, protected, nullJSON(settings))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetEnvironment(ctx, orgID, id)
}

// SoftDeleteEnvironment marks the environment and its services as deleted. It returns the service ids.
func (s *Store) SoftDeleteEnvironment(ctx context.Context, orgID, id string) ([]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE environments SET deleted_at = now(), slug = slug`+deletedSuffix+`
		WHERE id = $1 AND deleted_at IS NULL AND project_id IN (SELECT id FROM projects WHERE org_id = $2 AND deleted_at IS NULL)`, id, orgID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	ids, err := softDeleteServices(ctx, tx, `environment_id = $1`, id)
	if err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
}

// Service is a service with the data of its environment and a computed status.
type Service struct {
	ID            string
	EnvironmentID string
	ProjectID     string
	OrgID         string
	EnvProtected  bool
	Name          string
	Type          string
	SourceType    string
	DBEngine      *string
	Config        json.RawMessage
	GitManaged    []string
	LiveReleaseID *string
	DesiredState  string
	Failover      string
	AllowDataLoss bool
	Status        string
	Placements    []Placement
}

// Placement is where a service runs.
type Placement struct {
	ID           string
	NodeID       string
	ReleaseID    string
	ReplicaIndex int
	Color        string
	Role         string
	Weight       int
	Health       string
	ContainerID  string
	IP           string
	CreatedAt    time.Time
}

const activeStates = `'queued','building','image_ready','starting','health_checking','shifting','switching','draining'`

const svcCols = `s.id, s.environment_id, e.project_id, p.org_id, e.protected, s.name, s.type, s.source_type, s.db_engine, s.config,
	s.git_managed_fields, s.live_release_id, s.desired_state, s.failover, s.allow_data_loss_failover,
	CASE
	  WHEN s.desired_state = 'stopped' THEN 'stopped'
	  WHEN EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = s.id AND d.state IN (` + activeStates + `)) THEN 'deploying'
	  WHEN s.live_release_id IS NULL AND EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = s.id AND d.state = 'failed') THEN 'failed'
	  WHEN s.live_release_id IS NULL THEN 'no_release'
	  WHEN EXISTS (SELECT 1 FROM placements pl WHERE pl.service_id = s.id AND pl.role = 'live' AND pl.health = 'unhealthy') THEN 'degraded'
	  ELSE 'running'
	END`
const svcFrom = ` FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id`
const svcWhere = ` AND s.deleted_at IS NULL AND e.deleted_at IS NULL AND p.deleted_at IS NULL`

func scanService(row pgx.Row) (*Service, error) {
	var v Service
	if err := row.Scan(&v.ID, &v.EnvironmentID, &v.ProjectID, &v.OrgID, &v.EnvProtected, &v.Name, &v.Type, &v.SourceType, &v.DBEngine,
		&v.Config, &v.GitManaged, &v.LiveReleaseID, &v.DesiredState, &v.Failover, &v.AllowDataLoss, &v.Status); err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// ListServices lists the services of an environment, with their placements.
func (s *Store) ListServices(ctx context.Context, orgID, envID string) ([]Service, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+svcCols+svcFrom+` WHERE s.environment_id = $1 AND p.org_id = $2`+svcWhere+` ORDER BY s.created_at, s.id`, envID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Service{}
	for rows.Next() {
		var v Service
		if err := rows.Scan(&v.ID, &v.EnvironmentID, &v.ProjectID, &v.OrgID, &v.EnvProtected, &v.Name, &v.Type, &v.SourceType, &v.DBEngine,
			&v.Config, &v.GitManaged, &v.LiveReleaseID, &v.DesiredState, &v.Failover, &v.AllowDataLoss, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	pl, err := s.placements(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Placements = pl[out[i].ID]
	}
	return out, nil
}

// GetService reads a service of the org with its placements.
func (s *Store) GetService(ctx context.Context, orgID, id string) (*Service, error) {
	v, err := scanService(s.Pool.QueryRow(ctx, `SELECT `+svcCols+svcFrom+` WHERE s.id = $1 AND p.org_id = $2`+svcWhere, id, orgID))
	if err != nil {
		return nil, err
	}
	pl, err := s.placements(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	v.Placements = pl[id]
	return v, nil
}

func (s *Store) placements(ctx context.Context, serviceIDs []string) (map[string][]Placement, error) {
	out := map[string][]Placement{}
	if len(serviceIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT service_id, id, node_id, release_id, replica_index, color, role, weight, health,
		COALESCE(container_id, ''), COALESCE(host(ip), ''), created_at
		FROM placements WHERE service_id = ANY($1) ORDER BY created_at, id`, serviceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		var p Placement
		if err := rows.Scan(&sid, &p.ID, &p.NodeID, &p.ReleaseID, &p.ReplicaIndex, &p.Color, &p.Role, &p.Weight, &p.Health, &p.ContainerID, &p.IP, &p.CreatedAt); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], p)
	}
	return out, rows.Err()
}

// NewService is the data for a new service.
type NewService struct {
	EnvironmentID string
	Name          string
	Type          string
	SourceType    string
	Config        json.RawMessage
}

// CreateService adds a service to an environment of the org.
func (s *Store) CreateService(ctx context.Context, orgID string, n NewService) (*Service, error) {
	env, err := s.GetEnvironment(ctx, orgID, n.EnvironmentID)
	if err != nil {
		return nil, err
	}
	id := NewID("svc")
	if _, err := s.Pool.Exec(ctx, `INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, env.ID, n.Name, n.Type, n.SourceType, string(n.Config)); err != nil {
		return nil, err
	}
	return s.GetService(ctx, orgID, id)
}

// UpdateService changes the config and the failover settings. A nil value is not changed.
func (s *Store) UpdateService(ctx context.Context, orgID, id string, config json.RawMessage, failover *string, allowDataLoss *bool) (*Service, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE services SET config = COALESCE($3::jsonb, config), failover = COALESCE($4, failover),
		allow_data_loss_failover = COALESCE($5, allow_data_loss_failover), updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL AND environment_id IN
		  (SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.org_id = $2)`,
		id, orgID, nullJSON(config), failover, allowDataLoss)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetService(ctx, orgID, id)
}

// SoftDeleteService marks a service as deleted and removes its domains.
func (s *Store) SoftDeleteService(ctx context.Context, orgID, id string) error {
	if _, err := s.GetService(ctx, orgID, id); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := softDeleteServices(ctx, tx, `id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Deployment is a deployment with its steps (only when read with GetDeployment).
type Deployment struct {
	ID              string
	ServiceID       string
	EnvironmentID   string
	ReleaseID       *string
	PrevReleaseID   *string
	Trigger         string
	Strategy        string
	StrategyOptions json.RawMessage
	State           string
	CanaryStep      *int
	CanaryPaused    bool
	CanaryGate      json.RawMessage
	FailureReason   *string
	FailureDetail   *string
	ActorID         *string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	Warnings        json.RawMessage
	Steps           []DeploymentStep
}

// DeploymentStep is one state change of a deployment.
type DeploymentStep struct {
	State     string
	Message   *string
	CreatedAt time.Time
}

const depCols = `d.id, d.service_id, d.environment_id, d.release_id, d.previous_release_id, d.trigger, d.strategy, d.strategy_options,
	d.state, d.canary_step, d.canary_paused, d.canary_gate, d.failure_reason, d.failure_detail, d.actor_id, d.started_at, d.finished_at, d.created_at, d.warnings`
const depFrom = ` FROM deployments d JOIN environments e ON e.id = d.environment_id JOIN projects p ON p.id = e.project_id`

func scanDeployment(row pgx.Row) (*Deployment, error) {
	var d Deployment
	if err := row.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.ReleaseID, &d.PrevReleaseID, &d.Trigger, &d.Strategy, &d.StrategyOptions,
		&d.State, &d.CanaryStep, &d.CanaryPaused, &d.CanaryGate, &d.FailureReason, &d.FailureDetail, &d.ActorID, &d.StartedAt, &d.FinishedAt, &d.CreatedAt, &d.Warnings); err != nil {
		return nil, notFound(err)
	}
	return &d, nil
}

// ListDeployments returns one page of deployments of a service, newest first (limit+1 rows at most).
func (s *Store) ListDeployments(ctx context.Context, orgID, serviceID string, limit int, after *Cursor) ([]Deployment, error) {
	var t time.Time
	var id string
	if after != nil {
		t, id = after.Time, after.ID
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+depCols+depFrom+`
		WHERE d.service_id = $1 AND p.org_id = $2 AND ($3::timestamptz IS NULL OR (d.created_at, d.id) < ($3, $4))
		ORDER BY d.created_at DESC, d.id DESC LIMIT $5`, serviceID, orgID, nullTime(after, t), id, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.ServiceID, &d.EnvironmentID, &d.ReleaseID, &d.PrevReleaseID, &d.Trigger, &d.Strategy, &d.StrategyOptions,
			&d.State, &d.CanaryStep, &d.CanaryPaused, &d.CanaryGate, &d.FailureReason, &d.FailureDetail, &d.ActorID, &d.StartedAt, &d.FinishedAt, &d.CreatedAt, &d.Warnings); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDeployment reads a deployment of the org with its steps.
func (s *Store) GetDeployment(ctx context.Context, orgID, id string) (*Deployment, error) {
	d, err := scanDeployment(s.Pool.QueryRow(ctx, `SELECT `+depCols+depFrom+` WHERE d.id = $1 AND p.org_id = $2`, id, orgID))
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT state, message, created_at FROM deployment_steps WHERE deployment_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Steps = []DeploymentStep{}
	for rows.Next() {
		var st DeploymentStep
		if err := rows.Scan(&st.State, &st.Message, &st.CreatedAt); err != nil {
			return nil, err
		}
		d.Steps = append(d.Steps, st)
	}
	return d, rows.Err()
}

// SetConfigSource sets (or with null clears) the Git source of the config file of a project (REQ-CAC-003).
func (s *Store) SetConfigSource(ctx context.Context, orgID, projectID string, source json.RawMessage) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE projects SET config_source = $3::jsonb, updated_at = now() WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		projectID, orgID, nullJSON(source))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
