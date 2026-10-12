// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
)

// Errors of members, teams and tokens.
var (
	ErrLastOwner     = errors.New("store: the org must keep one active owner")
	ErrOwnerProtect  = errors.New("store: only an owner can change an owner")
	ErrAlreadyMember = errors.New("store: this email is already a member")
	ErrBadReference  = errors.New("store: a user, team or project is not in this org")
	ErrNameTaken     = errors.New("store: the name is taken")
)

// Invitation is an open invitation.
type Invitation struct {
	ID        string
	Email     string
	Role      string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// InvitationTTL is the life of an invitation link (REQ-RBAC-006).
const InvitationTTL = 7 * 24 * time.Hour

// ListInvitations lists the invitations that are not accepted and not expired.
func (s *Store) ListInvitations(ctx context.Context, orgID string, now time.Time) ([]Invitation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, email, role, expires_at, created_at FROM invitations
		WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > $2 ORDER BY created_at, id`, orgID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if err := rows.Scan(&i.ID, &i.Email, &i.Role, &i.ExpiresAt, &i.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CreateInvitation stores an invitation. An older open invitation for the same email is replaced.
func (s *Store) CreateInvitation(ctx context.Context, orgID, email, role, invitedBy, tokenHash string, now time.Time) (*Invitation, error) {
	return s.CreateInvitationTTL(ctx, orgID, email, role, invitedBy, tokenHash, now, InvitationTTL)
}

// CreateInvitationTTL is CreateInvitation with an own link lifetime (the host-admin command uses one hour).
func (s *Store) CreateInvitationTTL(ctx context.Context, orgID, email, role, invitedBy, tokenHash string, now time.Time, ttl time.Duration) (*Invitation, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var member bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u JOIN memberships m ON m.user_id = u.id
		WHERE u.email = $1 AND m.org_id = $2 AND u.deleted_at IS NULL)`, email, orgID).Scan(&member); err != nil {
		return nil, err
	}
	if member {
		return nil, ErrAlreadyMember
	}
	if _, err := tx.Exec(ctx, `DELETE FROM invitations WHERE org_id = $1 AND email = $2 AND accepted_at IS NULL`, orgID, email); err != nil {
		return nil, err
	}
	inv := Invitation{ID: NewID("inv"), Email: email, Role: role, ExpiresAt: now.Add(ttl), CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO invitations (id, org_id, email, role, token_hash, invited_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), $7, $8)`, inv.ID, orgID, email, role, tokenHash, invitedBy, inv.ExpiresAt, now); err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "member.invite", TargetType: "invitation", TargetID: inv.ID,
		Diff: map[string]any{"after": map[string]any{"email": email, "role": role, "expires_at": inv.ExpiresAt}}}); err != nil {
		return nil, err
	}
	return &inv, tx.Commit(ctx)
}

// AcceptInvitation makes the user (or brings back a removed user) and the membership, and marks the invitation used.
func (s *Store) AcceptInvitation(ctx context.Context, tokenHash, name, passwordHash string, now time.Time) (*User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var invID, orgID, email, role string
	err = tx.QueryRow(ctx, `SELECT id, org_id, email, role FROM invitations
		WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > $2 FOR UPDATE`, tokenHash, now).Scan(&invID, &orgID, &email, &role)
	if err != nil {
		return nil, notFound(err)
	}
	var userID string
	var deleted bool
	err = tx.QueryRow(ctx, `SELECT id, deleted_at IS NOT NULL FROM users WHERE email = $1`, email).Scan(&userID, &deleted)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		userID = NewID("usr")
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, $3, $4)`, userID, email, name, passwordHash); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case !deleted:
		return nil, ErrAlreadyMember
	default:
		if _, err := tx.Exec(ctx, `UPDATE users SET deleted_at = NULL, disabled = false, name = $2, password_hash = $3,
			totp_enabled = false, totp_secret_enc = NULL, recovery_codes_hash = '{}', updated_at = now() WHERE id = $1`, userID, name, passwordHash); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO memberships (id, org_id, user_id, role) VALUES ($1, $2, $3, $4)`, NewID("mem"), orgID, userID, role); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invitations SET accepted_at = $2 WHERE id = $1`, invID, now); err != nil {
		return nil, err
	}
	req := &audit.Request{ActorType: audit.ActorUser, ActorID: userID}
	if r := audit.RequestFrom(ctx); r != nil {
		req.IP, req.UserAgent, req.RequestID = r.IP, r.UserAgent, r.RequestID
	}
	if err := audit.Write(audit.WithRequest(ctx, req), tx, audit.Event{OrgID: orgID, Action: "member.join", TargetType: "user", TargetID: userID,
		Diff: map[string]any{"after": map[string]any{"email": email, "role": role}, "invitation_id": invID}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// CancelInvitation deletes an open invitation.
func (s *Store) CancelInvitation(ctx context.Context, orgID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var email string
	err = tx.QueryRow(ctx, `DELETE FROM invitations WHERE id = $1 AND org_id = $2 AND accepted_at IS NULL RETURNING email`, id, orgID).Scan(&email)
	if err != nil {
		return notFound(err)
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "member.invite_cancel", TargetType: "invitation", TargetID: id,
		Diff: map[string]any{"before": map[string]any{"email": email}}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockOwners serializes the changes of owners of one org, and returns the number of active owners
// other than the user.
func lockOwners(ctx context.Context, tx pgx.Tx, orgID, exceptUserID string) (int, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('owners:' || $1::text, 0))`, orgID); err != nil {
		return 0, err
	}
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.role = 'owner' AND m.user_id <> $2 AND NOT u.disabled AND u.deleted_at IS NULL`, orgID, exceptUserID).Scan(&n)
	return n, err
}

// UpdateMember changes the role or the disabled flag of a member. actorRole is the org role of the one who changes it:
// only an owner may change an owner or make one (REQ-RBAC-008). The last owner cannot be demoted or disabled (REQ-RBAC-007).
func (s *Store) UpdateMember(ctx context.Context, orgID, actorRole, userID string, role *string, disabled *bool) (*Member, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	others, err := lockOwners(ctx, tx, orgID, userID)
	if err != nil {
		return nil, err
	}
	var oldRole string
	var oldDisabled bool
	err = tx.QueryRow(ctx, `SELECT m.role, u.disabled FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.user_id = $2 AND u.deleted_at IS NULL FOR UPDATE OF m, u`, orgID, userID).Scan(&oldRole, &oldDisabled)
	if err != nil {
		return nil, notFound(err)
	}
	newRole, newDisabled := oldRole, oldDisabled
	if role != nil {
		newRole = *role
	}
	if disabled != nil {
		newDisabled = *disabled
	}
	if actorRole != "owner" && (oldRole == "owner" || newRole == "owner") {
		return nil, ErrOwnerProtect
	}
	if oldRole == "owner" && !oldDisabled && (newRole != "owner" || newDisabled) && others == 0 {
		return nil, ErrLastOwner
	}
	if newRole != oldRole {
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role = $3, source = 'manual' WHERE org_id = $1 AND user_id = $2`, orgID, userID, newRole); err != nil {
			return nil, err
		}
		if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "role.change", TargetType: "user", TargetID: userID,
			Diff: map[string]any{"before": map[string]any{"role": oldRole}, "after": map[string]any{"role": newRole}}}); err != nil {
			return nil, err
		}
	}
	if newDisabled != oldDisabled {
		if _, err := tx.Exec(ctx, `UPDATE users SET disabled = $2, updated_at = now() WHERE id = $1`, userID, newDisabled); err != nil {
			return nil, err
		}
		if newDisabled {
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
				return nil, err
			}
		}
		action := "member.enable"
		if newDisabled {
			action = "member.disable"
		}
		if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: action, TargetType: "user", TargetID: userID}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.memberByID(ctx, orgID, userID)
}

func (s *Store) memberByID(ctx context.Context, orgID, userID string) (*Member, error) {
	list, err := s.ListMembers(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].UserID == userID {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

// MemberByID returns one member.
func (s *Store) MemberByID(ctx context.Context, orgID, userID string) (*Member, error) {
	return s.memberByID(ctx, orgID, userID)
}

// RemoveMember removes a user from the org: sessions and tokens end at once, the team and project roles go.
// The user row stays (soft deleted), so the audit log keeps its actor.
func (s *Store) RemoveMember(ctx context.Context, orgID, actorRole, userID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	others, err := lockOwners(ctx, tx, orgID, userID)
	if err != nil {
		return err
	}
	var role, email string
	err = tx.QueryRow(ctx, `SELECT m.role, u.email FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.user_id = $2 AND u.deleted_at IS NULL FOR UPDATE OF m`, orgID, userID).Scan(&role, &email)
	if err != nil {
		return notFound(err)
	}
	if role == "owner" {
		if actorRole != "owner" {
			return ErrOwnerProtect
		}
		if others == 0 {
			return ErrLastOwner
		}
	}
	for _, q := range []string{
		`DELETE FROM sessions WHERE user_id = $1`,
		`UPDATE api_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		`DELETE FROM team_members WHERE user_id = $1`,
		`DELETE FROM project_roles WHERE user_id = $1`,
		`DELETE FROM memberships WHERE user_id = $1`,
		`UPDATE users SET deleted_at = now(), updated_at = now() WHERE id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, userID); err != nil {
			return err
		}
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "member.remove", TargetType: "user", TargetID: userID,
		Diff: map[string]any{"before": map[string]any{"email": email, "role": role}}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeUserSessions ends all sessions of a member. It returns ErrNotFound when the user is not a member.
func (s *Store) RevokeUserSessions(ctx context.Context, orgID, userID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID).Scan(&one); err != nil {
		return notFound(err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "session.revoke_all", TargetType: "user", TargetID: userID,
		Diff: map[string]any{"sessions": tag.RowsAffected()}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Team is a team with its members.
type Team struct {
	ID        string
	Name      string
	MemberIDs []string
}

// ListTeams lists the teams of the org with their member ids.
func (s *Store) ListTeams(ctx context.Context, orgID string) ([]Team, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.id, t.name, coalesce(array_agg(tm.user_id ORDER BY tm.user_id) FILTER (WHERE tm.user_id IS NOT NULL), '{}')
		FROM teams t LEFT JOIN team_members tm ON tm.team_id = t.id WHERE t.org_id = $1 GROUP BY t.id ORDER BY t.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Team{}
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.MemberIDs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) getTeam(ctx context.Context, orgID, id string) (*Team, error) {
	list, err := s.ListTeams(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

// CreateTeam makes a team.
func (s *Store) CreateTeam(ctx context.Context, orgID, name string) (*Team, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := NewID("team")
	if _, err := tx.Exec(ctx, `INSERT INTO teams (id, org_id, name) VALUES ($1, $2, $3)`, id, orgID, name); err != nil {
		if u, _ := IsUnique(err); u {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "team.create", TargetType: "team", TargetID: id,
		Diff: map[string]any{"after": map[string]any{"name": name}}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.getTeam(ctx, orgID, id)
}

// RenameTeam changes the name of a team.
func (s *Store) RenameTeam(ctx context.Context, orgID, id, name string) (*Team, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var old string
	if err := tx.QueryRow(ctx, `SELECT name FROM teams WHERE id = $1 AND org_id = $2 FOR UPDATE`, id, orgID).Scan(&old); err != nil {
		return nil, notFound(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE teams SET name = $2 WHERE id = $1`, id, name); err != nil {
		if u, _ := IsUnique(err); u {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "team.update", TargetType: "team", TargetID: id,
		Diff: map[string]any{"before": map[string]any{"name": old}, "after": map[string]any{"name": name}}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.getTeam(ctx, orgID, id)
}

// DeleteTeam deletes a team and the project roles of the team.
func (s *Store) DeleteTeam(ctx context.Context, orgID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM teams WHERE id = $1 AND org_id = $2 FOR UPDATE`, id, orgID).Scan(&name); err != nil {
		return notFound(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_roles WHERE team_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "team.delete", TargetType: "team", TargetID: id,
		Diff: map[string]any{"before": map[string]any{"name": name}}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetTeamMember adds (present) or removes a user of a team.
func (s *Store) SetTeamMember(ctx context.Context, orgID, teamID, userID string, present bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM teams WHERE id = $1 AND org_id = $2`, teamID, orgID).Scan(&one); err != nil {
		return notFound(err)
	}
	action := "team.member_remove"
	if present {
		if err := tx.QueryRow(ctx, `SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
			WHERE m.org_id = $1 AND m.user_id = $2 AND u.deleted_at IS NULL`, orgID, userID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrBadReference
			}
			return err
		}
		action = "team.member_add"
		_, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, teamID, userID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	}
	if err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: action, TargetType: "team", TargetID: teamID,
		Diff: map[string]any{"user_id": userID}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ProjectRole is a project role override of a user or a team.
type ProjectRole struct {
	UserID string
	TeamID string
	Role   string
}

// ListProjectRoles lists the overrides of a project.
func (s *Store) ListProjectRoles(ctx context.Context, projectID string) ([]ProjectRole, error) {
	rows, err := s.Pool.Query(ctx, `SELECT coalesce(user_id,''), coalesce(team_id,''), role FROM project_roles WHERE project_id = $1 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRole{}
	for rows.Next() {
		var r ProjectRole
		if err := rows.Scan(&r.UserID, &r.TeamID, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceProjectRoles replaces all overrides of a project. Every user and team must be in the org.
func (s *Store) ReplaceProjectRoles(ctx context.Context, orgID, projectID string, roles []ProjectRole) ([]ProjectRole, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM projects WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL FOR UPDATE`, projectID, orgID).Scan(&one); err != nil {
		return nil, notFound(err)
	}
	before, err := s.ListProjectRoles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_roles WHERE project_id = $1`, projectID); err != nil {
		return nil, err
	}
	for _, r := range roles {
		var ok bool
		if r.UserID != "" {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE org_id = $1 AND user_id = $2)`, orgID, r.UserID).Scan(&ok)
		} else {
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE org_id = $1 AND id = $2)`, orgID, r.TeamID).Scan(&ok)
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrBadReference
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_roles (id, project_id, user_id, team_id, role) VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5)`,
			NewID("prr"), projectID, r.UserID, r.TeamID, r.Role); err != nil {
			return nil, err
		}
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, ProjectID: projectID, Action: "project.roles_replace", TargetType: "project", TargetID: projectID,
		Diff: map[string]any{"before": roleDiff(before), "after": roleDiff(roles)}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ListProjectRoles(ctx, projectID)
}

