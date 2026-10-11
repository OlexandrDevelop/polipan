// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"

	"github.com/polipan/polipan/server/internal/audit"
)

// SoftDeleteOrg marks the org, its projects, environments and services as deleted. A purge job removes the rows
// after 30 days. Sessions end and API tokens are revoked at once. It returns the ids of the services, so the
// caller can remove their containers.
func (s *Store) SoftDeleteOrg(ctx context.Context, orgID string) ([]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var slug string
	err = tx.QueryRow(ctx, `UPDATE orgs SET deleted_at = now(), slug = slug`+deletedSuffix+`, updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL RETURNING slug`, orgID).Scan(&slug)
	if err != nil {
		return nil, notFound(err)
	}
	ids, err := softDeleteServices(ctx, tx, `environment_id IN (SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.org_id = $1)`, orgID)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{
		`UPDATE environments SET deleted_at = now(), slug = slug` + deletedSuffix + ` WHERE deleted_at IS NULL AND project_id IN (SELECT id FROM projects WHERE org_id = $1)`,
		`UPDATE projects SET deleted_at = now(), slug = slug` + deletedSuffix + ` WHERE org_id = $1 AND deleted_at IS NULL`,
		`DELETE FROM sessions WHERE user_id IN (SELECT user_id FROM memberships WHERE org_id = $1)`,
		`UPDATE api_tokens SET revoked_at = now() WHERE org_id = $1 AND revoked_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, q, orgID); err != nil {
			return nil, err
		}
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "org.delete", TargetType: "org", TargetID: orgID,
		Diff: map[string]any{"services": len(ids)}}); err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
}

// MoveDomain gives a domain to another service in one statement. port is the new container port, or 0 to keep it.
// It returns the domain and the id of the service it left.
func (s *Store) MoveDomain(ctx context.Context, orgID, id, targetServiceID string, port int) (*Domain, string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var from string
	err = tx.QueryRow(ctx, `SELECT d.service_id FROM domains d JOIN services s ON s.id = d.service_id JOIN environments e ON e.id = s.environment_id
		JOIN projects p ON p.id = e.project_id WHERE d.id = $1 AND p.org_id = $2`+domWhere+` FOR UPDATE OF d`, id, orgID).Scan(&from)
	if err != nil {
		return nil, "", notFound(err)
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM services s JOIN environments e ON e.id = s.environment_id
		JOIN projects p ON p.id = e.project_id WHERE s.id = $1 AND p.org_id = $2`+domWhere+`)`, targetServiceID, orgID).Scan(&ok); err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE domains SET service_id = $2, container_port = CASE WHEN $3 > 0 THEN $3 ELSE container_port END WHERE id = $1`,
		id, targetServiceID, port); err != nil {
		return nil, "", err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "domain.move", TargetType: "domain", TargetID: id,
		Diff: map[string]any{"from_service": from, "to_service": targetServiceID}}); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	d, _, err := s.getDomainByID(ctx, id)
	return d, from, err
}
