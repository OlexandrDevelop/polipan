// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/backup"
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// Statuses of one item in the result of an apply.
const (
	StatusApplied = "applied"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// ErrInvalidPlan is returned when apply gets a plan with errors.
var ErrInvalidPlan = errors.New("the plan has errors")

// ResultItem is the result of one change.
type ResultItem struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

// Result is what apply did.
type Result struct {
	Items []ResultItem `json:"items"`
	// Deployments are the ids of the deployments that apply queued (trigger config_apply).
	Deployments []string `json:"deployments"`
	// DeployErrors tells why a deployment was not queued. It does not fail the apply.
	DeployErrors []string `json:"deploy_errors,omitempty"`
}

// Failed tells if one item failed.
func (r *Result) Failed() bool {
	for _, i := range r.Items {
		if i.Status == StatusFailed {
			return true
		}
	}
	return false
}

// Options of one apply.
type Options struct {
	OrgID     string
	ProjectID string
	// Prune deletes what the file does not list (REQ-CAC-006). It needs an admin.
	Prune bool
	// PruneData also allows to delete volumes and databases.
	PruneData bool
	// Actor is the user of the audit record. Empty for a push.
	Actor store.Actor
}

// Applier runs a plan against the database and the orchestrator (REQ-CAC-008).
type Applier struct {
	Pool    *pgxpool.Pool
	Store   *store.Store
	Secrets secrets.SecretProvider
	// Backup is the backup service. Without it, backup items fail.
	Backup *backup.Service
	// Deploy queues a deployment. Nil: no deployment is queued.
	Deploy func(ctx context.Context, serviceID, actorID, trigger string) (string, error)
	// DomainsChanged recomputes the ingress after a domain change.
	DomainsChanged func(ctx context.Context, serviceID string) error
	// Removed tells the orchestrator that a service is gone.
	Removed func(ctx context.Context, serviceID string) error
}

type run struct {
	a   *Applier
	opt Options
	pl  *Plan
	res *Result
	// deploy lists the services that need a deployment, by env/name, in order.
	deploy []string
	seen   map[string]bool
}

// Apply runs the changes of a valid plan in order. It stops at the first failure: the next items are skipped.
// Nothing is applied when the plan has errors.
func (a *Applier) Apply(ctx context.Context, pl *Plan, opt Options) (*Result, error) {
	if !pl.Valid {
		return nil, ErrInvalidPlan
	}
	r := &run{a: a, opt: opt, pl: pl, res: &Result{Items: []ResultItem{}, Deployments: []string{}}, seen: map[string]bool{}}
	failed := false
	for i := range pl.Changes {
		c := &pl.Changes[i]
		item := ResultItem{Action: c.Action, Resource: c.Resource, Name: c.Name}
		switch {
		case failed:
			item.Status, item.Message = StatusSkipped, "an earlier item failed"
		default:
			msg, status, err := r.change(ctx, c)
			item.Status, item.Message = status, msg
			if err != nil {
				failed = true
				item.Status, item.Message = StatusFailed, err.Error()
			}
		}
		r.res.Items = append(r.res.Items, item)
	}
	if !failed {
		r.deployAll(ctx)
		r.closeDrift(ctx)
	}
	return r.res, nil
}

func (r *run) change(ctx context.Context, c *Change) (msg, status string, err error) {
	if c.op == nil {
		return "nothing to do", StatusSkipped, nil
	}
	if c.Action == ActionUnmanaged {
		if !r.opt.Prune {
			return "kept: it is not in the file (use prune to delete it)", StatusSkipped, nil
		}
		if c.op.dataLoss && !r.opt.PruneData {
			return "kept: deleting it deletes data (use prune_data)", StatusSkipped, nil
		}
		return r.prune(ctx, c)
	}
	switch c.Resource {
	case ResPreviews:
		err = r.previews(ctx, c)
	case ResEnvironment:
		err = r.environment(ctx, c)
	case ResVariable:
		err = r.variable(ctx, c)
	case ResSecret:
		_, err = r.a.Pool.Exec(ctx, `UPDATE secrets SET preview = preview OR $2, build_time = build_time OR $3, git_managed = true WHERE id = $1`,
			c.op.secret.ID, c.op.ref.Preview, c.op.ref.BuildTime)
	case ResService:
		err = r.service(ctx, c)
	case ResVolume:
		err = r.volume(ctx, c)
	case ResDomain:
		err = r.domain(ctx, c)
	case ResBackupTarget:
		err = r.target(ctx, c)
	case ResBackupPolicy:
		err = r.policy(ctx, c)
	default:
		return "this resource is not applied in this version", StatusSkipped, nil
	}
	if err != nil {
		return "", StatusFailed, err
	}
	return "", StatusApplied, nil
}

// ---- ids ----

func (r *run) envID(ctx context.Context, slug string) (string, error) {
	var id string
	err := r.a.Pool.QueryRow(ctx, `SELECT id FROM environments WHERE project_id = $1 AND slug = $2 AND deleted_at IS NULL`, r.opt.ProjectID, slug).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("the environment %q does not exist", slug)
	}
	return id, nil
}

