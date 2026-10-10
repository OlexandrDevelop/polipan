// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/secrets"
)

// Variable is a plain variable.
type Variable struct {
	ID            string
	ProjectID     string
	OrgID         string
	EnvironmentID *string
	ServiceID     *string
	Name          string
	Value         string
	Preview       bool
	BuildTime     bool
	GitManaged    bool
	EnvProtected  bool
}

const varCols = `x.id, x.project_id, p.org_id, x.environment_id, x.service_id, x.name, x.value, x.preview, x.build_time, x.git_managed,
	COALESCE(e.protected, false)`
const varFrom = ` FROM variables x JOIN projects p ON p.id = x.project_id LEFT JOIN environments e ON e.id = x.environment_id`

func scanVariable(row pgx.Row) (*Variable, error) {
	var v Variable
	if err := row.Scan(&v.ID, &v.ProjectID, &v.OrgID, &v.EnvironmentID, &v.ServiceID, &v.Name, &v.Value, &v.Preview, &v.BuildTime,
		&v.GitManaged, &v.EnvProtected); err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// ListVariables lists the variables of a project. A filter keeps only the variables of that environment or service.
func (s *Store) ListVariables(ctx context.Context, orgID, projectID string, envID, serviceID *string) ([]Variable, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+varCols+varFrom+`
		WHERE x.project_id = $1 AND p.org_id = $2 AND p.deleted_at IS NULL
		  AND ($3::text IS NULL OR x.environment_id = $3) AND ($4::text IS NULL OR x.service_id = $4)
		ORDER BY x.name, x.id`, projectID, orgID, envID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Variable{}
	for rows.Next() {
		v, err := scanVariable(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetVariable reads a variable of the org.
func (s *Store) GetVariable(ctx context.Context, orgID, id string) (*Variable, error) {
	return scanVariable(s.Pool.QueryRow(ctx, `SELECT `+varCols+varFrom+` WHERE x.id = $1 AND p.org_id = $2 AND p.deleted_at IS NULL`, id, orgID))
}

// NewVariable is the data of a variable write. Nil flags are false for a new variable and not changed for an existing one.
type NewVariable struct {
	Name      string
	Value     string
	Preview   *bool
	BuildTime *bool
}

func variableDiff(v NewVariable) map[string]any {
	return map[string]any{"name": v.Name, "value": v.Value, "preview": v.Preview, "build_time": v.BuildTime}
}

// writeVariable creates a variable or, with upsert, changes the one with the name in the scope.
// It returns the id and true when it created the variable.
func writeVariable(ctx context.Context, tx pgx.Tx, orgID, projectID string, sc *Scope, n NewVariable, upsert bool, a Actor) (string, bool, error) {
	if used, err := nameUsed(ctx, tx, true, projectID, sc.EnvironmentID, sc.ServiceID, n.Name, ""); err != nil || used {
		if err == nil {
			err = ErrNameConflict
		}
		return "", false, err
	}
	var id, before string
	var managed bool
	err := tx.QueryRow(ctx, `SELECT id, value, git_managed FROM variables WHERE project_id = $1 AND COALESCE(environment_id,'') = COALESCE($2,'')
		AND COALESCE(service_id,'') = COALESCE($3,'') AND name = $4 FOR UPDATE`,
		projectID, sc.EnvironmentID, sc.ServiceID, n.Name).Scan(&id, &before, &managed)
	switch {
	case err == nil && !upsert:
		return "", false, ErrNameConflict
	case err == nil && managed:
		return "", false, ErrManagedByGit
	case err == nil:
		if _, err := tx.Exec(ctx, `UPDATE variables SET value = $2, preview = COALESCE($3, preview), build_time = COALESCE($4, build_time) WHERE id = $1`,
			id, n.Value, n.Preview, n.BuildTime); err != nil {
			return "", false, err
		}
		return id, false, secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "variable.update", "variable", id,
			map[string]any{"before": map[string]any{"value": before}, "after": variableDiff(n)}))
	case !errors.Is(err, pgx.ErrNoRows):
		return "", false, err
	}
	id = NewID("var")
	if _, err := tx.Exec(ctx, `INSERT INTO variables (id, project_id, environment_id, service_id, name, value, preview, build_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, projectID, sc.EnvironmentID, sc.ServiceID, n.Name, n.Value, deref(n.Preview), deref(n.BuildTime)); err != nil {
		if u, _ := IsUnique(err); u {
			return "", false, ErrNameConflict
		}
		return "", false, err
	}
	return id, true, secrets.WriteAudit(ctx, tx, a.audit(orgID, projectID, "variable.create", "variable", id, map[string]any{"after": variableDiff(n)}))
}

// CreateVariable makes a variable. A cycle of references gives *vars.CycleError (REQ-ENV-005).
func (s *Store) CreateVariable(ctx context.Context, orgID, projectID string, sc *Scope, n NewVariable, a Actor) (*Variable, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, projectID); err != nil {
		return nil, err
	}
	id, _, err := writeVariable(ctx, tx, orgID, projectID, sc, n, false, a)
	if err != nil {
		return nil, err
	}
	if err := checkCycles(ctx, tx, projectID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetVariable(ctx, orgID, id)
}

// UpdateVariable changes the name, the value and the flags of a variable.
func (s *Store) UpdateVariable(ctx context.Context, orgID, id string, n NewVariable, a Actor) (*Variable, error) {
	cur, err := s.GetVariable(ctx, orgID, id)
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
	if used, err := nameUsed(ctx, tx, true, cur.ProjectID, cur.EnvironmentID, cur.ServiceID, n.Name, ""); err != nil || used {
		if err == nil {
			err = ErrNameConflict
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE variables SET name = $2, value = $3, preview = COALESCE($4, preview), build_time = COALESCE($5, build_time)
		WHERE id = $1`, id, n.Name, n.Value, n.Preview, n.BuildTime); err != nil {
		if u, _ := IsUnique(err); u {
			return nil, ErrNameConflict
		}
		return nil, err
	}
	if err := checkCycles(ctx, tx, cur.ProjectID); err != nil {
		return nil, err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "variable.update", "variable", id,
		map[string]any{"before": map[string]any{"name": cur.Name, "value": cur.Value}, "after": variableDiff(n)})); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetVariable(ctx, orgID, id)
}

// DeleteVariable removes a variable.
func (s *Store) DeleteVariable(ctx context.Context, orgID, id string, a Actor) error {
	cur, err := s.GetVariable(ctx, orgID, id)
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
	if _, err := tx.Exec(ctx, `DELETE FROM variables WHERE id = $1`, id); err != nil {
		return err
	}
	if err := secrets.WriteAudit(ctx, tx, a.audit(orgID, cur.ProjectID, "variable.delete", "variable", id,
		map[string]any{"before": map[string]any{"name": cur.Name, "value": cur.Value}})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BulkItem is one item of a bulk import.
type BulkItem struct {
	Name      string
	Value     string
	Secret    bool
	Preview   *bool
	BuildTime *bool
}

// BulkSet creates or changes many variables and secrets in one transaction (REQ-ENV-006).
func (s *Store) BulkSet(ctx context.Context, p secrets.SecretProvider, orgID, projectID string, sc *Scope, items []BulkItem, a Actor) (created, updated int, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockProject(ctx, tx, projectID); err != nil {
		return 0, 0, err
	}
	for _, it := range items {
		var isNew bool
		if it.Secret {
			_, isNew, err = writeSecret(ctx, tx, p, orgID, projectID, sc, NewSecret{Name: it.Name, Value: it.Value, Preview: it.Preview, BuildTime: it.BuildTime}, true, a)
		} else {
			_, isNew, err = writeVariable(ctx, tx, orgID, projectID, sc, NewVariable{Name: it.Name, Value: it.Value, Preview: it.Preview, BuildTime: it.BuildTime}, true, a)
		}
		if err != nil {
			return 0, 0, err
		}
		if isNew {
			created++
		} else {
			updated++
		}
	}
	if err := checkCycles(ctx, tx, projectID); err != nil {
		return 0, 0, err
	}
	return created, updated, tx.Commit(ctx)
}
