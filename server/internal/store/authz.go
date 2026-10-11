// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AuthScope is the place of the object of a request: its project and whether its environment is protected.
type AuthScope struct {
	ProjectID string
	Protected bool
}

// scopeQueries finds the project and the protected flag from the id in a path. Every query takes
// the id as $1 and the org as $2, so an object of another org is not found.
var scopeQueries = map[string]string{
	"project_id": `SELECT p.id, false FROM projects p WHERE p.id = $1 AND p.org_id = $2`,
	"environment_id": `SELECT e.project_id, e.protected FROM environments e JOIN projects p ON p.id = e.project_id
		WHERE e.id = $1 AND p.org_id = $2`,
	"service_id": `SELECT e.project_id, e.protected FROM services s JOIN environments e ON e.id = s.environment_id
		JOIN projects p ON p.id = e.project_id WHERE s.id = $1 AND p.org_id = $2`,
	"deployment_id": `SELECT e.project_id, e.protected FROM deployments d JOIN environments e ON e.id = d.environment_id
		JOIN projects p ON p.id = e.project_id WHERE d.id = $1 AND p.org_id = $2`,
	"release_id": `SELECT e.project_id, e.protected FROM releases r JOIN services s ON s.id = r.service_id
		JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE r.id = $1 AND p.org_id = $2`,
	"domain_id": `SELECT e.project_id, e.protected FROM domains d JOIN services s ON s.id = d.service_id
		JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE d.id = $1 AND p.org_id = $2`,
	"volume_id": `SELECT e.project_id, e.protected FROM volumes v JOIN services s ON s.id = v.service_id
		JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE v.id = $1 AND p.org_id = $2`,
	"variable_id": `SELECT v.project_id, coalesce(e.protected, false) FROM variables v JOIN projects p ON p.id = v.project_id
		LEFT JOIN environments e ON e.id = v.environment_id WHERE v.id = $1 AND p.org_id = $2`,
	"secret_id": `SELECT v.project_id, coalesce(e.protected, false) FROM secrets v JOIN projects p ON p.id = v.project_id
		LEFT JOIN environments e ON e.id = v.environment_id WHERE v.id = $1 AND p.org_id = $2`,
	"instance_id": `SELECT e.project_id, e.protected FROM template_instances t JOIN environments e ON e.id = t.environment_id
		JOIN projects p ON p.id = e.project_id WHERE t.id = $1 AND p.org_id = $2`,
	"policy_id": `SELECT e.project_id, e.protected FROM backup_policies b JOIN services s ON s.id = b.service_id
		JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE b.id = $1 AND p.org_id = $2`,
}

// ScopeOf finds the project of the object a path parameter points to. found is false for a parameter
// that has no project (a node, a user) and for an object that does not exist: the org role then decides.
func (s *Store) ScopeOf(ctx context.Context, orgID, param, id string) (sc AuthScope, found bool, err error) {
	q, ok := scopeQueries[param]
	if !ok {
		return AuthScope{}, false, nil
	}
	err = s.Pool.QueryRow(ctx, q, id, orgID).Scan(&sc.ProjectID, &sc.Protected)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthScope{}, false, nil
	}
	return sc, err == nil, err
}

// ProjectRoleNames returns the project roles of a user: the roles given to the user and to the teams of the user.
func (s *Store) ProjectRoleNames(ctx context.Context, projectID, userID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT role FROM project_roles WHERE project_id = $1
		AND (user_id = $2 OR team_id IN (SELECT team_id FROM team_members WHERE user_id = $2))`, projectID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserProjectRole is a project role of a user, direct or through a team.
type UserProjectRole struct {
	ProjectID string
	Role      string
}

// ProjectRolesOf lists the project roles that apply to a user (for GET /me).
func (s *Store) ProjectRolesOf(ctx context.Context, userID string) ([]UserProjectRole, error) {
	rows, err := s.Pool.Query(ctx, `SELECT project_id, role FROM project_roles
		WHERE user_id = $1 OR team_id IN (SELECT team_id FROM team_members WHERE user_id = $1) ORDER BY project_id, role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserProjectRole
	for rows.Next() {
		var r UserProjectRole
		if err := rows.Scan(&r.ProjectID, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// APIToken is a token without its value.
type APIToken struct {
	ID         string
	OrgID      string
	UserID     string
	UserEmail  string
	Name       string
	Prefix     string
	Scopes     []string
	ProjectID  *string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

const tokenCols = `t.id, t.org_id, t.user_id, u.email, t.name, t.token_prefix, t.scopes, t.project_id, t.expires_at, t.last_used_at, t.revoked_at, t.created_at`

func scanToken(row pgx.Row) (*APIToken, error) {
	var t APIToken
	if err := row.Scan(&t.ID, &t.OrgID, &t.UserID, &t.UserEmail, &t.Name, &t.Prefix, &t.Scopes, &t.ProjectID, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// TokenAuth is a token with the user it belongs to, for the login of a request.
type TokenAuth struct {
	Token APIToken
	User  User
}

// GetTokenByHash finds a token that is not revoked and not expired, of a user who is active.
func (s *Store) GetTokenByHash(ctx context.Context, hash string, now time.Time) (*TokenAuth, error) {
	var ta TokenAuth
	t, u := &ta.Token, &ta.User
	err := s.Pool.QueryRow(ctx, `SELECT `+tokenCols+`, u.name, u.password_hash, u.totp_enabled, u.disabled, u.created_at, m.role,
			(coalesce((o.settings->>'require_2fa')::boolean, false) AND u.password_hash IS NOT NULL)
		FROM api_tokens t JOIN users u ON u.id = t.user_id JOIN memberships m ON m.user_id = u.id AND m.org_id = t.org_id
		JOIN orgs o ON o.id = t.org_id
		WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > $2)
		AND u.deleted_at IS NULL AND NOT u.disabled LIMIT 1`, hash, now).
		Scan(&t.ID, &t.OrgID, &t.UserID, &t.UserEmail, &t.Name, &t.Prefix, &t.Scopes, &t.ProjectID, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt,
			&u.Name, &u.PasswordHash, &u.TotpEnabled, &u.Disabled, &u.CreatedAt, &u.Role, &u.Require2FA)
	if err != nil {
		return nil, notFound(err)
	}
	u.ID, u.Email, u.OrgID = t.UserID, t.UserEmail, t.OrgID
	return &ta, nil
}

// TouchToken stores the last use of a token, at most once a minute.
func (s *Store) TouchToken(ctx context.Context, id string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2::timestamptz WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, now)
	return err
}