func (r *run) serviceID(ctx context.Context, env, name string) (string, error) {
	var id string
	err := r.a.Pool.QueryRow(ctx, `SELECT s.id FROM services s JOIN environments e ON e.id = s.environment_id
		WHERE e.project_id = $1 AND e.slug = $2 AND s.name = $3 AND s.deleted_at IS NULL AND e.deleted_at IS NULL`, r.opt.ProjectID, env, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("the service %s/%s does not exist", env, name)
	}
	return id, nil
}

func (r *run) serviceOf(ctx context.Context, env, name string) (*store.Service, error) {
	id, err := r.serviceID(ctx, env, name)
	if err != nil {
		return nil, err
	}
	return r.a.Store.GetService(ctx, r.opt.OrgID, id)
}

func (r *run) needDeploy(env, name string) {
	k := ServiceKey(env, name)
	if !r.seen[k] {
		r.seen[k] = true
		r.deploy = append(r.deploy, k)
	}
}

// ---- project and environments ----

func (r *run) previews(ctx context.Context, c *Change) error {
	p, err := r.a.Store.GetProject(ctx, r.opt.OrgID, r.opt.ProjectID)
	if err != nil {
		return err
	}
	settings := map[string]any{}
	_ = json.Unmarshal(p.Settings, &settings)
	cur, _ := settings["previews"].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	for k, v := range c.op.preview {
		cur[k] = v
	}
	settings["previews"] = cur
	raw, _ := json.Marshal(settings)
	_, err = r.a.Store.UpdateProject(ctx, r.opt.OrgID, r.opt.ProjectID, nil, raw)
	return err
}

func (r *run) environment(ctx context.Context, c *Change) error {
	de := c.op.de
	if c.Action == ActionCreate {
		kind := de.Kind
		if kind == "" {
			kind = "custom"
		}
		protected := kind == "production"
		if de.Protected != nil {
			protected = *de.Protected
		}
		e, err := r.a.Store.CreateEnvironment(ctx, r.opt.OrgID, r.opt.ProjectID, store.NewEnv{Name: de.Slug, Slug: de.Slug, Kind: kind, Protected: protected})
		if err != nil {
			return err
		}
		if de.AutoDeploy != nil {
			raw, _ := json.Marshal(map[string]any{"auto_deploy": *de.AutoDeploy})
			if _, err := r.a.Store.UpdateEnvironment(ctx, r.opt.OrgID, e.ID, nil, nil, raw); err != nil {
				return err
			}
		}
		return r.envVars(ctx, e.ID, de)
	}
	le := c.op.le
	var settings json.RawMessage
	if de.AutoDeploy != nil {
		merged := map[string]any{}
		for k, v := range le.Settings {
			merged[k] = v
		}
		merged["auto_deploy"] = *de.AutoDeploy
		settings, _ = json.Marshal(merged)
	}
	_, err := r.a.Store.UpdateEnvironment(ctx, r.opt.OrgID, le.ID, nil, de.Protected, settings)
	return err
}

// envVars writes the variables of a new environment (the plan leaves them to the environment create).
func (r *run) envVars(ctx context.Context, envID string, de *DesiredEnv) error {
	for _, name := range sortedKeys(de.Vars) {
		if err := r.writeVar(ctx, "environment", envID, "", name, de.Vars[name]); err != nil {
			return err
		}
	}
	return nil
}