func roleDiff(roles []ProjectRole) []map[string]string {
	out := make([]map[string]string, 0, len(roles))
	for _, r := range roles {
		m := map[string]string{"role": r.Role}
		if r.UserID != "" {
			m["user_id"] = r.UserID
		} else {
			m["team_id"] = r.TeamID
		}
		out = append(out, m)
	}
	return out
}

// CreateToken stores a token. Only the hash and the display prefix are kept.
func (s *Store) CreateToken(ctx context.Context, orgID, userID, name, hash, prefix string, scopes []string, projectID string, expires *time.Time) (*APIToken, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if projectID != "" {
		var one int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM projects WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, projectID, orgID).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrBadReference
			}
			return nil, err
		}
	}
	id := NewID("tok")
	if _, err := tx.Exec(ctx, `INSERT INTO api_tokens (id, org_id, user_id, name, token_hash, token_prefix, scopes, project_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), $9)`, id, orgID, userID, name, hash, prefix, scopes, projectID, expires); err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, ProjectID: projectID, Action: "token.create", TargetType: "token", TargetID: id,
		Diff: map[string]any{"after": map[string]any{"name": name, "scopes": scopes, "project_id": projectID, "expires_at": expires}}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return scanToken(s.Pool.QueryRow(ctx, `SELECT `+tokenCols+` FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = $1`, id))
}

