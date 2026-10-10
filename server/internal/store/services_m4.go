// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/secrets"
)

// Errors of the creation of services in a group.
var (
	ErrServiceNameUsed = errors.New("store: a service with this name exists in the environment")
	ErrDomainUsed      = errors.New("store: a domain of the group is used already")
)

// ServiceInput is one service of a group that is created in one transaction (a database, or a template).
type ServiceInput struct {
	ID        string // optional, made when empty
	Name      string
	Type      string // app | database | worker
	DBEngine  string
	DBVersion string
	Config    json.RawMessage
	Env       []dbengine.EnvEntry // variables and secrets of the service scope
	Volumes   []Volume
	Domains   []Domain
}

// InstanceInput is the template instance of a group.
type InstanceInput struct {
	TemplateID, Version string
	Inputs              json.RawMessage
}

// DomainUsedError tells which host was used.
type DomainUsedError struct{ Host string }

func (e *DomainUsedError) Error() string { return "domain " + e.Host + " is in use" }
func (e *DomainUsedError) Unwrap() error { return ErrDomainUsed }

// CreateServices creates services with their variables, secrets, volumes and domains, and the template
// instance row, in one transaction. If one thing fails nothing is created (REQ-TPL-003). The services are
// image services. It returns the ids in the order of the input.
func (s *Store) CreateServices(ctx context.Context, p secrets.SecretProvider, orgID, environmentID string, in []ServiceInput, inst *InstanceInput, a Actor) ([]string, error) {
	env, err := s.GetEnvironment(ctx, orgID, environmentID)
	if err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, env.ProjectID); err != nil {
		return nil, err
	}
	var instanceID *string
	if inst != nil {
		id := NewID("tpi")
		instanceID = &id
		raw := inst.Inputs
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO template_instances (id, template_id, template_version, environment_id, inputs) VALUES ($1,$2,$3,$4,$5)`,
			id, inst.TemplateID, inst.Version, env.ID, string(raw)); err != nil {
			return nil, err
		}
	}
	ids := make([]string, len(in))
	for i := range in {
		sv := &in[i]
		if sv.ID == "" {
			sv.ID = NewID("svc")
		}
		ids[i] = sv.ID
		var engine, version *string
		if sv.DBEngine != "" {
			engine, version = &sv.DBEngine, &sv.DBVersion
		}
		if _, err := tx.Exec(ctx, `INSERT INTO services (id, environment_id, name, type, source_type, config, template_instance_id, db_engine, db_version)
			VALUES ($1,$2,$3,$4,'image',$5,$6,$7,$8)`, sv.ID, env.ID, sv.Name, sv.Type, string(sv.Config), instanceID, engine, version); err != nil {
			if u, _ := IsUnique(err); u {
				return nil, ErrServiceNameUsed
			}
			return nil, err
		}
		sc := &Scope{EnvironmentID: &env.ID, ServiceID: &sv.ID, Protected: env.Protected}
		for _, e := range sv.Env {
			if e.Secret {
				if _, _, err := writeSecret(ctx, tx, p, orgID, env.ProjectID, sc, NewSecret{Name: e.Name, Value: e.Value}, false, a); err != nil {
					return nil, err
				}
			} else if _, _, err := writeVariable(ctx, tx, orgID, env.ProjectID, sc, NewVariable{Name: e.Name, Value: e.Value}, false, a); err != nil {
				return nil, err
			}
		}
		for _, v := range sv.Volumes {
			if v.AccessMode == "" {
				v.AccessMode = "single_writer"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO volumes (id, service_id, name, kind, mount_path, size_limit_bytes, access_mode) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				NewID("vol"), sv.ID, v.Name, v.Kind, v.MountPath, v.SizeLimitBytes, v.AccessMode); err != nil {
				return nil, err
			}
		}
		for _, d := range sv.Domains {
			if _, err := tx.Exec(ctx, `INSERT INTO domains (id, service_id, host, path_prefix, container_port, https, redirect_www) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				NewID("dom"), sv.ID, d.Host, d.PathPrefix, d.ContainerPort, d.HTTPS, d.RedirectWWW); err != nil {
				if u, _ := IsUnique(err); u {
					return nil, &DomainUsedError{Host: d.Host}
				}
				return nil, err
			}
		}
		if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, env.ProjectID, "service.create", "service", sv.ID,
			map[string]any{"after": map[string]any{"name": sv.Name, "type": sv.Type, "db_engine": sv.DBEngine, "password": maskIf(sv.DBEngine != "")}})); err != nil {
			return nil, err
		}
	}
	if err := checkCycles(ctx, tx, env.ProjectID); err != nil {
		return nil, err
	}
	if inst != nil {
		if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, env.ProjectID, "template.instantiate", "template_instance", *instanceID,
			map[string]any{"template": inst.TemplateID, "version": inst.Version, "services": ids})); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func maskIf(b bool) any {
	if b {
		return mask.Replacement
	}
	return nil
}

// ServiceBackupSkip records that the user skipped the backup suggestion of a database (REQ-DB-006).
func (s *Store) SetBackupSkipped(ctx context.Context, orgID, serviceID string, skipped bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE services SET backup_skipped = $3, updated_at = now() WHERE id = $1 AND deleted_at IS NULL
		AND environment_id IN (SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.org_id = $2)`, serviceID, orgID, skipped)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ServiceM4 are the M4 columns of a service.
type ServiceM4 struct {
	DBVersion         *string
	BackupSkipped     bool
	TemplateInstance  *string
	TemplateSlug      *string
	HasBackupPolicies bool
}

// GetServiceM4 reads the M4 columns of a service.
func (s *Store) GetServiceM4(ctx context.Context, serviceID string) (*ServiceM4, error) {
	var m ServiceM4
	err := s.Pool.QueryRow(ctx, `SELECT s.db_version, s.backup_skipped, s.template_instance_id,
		(SELECT i.template_id FROM template_instances i WHERE i.id = s.template_instance_id),
		EXISTS (SELECT 1 FROM backup_policies bp WHERE bp.service_id = s.id) FROM services s WHERE s.id = $1`, serviceID).
		Scan(&m.DBVersion, &m.BackupSkipped, &m.TemplateInstance, &m.TemplateSlug, &m.HasBackupPolicies)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}