// ---- variables ----

func (r *run) writeVar(ctx context.Context, scope, envID, svcID, name string, v DesiredVar) error {
	var env, svc any
	switch scope {
	case "environment":
		env = envID
	case "service":
		env, svc = nil, svcID
	}
	tag, err := r.a.Pool.Exec(ctx, `UPDATE variables SET value = $5, preview = $6, build_time = $7, git_managed = true
		WHERE project_id = $1 AND COALESCE(environment_id,'') = COALESCE($2::text,'') AND COALESCE(service_id,'') = COALESCE($3::text,'') AND name = $4`,
		r.opt.ProjectID, env, svc, name, v.Value, v.Preview, v.BuildTime)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// A secret with this name in the same scope is a conflict.
	_, err = r.a.Pool.Exec(ctx, `INSERT INTO variables (id, project_id, environment_id, service_id, name, value, preview, build_time, git_managed)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true)`, store.NewID("var"), r.opt.ProjectID, env, svc, name, v.Value, v.Preview, v.BuildTime)
	return err
}

func (r *run) variable(ctx context.Context, c *Change) error {
	o := c.op
	var envID, svcID string
	if o.scope != "project" {
		id, err := r.envID(ctx, o.env)
		if err != nil {
			return err
		}
		envID = id
	}
	if o.scope == "service" {
		id, err := r.serviceID(ctx, o.env, serviceOfVar(c.Name))
		if err != nil {
			return err
		}
		svcID = id
		r.needDeploy(o.env, serviceOfVar(c.Name))
	}
	return r.writeVar(ctx, o.scope, envID, svcID, o.item, *o.dvar)
}

// serviceOfVar gets the service name from the label env/service:NAME of a variable change.
func serviceOfVar(label string) string {
	left, _, _ := strings.Cut(label, ":")
	_, name, _ := strings.Cut(left, "/")
	return name
}

// ---- services ----

func mergeConfig(live, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range live {
		out[k] = v
	}
	for k, v := range overlay {
		nv := norm(v)
		if m, ok := nv.(map[string]any); ok && mapKeys[k] {
			cur, _ := out[k].(map[string]any)
			merged := map[string]any{}
			for ck, cv := range cur {
				merged[ck] = cv
			}
			for mk, mv := range m {
				merged[mk] = mv
			}
			out[k] = merged
			continue
		}
		out[k] = nv
	}
	return out
}

func (r *run) setManaged(ctx context.Context, serviceID string, ds *DesiredService) error {
	_, err := r.a.Pool.Exec(ctx, `UPDATE services SET git_managed_fields = $2 WHERE id = $1`, serviceID, nonNil(ds.Managed))
	return err
}

func (r *run) service(ctx context.Context, c *Change) error {
	o := c.op
	ds := o.ds
	if c.Action == ActionUpdate {
		cfg := mergeConfig(o.ls.Config, ds.Overlay)
		raw, _ := json.Marshal(cfg)
		var failover *string
		if ds.Failover != "" {
			failover = &ds.Failover
		}
		if _, err := r.a.Store.UpdateService(ctx, r.opt.OrgID, o.ls.ID, raw, failover, ds.AllowDataLoss); err != nil {
			return err
		}
		if err := r.setManaged(ctx, o.ls.ID, ds); err != nil {
			return err
		}
		if o.deploy && o.ls.HasRelease {
			r.needDeploy(ds.Env, ds.Name)
		}
		return nil
	}
	envID, err := r.envID(ctx, ds.Env)
	if err != nil {
		return err
	}
	cfg := map[string]any{}
	for k, v := range ds.Defaults {
		cfg[k] = v
	}
	cfg = mergeConfig(cfg, ds.Overlay)
	var id string
	if ds.Type == "database" {
		id, err = r.createDatabase(ctx, envID, ds, cfg)
		if err != nil {
			return err
		}
	} else {
		raw, _ := json.Marshal(cfg)
		s, err := r.a.Store.CreateService(ctx, r.opt.OrgID, store.NewService{EnvironmentID: envID, Name: ds.Name, Type: ds.Type, SourceType: ds.SourceType, Config: raw})
		if err != nil {
			return err
		}
		id = s.ID
	}
	var failover *string
	if ds.Failover != "" {
		failover = &ds.Failover
	}
	if failover != nil || ds.AllowDataLoss != nil {
		if _, err := r.a.Store.UpdateService(ctx, r.opt.OrgID, id, nil, failover, ds.AllowDataLoss); err != nil {
			return err
		}
	}
	if err := r.setManaged(ctx, id, ds); err != nil {
		return err
	}
	for _, name := range sortedKeys(ds.Vars) {
		if err := r.writeVar(ctx, "service", envID, id, name, ds.Vars[name]); err != nil {
			return err
		}
	}
	svc, err := r.a.Store.GetService(ctx, r.opt.OrgID, id)
	if err != nil {
		return err
	}
	for _, v := range ds.Volumes {
		if err := r.makeVolume(ctx, svc, v); err != nil {
			return err
		}
	}
	for _, d := range ds.Domains {
		if err := r.makeDomain(ctx, id, d); err != nil {
			return err
		}
	}
	if len(ds.Domains) > 0 {
		r.domainsChanged(ctx, id)
	}
	r.needDeploy(ds.Env, ds.Name)
	return nil
}

