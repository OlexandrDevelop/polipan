// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"time"
)

// CustomTemplate is a template that an org added (the built-in ones are in the code).
type CustomTemplate struct {
	ID        string
	Slug      string
	Version   string
	Manifest  json.RawMessage
	CreatedAt time.Time
}

// ListCustomTemplates returns the latest version of every custom template of the org.
func (s *Store) ListCustomTemplates(ctx context.Context, orgID string) ([]CustomTemplate, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ON (slug) id, slug, version, manifest, created_at FROM templates
		WHERE org_id = $1 ORDER BY slug, created_at DESC, id DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomTemplate{}
	for rows.Next() {
		var t CustomTemplate
		if err := rows.Scan(&t.ID, &t.Slug, &t.Version, &t.Manifest, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetCustomTemplate returns the latest version of a custom template.
func (s *Store) GetCustomTemplate(ctx context.Context, orgID, slug string) (*CustomTemplate, error) {
	var t CustomTemplate
	err := s.Pool.QueryRow(ctx, `SELECT id, slug, version, manifest, created_at FROM templates WHERE org_id = $1 AND slug = $2
		ORDER BY created_at DESC, id DESC LIMIT 1`, orgID, slug).Scan(&t.ID, &t.Slug, &t.Version, &t.Manifest, &t.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// CreateCustomTemplate adds a custom template. The same slug and version is a unique violation (409).
func (s *Store) CreateCustomTemplate(ctx context.Context, orgID, slug, version string, manifest json.RawMessage) (*CustomTemplate, error) {
	t := CustomTemplate{ID: NewID("tem"), Slug: slug, Version: version, Manifest: manifest}
	err := s.Pool.QueryRow(ctx, `INSERT INTO templates (id, org_id, slug, version, manifest) VALUES ($1,$2,$3,$4,$5) RETURNING created_at`,
		t.ID, orgID, slug, version, string(manifest)).Scan(&t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// TemplateInstance is a group of services made by a template.
type TemplateInstance struct {
	ID            string
	TemplateID    string
	Version       string
	EnvironmentID string
	ProjectID     string
	OrgID         string
	Protected     bool
	ServiceIDs    []string
}

// GetTemplateInstance reads an instance of the org with the ids of its services that are not deleted.
func (s *Store) GetTemplateInstance(ctx context.Context, orgID, id string) (*TemplateInstance, error) {
	var ti TemplateInstance
	err := s.Pool.QueryRow(ctx, `SELECT i.id, i.template_id, i.template_version, i.environment_id, e.project_id, p.org_id, e.protected
		FROM template_instances i JOIN environments e ON e.id = i.environment_id JOIN projects p ON p.id = e.project_id
		WHERE i.id = $1 AND p.org_id = $2 AND e.deleted_at IS NULL AND p.deleted_at IS NULL`, id, orgID).
		Scan(&ti.ID, &ti.TemplateID, &ti.Version, &ti.EnvironmentID, &ti.ProjectID, &ti.OrgID, &ti.Protected)
	if err != nil {
		return nil, notFound(err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT id FROM services WHERE template_instance_id = $1 AND deleted_at IS NULL ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		ti.ServiceIDs = append(ti.ServiceIDs, sid)
	}
	return &ti, rows.Err()
}

// DeleteTemplateInstance soft deletes all services of an instance in one transaction.
func (s *Store) DeleteTemplateInstance(ctx context.Context, orgID, id string) ([]string, error) {
	if _, err := s.GetTemplateInstance(ctx, orgID, id); err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ids, err := softDeleteServices(ctx, tx, `template_instance_id = $1`, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}