// ListTokens lists the tokens of a user, or of the whole org (userID empty).
func (s *Store) ListTokens(ctx context.Context, orgID, userID string) ([]APIToken, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+tokenCols+` FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.org_id = $1 AND ($2 = '' OR t.user_id = $2) ORDER BY t.created_at DESC, t.id`, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// RevokeToken revokes a token of the org. ownerID limits it to the tokens of one user (empty: any user).
func (s *Store) RevokeToken(ctx context.Context, orgID, ownerID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name, uid string
	err = tx.QueryRow(ctx, `UPDATE api_tokens SET revoked_at = coalesce(revoked_at, now())
		WHERE id = $1 AND org_id = $2 AND ($3 = '' OR user_id = $3) RETURNING name, user_id`, id, orgID, ownerID).Scan(&name, &uid)
	if err != nil {
		return notFound(err)
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "token.revoke", TargetType: "token", TargetID: id,
		Diff: map[string]any{"before": map[string]any{"name": name, "user_id": uid}}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SessionInfo is a session of a user for the list.
type SessionInfo struct {
	ID        string
	IP        string
	UserAgent string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// ListSessions lists the live sessions of a user.
func (s *Store) ListSessions(ctx context.Context, userID string, now time.Time) ([]SessionInfo, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, coalesce(host(ip),''), coalesce(user_agent,''), created_at, expires_at FROM sessions
		WHERE user_id = $1 AND expires_at > $2 AND absolute_expires_at > $2 ORDER BY created_at DESC, id`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var i SessionInfo
		if err := rows.Scan(&i.ID, &i.IP, &i.UserAgent, &i.CreatedAt, &i.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteUserSession ends one session of the user.
func (s *Store) DeleteUserSession(ctx context.Context, orgID, userID, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "session.revoke", TargetType: "session", TargetID: id[:min(len(id), 12)]}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TOTP holds the two-factor data of a user.
type TOTP struct {
	SecretEnc []byte
	Enabled   bool
	LastStep  int64
	Recovery  []string
}

// GetTOTP reads the two-factor data of a user.
func (s *Store) GetTOTP(ctx context.Context, userID string) (*TOTP, error) {
	var t TOTP
	err := s.Pool.QueryRow(ctx, `SELECT totp_secret_enc, totp_enabled, totp_last_step, recovery_codes_hash FROM users WHERE id = $1`, userID).
		Scan(&t.SecretEnc, &t.Enabled, &t.LastStep, &t.Recovery)
	if err != nil {
		return nil, notFound(err)
	}
	return &t, nil
}

// SetTOTPPending stores the new secret. TOTP is not on until EnableTOTP.
func (s *Store) SetTOTPPending(ctx context.Context, userID string, secretEnc []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET totp_secret_enc = $2, updated_at = now() WHERE id = $1 AND NOT totp_enabled`, userID, secretEnc)
	return err
}

// EnableTOTP turns TOTP on with the recovery code hashes, and records the step of the confirming code.
func (s *Store) EnableTOTP(ctx context.Context, orgID, userID string, step int64, recoveryHashes []string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE users SET totp_enabled = true, totp_last_step = $2, recovery_codes_hash = $3, updated_at = now()
		WHERE id = $1 AND NOT totp_enabled AND totp_secret_enc IS NOT NULL`, userID, step, recoveryHashes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "auth.totp_enable", TargetType: "user", TargetID: userID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DisableTOTP turns TOTP off and removes the secret and the recovery codes.
func (s *Store) DisableTOTP(ctx context.Context, orgID, userID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_enabled = false, totp_secret_enc = NULL, totp_last_step = 0,
		recovery_codes_hash = '{}', updated_at = now() WHERE id = $1`, userID); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "auth.totp_disable", TargetType: "user", TargetID: userID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UseTOTPStep records the step of a good code. It is false when the step was used already (replay).
func (s *Store) UseTOTPStep(ctx context.Context, userID string, step int64) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET totp_last_step = $2 WHERE id = $1 AND totp_last_step < $2`, userID, step)
	return tag.RowsAffected() == 1, err
}

// UseRecoveryCode removes a recovery code (by hash). It is false when the user has no such code.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, hash string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE users SET recovery_codes_hash = array_remove(recovery_codes_hash, $2)
		WHERE id = $1 AND $2 = ANY(recovery_codes_hash)`, userID, hash)
	return tag.RowsAffected() == 1, err
}

// PasswordResetTTL is the life of a reset link.
const PasswordResetTTL = time.Hour

// CreatePasswordReset stores a reset token (by hash) for a user.
func (s *Store) CreatePasswordReset(ctx context.Context, userID, hash string, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash, userID, now.Add(PasswordResetTTL))
	return err
}