func (r *run) createDatabase(ctx context.Context, envID string, ds *DesiredService, cfg map[string]any) (string, error) {
	if ds.DB == nil {
		return "", errors.New("a database needs db.engine")
	}
	eng := dbengine.Get(ds.DB.Engine)
	if eng == nil {
		return "", fmt.Errorf("the database engine %q is not known", ds.DB.Engine)
	}
	spec, err := eng.NewService(ds.DB.Version, "")
	if err != nil {
		return "", err
	}
	for k, v := range cfg {
		spec.Config[k] = v
	}
	spec.Config["strategy"] = "recreate"
	raw, _ := json.Marshal(spec.Config)
	in := store.ServiceInput{Name: ds.Name, Type: "database", DBEngine: eng.Name, DBVersion: spec.Version, Config: raw, Env: spec.Env,
		Volumes: []store.Volume{{Name: "data", Kind: "volume", MountPath: spec.DataPath, AccessMode: "single_writer"}}}
	ids, err := r.a.Store.CreateServices(ctx, r.a.Secrets, r.opt.OrgID, envID, []store.ServiceInput{in}, nil, r.opt.Actor)
	if err != nil {
		return "", err
	}
	return ids[0], nil
}

// ---- volumes ----

func (r *run) makeVolume(ctx context.Context, svc *store.Service, v DesiredVolume) error {
	var limit *int64
	if v.SizeLimit > 0 {
		limit = &v.SizeLimit
	}
	_, err := r.a.Store.CreateVolume(ctx, svc, store.Volume{Name: v.Name, Kind: v.Kind, MountPath: v.Path, SizeLimitBytes: limit, AccessMode: v.AccessMode}, r.opt.Actor)
	if errors.Is(err, store.ErrNameConflict) {
		return nil // a database already has its data volume
	}
	return err
}

func (r *run) volume(ctx context.Context, c *Change) error {
	o := c.op
	svc, err := r.serviceOf(ctx, o.env, o.ds.Name)
	if err != nil {
		return err
	}
	if c.Action == ActionCreate {
		r.needDeploy(o.env, o.ds.Name)
		return r.makeVolume(ctx, svc, *o.dvol)
	}
	var limit *int64
	if o.dvol.SizeLimit > 0 {
		limit = &o.dvol.SizeLimit
	}
	before, err := r.a.Store.GetVolume(ctx, r.opt.OrgID, o.lvol.ID)
	if err != nil {
		return err
	}
	_, err = r.a.Store.UpdateVolume(ctx, svc, before, store.Volume{MountPath: o.dvol.Path, SizeLimitBytes: limit, AccessMode: o.dvol.AccessMode}, r.opt.Actor)
	r.needDeploy(o.env, o.ds.Name)
	return err
}

// ---- domains ----

