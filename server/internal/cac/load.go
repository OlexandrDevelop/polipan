// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

func jsonMap(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// Load reads the live state of a project (not deleted rows only).
func Load(ctx context.Context, pool *pgxpool.Pool, orgID, projectID string) (*Live, error) {
	l := &Live{ProjectID: projectID, Envs: map[string]*LiveEnv{}, ProjectVars: map[string]LiveVar{},
		Targets: map[string]*LiveTarget{}, Connections: map[string]string{}}
	var settings, source []byte
	if err := pool.QueryRow(ctx, `SELECT slug, settings, config_source FROM projects WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`,
		projectID, orgID).Scan(&l.Slug, &settings, &source); err != nil {
		return nil, err
	}
	l.Settings, l.ConfigSource = jsonMap(settings), jsonMap(source)

	byEnvID := map[string]*LiveEnv{}
	rows, err := pool.Query(ctx, `SELECT id, slug, name, kind, protected, settings FROM environments WHERE project_id = $1 AND deleted_at IS NULL ORDER BY slug`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		e := &LiveEnv{Services: map[string]*LiveService{}, Vars: map[string]LiveVar{}}
		var st []byte
		if err := rows.Scan(&e.ID, &e.Slug, &e.Name, &e.Kind, &e.Protected, &st); err != nil {
			rows.Close()
			return nil, err
		}
		e.Settings = jsonMap(st)
		l.Envs[e.Slug], byEnvID[e.ID] = e, e
	}
	rows.Close()

	bySvcID := map[string]*LiveService{}
	svcEnv := map[string]*LiveEnv{}
	rows, err = pool.Query(ctx, `SELECT s.id, s.environment_id, s.name, s.type, s.source_type, COALESCE(s.db_engine,''), s.config, s.failover,
		s.allow_data_loss_failover, s.git_managed_fields, s.live_release_id IS NOT NULL
		FROM services s JOIN environments e ON e.id = s.environment_id
		WHERE e.project_id = $1 AND s.deleted_at IS NULL AND e.deleted_at IS NULL ORDER BY s.name, s.id`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		s := &LiveService{Vars: map[string]LiveVar{}}
		var envID string
		var cfg []byte
		if err := rows.Scan(&s.ID, &envID, &s.Name, &s.Type, &s.SourceType, &s.DBEngine, &cfg, &s.Failover, &s.AllowDataLoss, &s.Managed, &s.HasRelease); err != nil {
			rows.Close()
			return nil, err
		}
		s.Config = jsonMap(cfg)
		if e := byEnvID[envID]; e != nil {
			e.Services[s.Name] = s
			bySvcID[s.ID], svcEnv[s.ID] = s, e
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT d.id, d.service_id, d.host, d.path_prefix, d.container_port, d.https, d.options, COALESCE(d.redirect_www,'')
		FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id
		WHERE e.project_id = $1 AND s.deleted_at IS NULL ORDER BY d.host, d.path_prefix`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d LiveDomain
		var sid string
		var opts []byte
		if err := rows.Scan(&d.ID, &sid, &d.Host, &d.PathPrefix, &d.Port, &d.HTTPS, &opts, &d.RedirectWWW); err != nil {
			rows.Close()
			return nil, err
		}
		d.Options = jsonMap(opts)
		if s := bySvcID[sid]; s != nil {
			s.Domains = append(s.Domains, d)
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT v.id, v.service_id, v.name, v.kind, v.mount_path, v.access_mode, COALESCE(v.size_limit_bytes,0), COALESCE(v.node_id,'')
		FROM volumes v JOIN services s ON s.id = v.service_id JOIN environments e ON e.id = s.environment_id
		WHERE e.project_id = $1 AND s.deleted_at IS NULL ORDER BY v.name`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v LiveVolume
		var sid string
		if err := rows.Scan(&v.ID, &sid, &v.Name, &v.Kind, &v.Path, &v.AccessMode, &v.SizeLimit, &v.Node); err != nil {
			rows.Close()
			return nil, err
		}
		if s := bySvcID[sid]; s != nil {
			s.Volumes = append(s.Volumes, v)
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT id, COALESCE(environment_id,''), COALESCE(service_id,''), name, value, preview, build_time, git_managed
		FROM variables WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, envID, svcID, name string
		v := LiveVar{}
		if err := rows.Scan(&id, &envID, &svcID, &name, &v.Value, &v.Preview, &v.BuildTime, &v.Managed); err != nil {
			rows.Close()
			return nil, err
		}
		v.ID = id
		switch {
		case svcID != "":
			if s := bySvcID[svcID]; s != nil {
				s.Vars[name] = v
			}
		case envID != "":
			if e := byEnvID[envID]; e != nil {
				e.Vars[name] = v
			}
		default:
			l.ProjectVars[name] = v
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT id, name, COALESCE(environment_id,''), COALESCE(service_id,''), preview, build_time, git_managed
		FROM secrets WHERE project_id = $1 AND deleted_at IS NULL ORDER BY name, id`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s LiveSecret
		if err := rows.Scan(&s.ID, &s.Name, &s.EnvID, &s.ServiceID, &s.Preview, &s.BuildTime, &s.Managed); err != nil {
			rows.Close()
			return nil, err
		}
		l.Secrets = append(l.Secrets, s)
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT id, name, kind, config FROM backup_targets WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		t := &LiveTarget{}
		var cfg []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &cfg); err != nil {
			rows.Close()
			return nil, err
		}
		t.Config = jsonMap(cfg)
		l.Targets[t.Name] = t
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT p.id, p.service_id, p.schedule, p.target_ids, p.retention, p.restore_check_enabled, p.restore_check_schedule,
		p.restore_check_mode, COALESCE(p.volume_id,''), p.hooks
		FROM backup_policies p JOIN services s ON s.id = p.service_id JOIN environments e ON e.id = s.environment_id
		WHERE e.project_id = $1 AND s.deleted_at IS NULL ORDER BY p.created_at, p.id`, projectID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p LivePolicy
		var sid string
		var ret, hooks []byte
		if err := rows.Scan(&p.ID, &sid, &p.Schedule, &p.TargetIDs, &ret, &p.CheckEnabled, &p.CheckSchedule, &p.CheckMode, &p.VolumeID, &hooks); err != nil {
			rows.Close()
			return nil, err
		}
		p.Retention = map[string]int{}
		_ = json.Unmarshal(ret, &p.Retention)
		var h struct {
			Pre  []string `json:"pre"`
			Post []string `json:"post"`
		}
		_ = json.Unmarshal(hooks, &h)
		p.PreHook, p.PostHook = h.Pre, h.Post
		if s := bySvcID[sid]; s != nil {
			s.Policies = append(s.Policies, p)
		}
	}
	rows.Close()

	rows, err = pool.Query(ctx, `SELECT name, id FROM git_connections WHERE org_id = $1`, orgID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			rows.Close()
			return nil, err
		}
		l.Connections[name] = id
	}
	rows.Close()
	_ = svcEnv
	return l, nil
}