// ConfirmPasswordReset sets a new password with a reset token. All sessions of the user end.
func (s *Store) ConfirmPasswordReset(ctx context.Context, hash, passwordHash string, now time.Time) (*User, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	err = tx.QueryRow(ctx, `UPDATE password_resets SET used_at = $2 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2 RETURNING user_id`, hash, now).Scan(&userID)
	if err != nil {
		return nil, notFound(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, userID, passwordHash); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
		return nil, err
	}
	var orgID string
	if err := tx.QueryRow(ctx, `SELECT org_id FROM memberships WHERE user_id = $1 LIMIT 1`, userID).Scan(&orgID); err == nil {
		if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, ActorType: audit.ActorUser, ActorID: userID, Action: "auth.password_reset", TargetType: "user", TargetID: userID}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.UserByID(ctx, userID)
}

// UpdateOrg changes the name, sso_required or the settings (merged key by key) of the org.
func (s *Store) UpdateOrg(ctx context.Context, orgID string, name *string, ssoRequired *bool, settingsPatch []byte) (*Org, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var before Org
	err = tx.QueryRow(ctx, `SELECT id, name, slug, sso_required, settings FROM orgs WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, orgID).
		Scan(&before.ID, &before.Name, &before.Slug, &before.SSORequired, &before.Settings)
	if err != nil {
		return nil, notFound(err)
	}
	patch := "{}"
	if len(settingsPatch) > 0 {
		patch = string(settingsPatch)
	}
	if _, err := tx.Exec(ctx, `UPDATE orgs SET name = coalesce($2, name), sso_required = coalesce($3, sso_required),
		settings = settings || $4::jsonb, updated_at = now() WHERE id = $1`, orgID, name, ssoRequired, patch); err != nil {
		return nil, err
	}
	var after Org
	if err := tx.QueryRow(ctx, `SELECT id, name, slug, sso_required, settings FROM orgs WHERE id = $1`, orgID).
		Scan(&after.ID, &after.Name, &after.Slug, &after.SSORequired, &after.Settings); err != nil {
		return nil, err
	}
	if err := audit.Write(ctx, tx, audit.Event{OrgID: orgID, Action: "org.update", TargetType: "org", TargetID: orgID,
		Diff: map[string]any{"before": orgDiff(&before), "after": orgDiff(&after)}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &after, nil
}

func orgDiff(o *Org) map[string]any {
	return map[string]any{"name": o.Name, "sso_required": o.SSORequired, "settings": o.Settings}
}

// OnlyOrgID returns the id of the single org (the first one), or ErrNotFound when none exists.
func (s *Store) OnlyOrgID(ctx context.Context) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM orgs WHERE deleted_at IS NULL ORDER BY created_at, id LIMIT 1`).Scan(&id)
	return id, notFound(err)
}

// NormalizeEmail lowercases and trims an email.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