func (r *run) nodeIDs(ctx context.Context, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := r.a.Pool.Query(ctx, `SELECT id, name FROM nodes WHERE org_id = $1 AND name = ANY($2)`, r.opt.OrgID, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		found[name] = id
	}
	var ids []string
	for _, n := range names {
		id, ok := found[n]
		if !ok {
			return nil, fmt.Errorf("the node %q does not exist", n)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *run) makeDomain(ctx context.Context, serviceID string, d DesiredDomain) error {
	nodes, err := r.nodeIDs(ctx, d.IngressNodes)
	if err != nil {
		return err
	}
	opts, _ := json.Marshal(d.Options)
	var redirect *string
	if d.RedirectWWW != "" {
		redirect = &d.RedirectWWW
	}
	_, err = r.a.Store.CreateDomain(ctx, store.Domain{ServiceID: serviceID, Host: d.Host, PathPrefix: d.PathPrefix, ContainerPort: d.Port,
		HTTPS: d.HTTPS, Options: opts, RedirectWWW: redirect, IngressNodeIDs: nodes})
	return err
}

func (r *run) domainsChanged(ctx context.Context, serviceID string) {
	if r.a.DomainsChanged != nil {
		_ = r.a.DomainsChanged(ctx, serviceID)
	}
}

func (r *run) domain(ctx context.Context, c *Change) error {
	o := c.op
	sid, err := r.serviceID(ctx, o.env, o.ds.Name)
	if err != nil {
		return err
	}
	if c.Action == ActionCreate {
		err = r.makeDomain(ctx, sid, *o.ddom)
	} else {
		var cur *store.Domain
		cur, _, err = r.a.Store.GetDomain(ctx, r.opt.OrgID, o.ldom.ID)
		if err == nil {
			nodes, nerr := r.nodeIDs(ctx, o.ddom.IngressNodes)
			if nerr != nil {
				return nerr
			}
			cur.ContainerPort, cur.HTTPS = o.ddom.Port, o.ddom.HTTPS
			cur.Options, _ = json.Marshal(o.ddom.Options)
			cur.RedirectWWW = nil
			if o.ddom.RedirectWWW != "" {
				cur.RedirectWWW = &o.ddom.RedirectWWW
			}
			cur.IngressNodeIDs = nodes
			_, err = r.a.Store.UpdateDomain(ctx, *cur)
		}
	}
	if err != nil {
		return err
	}
	r.domainsChanged(ctx, sid)
	return nil
}

// ---- backups ----

// credentialsOf reads the value of the secret that holds the credentials of a target: a JSON object.
func (r *run) credentialsOf(ctx context.Context, name string) (map[string]string, error) {
	if name == "" {
		return nil, nil
	}
	var id, projectID string
	var version int
	err := r.a.Pool.QueryRow(ctx, `SELECT id, project_id, current_version FROM secrets WHERE project_id = $1 AND name = $2
		AND environment_id IS NULL AND service_id IS NULL AND deleted_at IS NULL`, r.opt.ProjectID, name).Scan(&id, &projectID, &version)
	if err != nil {
		return nil, fmt.Errorf("the secret %q does not exist", name)
	}
	raw, err := r.a.Secrets.Get(ctx, r.a.Pool, projectID, id, version)
	if err != nil {
		return nil, err
	}
	creds := map[string]string{}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("the secret %q must hold a JSON object with the credentials of the target", name)
	}
	return creds, nil
}

func (r *run) target(ctx context.Context, c *Change) error {
	if r.a.Backup == nil {
		return errors.New("backups are not set up on this server")
	}
	t := c.op.target
	creds, err := r.credentialsOf(ctx, t.CredentialsSecret)
	if err != nil {
		return err
	}
	in := backup.TargetInput{Name: t.Name, Kind: t.Kind, Config: t.Config, Credentials: creds}
	if c.Action == ActionCreate {
		_, err = r.a.Backup.CreateTarget(ctx, r.opt.OrgID, in)
		return err
	}
	_, err = r.a.Backup.UpdateTarget(ctx, r.opt.OrgID, c.op.ltarget.ID, in)
	return err
}

func (r *run) targetIDs(ctx context.Context, names []string) ([]string, error) {
	var ids []string
	for _, n := range names {
		var id string
		err := r.a.Pool.QueryRow(ctx, `SELECT id FROM backup_targets WHERE org_id = $1 AND name = $2`, r.opt.OrgID, n).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("the backup target %q does not exist", n)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *run) policy(ctx context.Context, c *Change) error {
	if r.a.Backup == nil {
		return errors.New("backups are not set up on this server")
	}
	o := c.op
	bk := o.ds.Backup
	sid, err := r.serviceID(ctx, o.env, o.ds.Name)
	if err != nil {
		return err
	}
	in := backup.PolicyInput{}
	if bk.Schedule != "" {
		in.Schedule = &bk.Schedule
	}
	if len(bk.Targets) > 0 {
		ids, err := r.targetIDs(ctx, bk.Targets)
		if err != nil {
			return err
		}
		in.TargetIDs = &ids
	}
	if bk.Retention != nil {
		in.Retention = &bk.Retention
	}
	if rc := bk.RestoreCheck; rc != nil {
		in.CheckEnabled = rc.Enabled
		if rc.Schedule != "" {
			in.CheckSchedule = &rc.Schedule
		}
		if rc.Mode != "" {
			in.CheckMode = &rc.Mode
		}
	}
	if bk.PreHook != nil {
		in.PreHook = &bk.PreHook
	}
	if bk.PostHook != nil {
		in.PostHook = &bk.PostHook
	}
	if c.Action == ActionCreate {
		if bk.Volume != "" {
			var vid string
			err := r.a.Pool.QueryRow(ctx, `SELECT id FROM volumes WHERE service_id = $1 AND name = $2`, sid, bk.Volume).Scan(&vid)
			if err != nil {
				return fmt.Errorf("the volume %q of the service does not exist", bk.Volume)
			}
			in.VolumeID = &vid
		}
		_, err = r.a.Backup.CreatePolicy(ctx, r.opt.OrgID, sid, in)
		return err
	}
	_, err = r.a.Backup.UpdatePolicy(ctx, r.opt.OrgID, o.policy.ID, in)
	return err
}

// ---- prune ----

func (r *run) prune(ctx context.Context, c *Change) (string, string, error) {
	o := c.op
	var err error
	switch c.Resource {
	case ResService:
		if err = r.a.Store.SoftDeleteService(ctx, r.opt.OrgID, o.ls.ID); err == nil && r.a.Removed != nil {
			_ = r.a.Removed(ctx, o.ls.ID)
		}
	case ResVariable:
		_, err = r.a.Pool.Exec(ctx, `DELETE FROM variables WHERE id = $1 AND project_id = $2`, o.lvar.ID, r.opt.ProjectID)
		if o.ls != nil {
			r.needDeploy(o.env, o.ls.Name)
		}
	case ResVolume:
		var v *store.Volume
		if v, err = r.a.Store.GetVolume(ctx, r.opt.OrgID, o.lvol.ID); err == nil {
			err = r.a.Store.DeleteVolume(ctx, r.opt.OrgID, r.opt.ProjectID, v, r.opt.PruneData, r.opt.Actor)
		}
	case ResDomain:
		if err = r.a.Store.DeleteDomain(ctx, o.ldom.ID); err == nil {
			r.domainsChanged(ctx, o.ls.ID)
		}
	default:
		return "kept: this item is never deleted by prune", StatusSkipped, nil
	}
	if err != nil {
		return "", StatusFailed, err
	}
	return "deleted", StatusApplied, nil
}

// ---- deployments and drift ----

func (r *run) deployAll(ctx context.Context) {
	if r.a.Deploy == nil {
		return
	}
	for _, key := range r.deploy {
		env, name, _ := strings.Cut(key, "/")
		id, err := r.serviceID(ctx, env, name)
		if err != nil {
			continue
		}
		dep, err := r.a.Deploy(ctx, id, r.opt.Actor.UserID, "config_apply")
		if err != nil {
			r.res.DeployErrors = append(r.res.DeployErrors, fmt.Sprintf("%s: %v", key, err))
			continue
		}
		r.res.Deployments = append(r.res.Deployments, dep)
	}
}

// closeDrift closes the drift records of the project: the file value is back (REQ-CAC-005).
func (r *run) closeDrift(ctx context.Context) {
	_, _ = r.a.Pool.Exec(ctx, `UPDATE config_drift SET resolved_at = now() WHERE project_id = $1 AND resolved_at IS NULL`, r.opt.ProjectID)
}

