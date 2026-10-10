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
