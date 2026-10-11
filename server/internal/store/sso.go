// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
)

// Errors of SSO.
var (
	ErrSSONoAccount  = errors.New("store: no account matches and users are not created")
	ErrSSODisabled   = errors.New("store: the user is disabled")
	ErrSSOForeignOrg = errors.New("store: the user belongs to another org")
)

// SSOProvider is a row of sso_providers.
type SSOProvider struct {
	ID              string
	OrgID           string
	Kind            string
	Name            string
	IssuerURL       *string
	ClientID        string
	ClientSecretEnc []byte
	Scopes          []string
	GroupsClaim     *string
	AllowedDomains  []string
	DefaultRole     *string
	AutoCreateUsers bool
	Enabled         bool
	CreatedAt       time.Time
}

const ssoCols = `id, org_id, kind, name, issuer_url, client_id, client_secret_enc, scopes, groups_claim, allowed_domains, default_role, auto_create_users, enabled, created_at`

func scanSSO(row pgx.Row) (*SSOProvider, error) {
	var p SSOProvider
	if err := row.Scan(&p.ID, &p.OrgID, &p.Kind, &p.Name, &p.IssuerURL, &p.ClientID, &p.ClientSecretEnc, &p.Scopes, &p.GroupsClaim,
		&p.AllowedDomains, &p.DefaultRole, &p.AutoCreateUsers, &p.Enabled, &p.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

// ssoDiff is the audit diff of a provider. It never holds the secret.
func ssoDiff(p *SSOProvider) map[string]any {
	return map[string]any{"kind": p.Kind, "name": p.Name, "issuer_url": p.IssuerURL, "client_id": p.ClientID, "client_secret": "***",
		"scopes": p.Scopes, "groups_claim": p.GroupsClaim, "allowed_domains": p.AllowedDomains, "default_role": p.DefaultRole,
		"auto_create_users": p.AutoCreateUsers, "enabled": p.Enabled}
}

// ListSSOProviders lists the providers of the org.
func (s *Store) ListSSOProviders(ctx context.Context, orgID string) ([]SSOProvider, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE org_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSOProvider{}
	for rows.Next() {
		p, err := scanSSO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetSSOProvider reads one provider of the org.
func (s *Store) GetSSOProvider(ctx context.Context, orgID, id string) (*SSOProvider, error) {
	return scanSSO(s.Pool.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE org_id = $1 AND id = $2`, orgID, id))
}

// GetSSOProviderByID reads an enabled provider for the login (no org is known yet).
func (s *Store) GetSSOProviderByID(ctx context.Context, id string) (*SSOProvider, error) {
	return scanSSO(s.Pool.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE id = $1 AND enabled`, id))
}

// LoginProvider is what the login page needs to know.
type LoginProvider struct {
	ID, Name, Kind string
	SSORequired    bool
	BreakGlass     bool
}

// ListLoginProviders lists the enabled providers with the org policy, for the login page.
func (s *Store) ListLoginProviders(ctx context.Context) ([]LoginProvider, error) {
	rows, err := s.Pool.Query(ctx, `SELECT p.id, p.name, p.kind, o.sso_required,
			coalesce((o.settings->>'allow_owner_password')::boolean, true)
		FROM sso_providers p JOIN orgs o ON o.id = p.org_id
		WHERE p.enabled AND o.deleted_at IS NULL ORDER BY p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LoginProvider{}
	for rows.Next() {
		var p LoginProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.SSORequired, &p.BreakGlass); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountEnabledSSOProviders counts the enabled providers of the org, other than one id.
func (s *Store) CountEnabledSSOProviders(ctx context.Context, orgID, exceptID string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM sso_providers WHERE org_id = $1 AND enabled AND id <> $2`, orgID, exceptID).Scan(&n)
	return n, err
}

// CreateSSOProvider stores a provider. The secret is already encrypted.
func (s *Store) CreateSSOProvider(ctx context.Context, p *SSOProvider) (*SSOProvider, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if p.ID == "" {
		p.ID = NewID("sso")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sso_providers (id, org_id, kind, name, issuer_url, client_id, client_secret_enc, scopes,
			groups_claim, allowed_domains, default_role, auto_create_users, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.OrgID, p.Kind, p.Name, p.IssuerURL, p.ClientID, p.ClientSecretEnc, p.Scopes, p.GroupsClaim, p.AllowedDomains,
		p.DefaultRole, p.AutoCreateUsers, p.Enabled); err != nil {
		if ok, _ := IsUnique(err); ok {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: p.OrgID, Action: "sso.provider_create", TargetType: "sso_provider", TargetID: p.ID,
		Diff: map[string]any{"after": ssoDiff(p)}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetSSOProvider(ctx, p.OrgID, p.ID)
}

// UpdateSSOProvider replaces the row of a provider with the given values. The caller merges a patch into the
// current row first. A nil ClientSecretEnc keeps the stored secret.
func (s *Store) UpdateSSOProvider(ctx context.Context, p *SSOProvider) (*SSOProvider, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before *SSOProvider
	if before, err = scanSSO(tx.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE org_id = $1 AND id = $2 FOR UPDATE`, p.OrgID, p.ID)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sso_providers SET name=$3, issuer_url=$4, client_id=$5, client_secret_enc=coalesce($6, client_secret_enc),
			scopes=$7, groups_claim=$8, allowed_domains=$9, default_role=$10, auto_create_users=$11, enabled=$12
		WHERE org_id = $1 AND id = $2`,
		p.OrgID, p.ID, p.Name, p.IssuerURL, p.ClientID, p.ClientSecretEnc, p.Scopes, p.GroupsClaim, p.AllowedDomains, p.DefaultRole,
		p.AutoCreateUsers, p.Enabled); err != nil {
		if ok, _ := IsUnique(err); ok {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	after, err := scanSSO(tx.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE id = $1`, p.ID))
	if err != nil {
		return nil, err
	}
	diff := map[string]any{"before": ssoDiff(before), "after": ssoDiff(after)}
	// Another issuer or client is another identity space: the old subjects must not log in to it.
	if !samePtrs(before.IssuerURL, after.IssuerURL) || before.ClientID != after.ClientID {
		if _, err := tx.Exec(ctx, `DELETE FROM sso_identities WHERE provider_id = $1`, p.ID); err != nil {
			return nil, err
		}
		diff["identities_reset"] = true
	}
	if p.ClientSecretEnc != nil {
		diff["client_secret_changed"] = true
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: p.OrgID, Action: "sso.provider_update", TargetType: "sso_provider", TargetID: p.ID, Diff: diff}); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

// DeleteSSOProvider removes a provider with its mappings and identities (the foreign keys cascade).
func (s *Store) DeleteSSOProvider(ctx context.Context, orgID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err := scanSSO(tx.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sso_providers WHERE id = $1`, id); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "sso.provider_delete", TargetType: "sso_provider", TargetID: id,
		Diff: map[string]any{"before": ssoDiff(before)}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GroupMapping is a row of sso_group_mappings.
type GroupMapping struct {
	GroupValue string
	Role       string
	ProjectID  *string
}

// ListGroupMappings lists the mappings of a provider of the org.
func (s *Store) ListGroupMappings(ctx context.Context, orgID, providerID string) ([]GroupMapping, error) {
	if _, err := s.GetSSOProvider(ctx, orgID, providerID); err != nil {
		return nil, err
	}
	return s.mappingsOf(ctx, s.Pool, providerID)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *Store) mappingsOf(ctx context.Context, q querier, providerID string) ([]GroupMapping, error) {
	rows, err := q.Query(ctx, `SELECT group_value, role, project_id FROM sso_group_mappings WHERE sso_provider_id = $1 ORDER BY group_value, project_id NULLS FIRST`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupMapping{}
	for rows.Next() {
		var m GroupMapping
		if err := rows.Scan(&m.GroupValue, &m.Role, &m.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceGroupMappings replaces all mappings of a provider. A project must be in the org (ErrBadReference).
func (s *Store) ReplaceGroupMappings(ctx context.Context, orgID, providerID string, list []GroupMapping) ([]GroupMapping, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := scanSSO(tx.QueryRow(ctx, `SELECT `+ssoCols+` FROM sso_providers WHERE org_id = $1 AND id = $2 FOR UPDATE`, orgID, providerID)); err != nil {
		return nil, err
	}
	before, err := s.mappingsOf(ctx, tx, providerID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sso_group_mappings WHERE sso_provider_id = $1`, providerID); err != nil {
		return nil, err
	}
	for _, m := range list {
		if m.ProjectID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND org_id = $2)`, *m.ProjectID, orgID).Scan(&ok); err != nil {
				return nil, err
			}
			if !ok {
				return nil, ErrBadReference
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sso_group_mappings (id, sso_provider_id, group_value, role, project_id) VALUES ($1,$2,$3,$4,$5)`,
			NewID("sgm"), providerID, m.GroupValue, m.Role, m.ProjectID); err != nil {
			if ok, _ := IsUnique(err); ok {
				return nil, ErrNameTaken
			}
			return nil, err
		}
	}
	after, err := s.mappingsOf(ctx, tx, providerID)
	if err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "sso.mappings_replace", TargetType: "sso_provider", TargetID: providerID,
		Diff: map[string]any{"before": before, "after": after}}); err != nil {
		return nil, err
	}
	return after, tx.Commit(ctx)
}

// SSOLoginInput is a login that passed the provider checks (signature, state, nonce, email, domain).
type SSOLoginInput struct {
	Provider *SSOProvider
	Subject  string
	Email    string
	Name     string
	Groups   []string
	// AllowCreate is false when the provider does not create users, or is not safe to (sso.UnsafeJIT).
	AllowCreate bool
}

// SSOLoginResult says what the login did.
type SSOLoginResult struct {
	UserID  string
	Created bool
	Linked  bool
	// NoAccess is true when no mapping and no default role gives the user a role (the login makes no session).
	NoAccess bool
	OrgRole  string
}

func roleRank(role string) int {
	switch role {
	case "owner":
		return 0
	case "admin":
		return 1
	case "developer":
		return 2
	case "viewer":
		return 3
	}
	return 99
}

// ssoRoles finds the roles the groups give: the highest org role, and the highest role per project.
// With no org role from a mapping, the default role of the provider is used.
func ssoRoles(p *SSOProvider, mappings []GroupMapping, groups []string) (orgRole string, projects map[string]string) {
	projects = map[string]string{}
	has := func(value string) bool {
		for _, g := range groups {
			if g == value || (p.Kind == "github" && strings.EqualFold(g, value)) {
				return true
			}
		}
		return false
	}
	for _, m := range mappings {
		if !has(m.GroupValue) {
			continue
		}
		if m.ProjectID == nil {
			if roleRank(m.Role) < roleRank(orgRole) {
				orgRole = m.Role
			}
		} else if cur, ok := projects[*m.ProjectID]; !ok || roleRank(m.Role) < roleRank(cur) {
			projects[*m.ProjectID] = m.Role
		}
	}
	if orgRole == "" && p.DefaultRole != nil {
		orgRole = *p.DefaultRole
	}
	return orgRole, projects
}

// SSOLogin finds or makes the user of an identity and applies the group mappings, all in one transaction
// (REQ-SSO-002, REQ-SSO-003, REQ-SSO-005). The caller checked the email, the domain and the Google hd claim.
func (s *Store) SSOLogin(ctx context.Context, in SSOLoginInput) (*SSOLoginResult, error) {
	p := in.Provider
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One login at a time per identity, so two tabs cannot make two users.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('sso:' || $1::text || ':' || $2::text, 0))`, p.ID, in.Subject); err != nil {
		return nil, err
	}
	res := &SSOLoginResult{}
	actor := audit.Event{OrgID: p.OrgID, ActorType: audit.ActorSSO, ActorID: p.ID}
	err = tx.QueryRow(ctx, `SELECT user_id FROM sso_identities WHERE provider_id = $1 AND subject = $2`, p.ID, in.Subject).Scan(&res.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1 AND deleted_at IS NULL FOR UPDATE`, in.Email).Scan(&res.UserID)
		switch {
		case err == nil:
			res.Linked = true
		case errors.Is(err, pgx.ErrNoRows):
			if !in.AllowCreate {
				return nil, ErrSSONoAccount
			}
			name := strings.TrimSpace(in.Name)
			if name == "" {
				name, _, _ = strings.Cut(in.Email, "@")
			}
			if len([]rune(name)) > 100 {
				name = string([]rune(name)[:100])
			}
			res.UserID, res.Created = NewID("usr"), true
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, name) VALUES ($1, $2, $3)`, res.UserID, in.Email, name); err != nil {
				return nil, err
			}
			e := actor
			e.Action, e.TargetType, e.TargetID = "sso.user_create", "user", res.UserID
			e.Diff = map[string]any{"email": in.Email, "provider": p.Name}
			if err := audit.Write(ctx, tx, e); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sso_identities (provider_id, subject, user_id) VALUES ($1, $2, $3)`, p.ID, in.Subject, res.UserID); err != nil {
			return nil, err
		}
		if res.Linked {
			e := actor
			e.Action, e.TargetType, e.TargetID = "sso.identity_link", "user", res.UserID
			e.Diff = map[string]any{"provider": p.Name}
			if err := audit.Write(ctx, tx, e); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}

	var disabled bool
	if err := tx.QueryRow(ctx, `SELECT disabled OR deleted_at IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, res.UserID).Scan(&disabled); err != nil {
		return nil, err
	}
	if disabled {
		return nil, ErrSSODisabled
	}

	mappings, err := s.mappingsOf(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	wantRole, wantProjects := ssoRoles(p, mappings, in.Groups)
	if err := s.syncOrgRole(ctx, tx, actor, p.OrgID, res, wantRole); err != nil {
		return nil, err
	}
	if err := s.syncProjectRoles(ctx, tx, actor, p.OrgID, res, wantProjects); err != nil {
		return nil, err
	}
	if res.Created && res.NoAccess {
		// Nobody gave this new user a role: do not keep the account.
		return res, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, res.UserID); err != nil {
		return nil, err
	}
	return res, tx.Commit(ctx)
}

// Commit is skipped above only for a user made by this login who has no access: that transaction ends with
// the deferred rollback, so the user and the identity are not kept.

// syncOrgRole makes the sso membership match the mapped role. A manual membership is not touched. The last
// owner is never removed or demoted by a mapping.
func (s *Store) syncOrgRole(ctx context.Context, tx pgx.Tx, actor audit.Event, orgID string, res *SSOLoginResult, want string) error {
	others, err := lockOwners(ctx, tx, orgID, res.UserID)
	if err != nil {
		return err
	}
	var memOrg, cur, source string
	err = tx.QueryRow(ctx, `SELECT org_id, role, source FROM memberships WHERE user_id = $1 FOR UPDATE`, res.UserID).Scan(&memOrg, &cur, &source)
	hasMem := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if hasMem && memOrg != orgID {
		return ErrSSOForeignOrg
	}
	event := func(action string, before, after any) error {
		e := actor
		e.Action, e.TargetType, e.TargetID = action, "user", res.UserID
		e.Diff = map[string]any{"before": before, "after": after, "source": "sso"}
		return audit.Write(ctx, tx, e)
	}
	switch {
	case hasMem && source == "manual":
		res.OrgRole = cur
	case hasMem && want == "":
		if cur == "owner" && others == 0 {
			res.OrgRole = cur // the org keeps its last owner
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE user_id = $1`, res.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, res.UserID); err != nil {
			return err
		}
		res.NoAccess = true
		return event("sso.access_removed", map[string]any{"role": cur}, nil)
	case hasMem && want != cur:
		if cur == "owner" && others == 0 {
			res.OrgRole = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = $2 WHERE user_id = $1`, res.UserID, want); err != nil {
			return err
		}
		res.OrgRole = want
		return event("role.change", map[string]any{"role": cur}, map[string]any{"role": want})
	case hasMem:
		res.OrgRole = cur
	case want == "":
		res.NoAccess = true
	default:
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (id, org_id, user_id, role, source) VALUES ($1, $2, $3, $4, 'sso')`,
			NewID("mem"), orgID, res.UserID, want); err != nil {
			return err
		}
		res.OrgRole = want
		return event("role.change", nil, map[string]any{"role": want})
	}
	return nil
}

// syncProjectRoles replaces the project roles with source = 'sso' of the user. A project that has a manual role
// of this user is skipped. Without a membership a user has no project roles.
func (s *Store) syncProjectRoles(ctx context.Context, tx pgx.Tx, actor audit.Event, orgID string, res *SSOLoginResult, want map[string]string) error {
	rows, err := tx.Query(ctx, `SELECT project_id, role FROM project_roles WHERE user_id = $1 AND source = 'sso'`, res.UserID)
	if err != nil {
		return err
	}
	before := map[string]string{}
	for rows.Next() {
		var pid, role string
		if err := rows.Scan(&pid, &role); err != nil {
			rows.Close()
			return err
		}
		before[pid] = role
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	after := map[string]string{}
	if !res.NoAccess {
		for pid, role := range want {
			if role == "owner" {
				role = "admin" // a project has no owner role
			}
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL)
				AND NOT EXISTS (SELECT 1 FROM project_roles WHERE project_id = $1 AND user_id = $3 AND source = 'manual')`, pid, orgID, res.UserID).Scan(&ok); err != nil {
				return err
			}
			if ok {
				after[pid] = role
			}
		}
	}
	if reflect.DeepEqual(before, after) {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_roles WHERE user_id = $1 AND source = 'sso'`, res.UserID); err != nil {
		return err
	}
	for pid, role := range after {
		if _, err := tx.Exec(ctx, `INSERT INTO project_roles (id, project_id, user_id, role, source) VALUES ($1, $2, $3, $4, 'sso')`,
			NewID("prl"), pid, res.UserID, role); err != nil {
			return fmt.Errorf("store: sso project role: %w", err)
		}
	}
	e := actor
	e.Action, e.TargetType, e.TargetID = "sso.project_roles", "user", res.UserID
	e.Diff = map[string]any{"before": before, "after": after}
	return audit.Write(ctx, tx, e)
}

func samePtrs(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
