// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/store"
)

// CodeLastOwner is the 409 code when a change would leave the org without an owner (REQ-RBAC-007).
const CodeLastOwner = "last_owner"

func validRole(r string) bool {
	return r == auth.RoleOwner || r == auth.RoleAdmin || r == auth.RoleDeveloper || r == auth.RoleViewer
}

func memberJSON(m store.Member) gen.Member {
	role, source, status := gen.Role(m.Role), gen.MemberSource(m.Source), gen.MemberStatus("active")
	if m.Disabled {
		status = "disabled"
	}
	return gen.Member{UserId: ptr(m.UserID), Email: ptr(m.Email), Name: ptr(m.Name), Role: &role,
		Source: &source, Status: &status, LastLoginAt: m.LastLoginAt}
}

func invitationJSON(i store.Invitation) gen.Member {
	role, source, status := gen.Role(i.Role), gen.MemberSource("manual"), gen.MemberStatus("invited")
	return gen.Member{UserId: ptr(i.ID), Email: ptr(i.Email), Name: ptr(""), Role: &role, Source: &source, Status: &status}
}

// ListMembers lists the members of the org and its open invitations.
func (h *Handlers) ListMembers(w http.ResponseWriter, r *http.Request, _ gen.ListMembersParams) {
	orgID := PrincipalFrom(r.Context()).OrgID
	list, err := h.Store.ListMembers(r.Context(), orgID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	invites, err := h.Store.ListInvitations(r.Context(), orgID, h.Clock.Now())
	if err != nil {
		internalError(w, r, err)
		return
	}
	items := make([]gen.Member, 0, len(list)+len(invites))
	for _, m := range list {
		items = append(items, memberJSON(m))
	}
	for _, i := range invites {
		items = append(items, invitationJSON(i))
	}
	WriteJSON(w, http.StatusOK, page[gen.Member]{Items: items})
}

func forbidOwnerChange(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusForbidden, CodeForbidden, "only an owner can change an owner or make one", map[string]any{"reason": "owner_only"})
}

// memberFailed maps the errors of the member changes. It returns true when it wrote an answer.
func memberFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		notFoundError(w, r)
	case errors.Is(err, store.ErrLastOwner):
		WriteError(w, r, http.StatusConflict, CodeLastOwner, "the organization must keep one active owner", nil)
	case errors.Is(err, store.ErrOwnerProtect):
		forbidOwnerChange(w, r)
	case errors.Is(err, store.ErrAlreadyMember):
		WriteError(w, r, http.StatusConflict, CodeConflict, "this email is already a member", map[string]any{"field": "email"})
	case errors.Is(err, store.ErrBadReference):
		invalid(w, r, "id", "the user, team or project is not in this organization")
	case errors.Is(err, store.ErrNameTaken):
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "this name is taken", map[string]any{"field": "name"})
	default:
		internalError(w, r, err)
	}
	return true
}

// InviteMember makes an invitation. There is no mail server in this version: the answer has the link
// (invite_url), shown once, and the admin sends it (REQ-RBAC-006).
func (h *Handlers) InviteMember(w http.ResponseWriter, r *http.Request) {
	var body gen.InviteMemberJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p := PrincipalFrom(r.Context())
	email := store.NormalizeEmail(string(body.Email))
	if !emailRe.MatchString(email) {
		invalid(w, r, "email", "email is not valid")
		return
	}
	if !validRole(string(body.Role)) {
		invalid(w, r, "role", "role must be owner, admin, developer or viewer")
		return
	}
	if body.Role == auth.RoleOwner && p.OrgRole != auth.RoleOwner {
		forbidOwnerChange(w, r)
		return
	}
	token, err := auth.NewOneTimeToken()
	if err != nil {
		internalError(w, r, err)
		return
	}
	inv, err := h.Store.CreateInvitation(r.Context(), p.OrgID, email, string(body.Role), p.User.ID, auth.HashToken(token), h.Clock.Now())
	if memberFailed(w, r, err) {
		return
	}
	m := invitationJSON(*inv)
	m.InviteUrl = ptr(strings.TrimRight(h.Settings.ExternalURL, "/") + "/invite?token=" + token)
	WriteJSON(w, http.StatusCreated, m)
}

// AcceptInvitation makes the user of an invitation and logs them in.
func (h *Handlers) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var body gen.AcceptInvitationJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := validName(body.Name)
	if !ok {
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	if err := auth.CheckPasswordPolicy(body.Password); err != nil {
		invalid(w, r, "password", err.Error())
		return
	}
	key := "invite:" + clientKey(r)
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		internalError(w, r, err)
		return
	}
	user, err := h.Store.AcceptInvitation(r.Context(), auth.HashToken(body.Token), name, hash, h.Clock.Now())
	if errors.Is(err, store.ErrNotFound) {
		h.Throttle.Fail(key)
		WriteError(w, r, http.StatusNotFound, CodeNotFound, "the invitation is not valid or has expired", nil)
		return
	}
	if memberFailed(w, r, err) {
		return
	}
	if err := h.startSession(w, r, user.ID); err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, userJSON(user))
}

// UpdateMember changes the role or disables a member.
func (h *Handlers) UpdateMember(w http.ResponseWriter, r *http.Request, userID gen.UserId) {
	var body gen.UpdateMemberJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p := PrincipalFrom(r.Context())
	var role *string
	if body.Role != nil {
		if !validRole(string(*body.Role)) {
			invalid(w, r, "role", "role must be owner, admin, developer or viewer")
			return
		}
		role = ptr(string(*body.Role))
	}
	m, err := h.Store.UpdateMember(r.Context(), p.OrgID, p.OrgRole, userID, role, body.Disabled)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, memberJSON(*m))
}

// RemoveMember removes a member, or cancels an invitation (the id starts with inv_). A user can leave: remove themselves.
func (h *Handlers) RemoveMember(w http.ResponseWriter, r *http.Request, userID gen.UserId) {
	p := PrincipalFrom(r.Context())
	var err error
	if strings.HasPrefix(userID, "inv_") {
		err = h.Store.CancelInvitation(r.Context(), p.OrgID, userID)
	} else {
		err = h.Store.RemoveMember(r.Context(), p.OrgID, p.OrgRole, userID)
	}
	if memberFailed(w, r, err) {
		return
	}
	if userID == p.User.ID {
		h.clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeMemberSessions ends all sessions of a member.
func (h *Handlers) RevokeMemberSessions(w http.ResponseWriter, r *http.Request, userID gen.UserId) {
	if memberFailed(w, r, h.Store.RevokeUserSessions(r.Context(), PrincipalFrom(r.Context()).OrgID, userID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func teamJSON(t store.Team) gen.Team {
	ids := t.MemberIDs
	if ids == nil {
		ids = []string{}
	}
	return gen.Team{Id: ptr(t.ID), Name: ptr(t.Name), MemberIds: &ids}
}

// ListTeams lists the teams.
func (h *Handlers) ListTeams(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListTeams(r.Context(), PrincipalFrom(r.Context()).OrgID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]gen.Team, 0, len(list))
	for _, t := range list {
		out = append(out, teamJSON(t))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateTeam makes a team.
func (h *Handlers) CreateTeam(w http.ResponseWriter, r *http.Request) {
	var body gen.CreateTeamJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := validName(body.Name)
	if !ok {
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	t, err := h.Store.CreateTeam(r.Context(), PrincipalFrom(r.Context()).OrgID, name)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, teamJSON(*t))
}

// UpdateTeam renames a team.
func (h *Handlers) UpdateTeam(w http.ResponseWriter, r *http.Request, teamID gen.TeamId) {
	var body gen.UpdateTeamJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	orgID := PrincipalFrom(r.Context()).OrgID
	if body.Name == nil {
		list, err := h.Store.ListTeams(r.Context(), orgID)
		if err != nil {
			internalError(w, r, err)
			return
		}
		for _, t := range list {
			if t.ID == teamID {
				WriteJSON(w, http.StatusOK, teamJSON(t))
				return
			}
		}
		notFoundError(w, r)
		return
	}
	name, ok := validName(*body.Name)
	if !ok {
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	t, err := h.Store.RenameTeam(r.Context(), orgID, teamID, name)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, teamJSON(*t))
}

// DeleteTeam deletes a team and its project roles.
func (h *Handlers) DeleteTeam(w http.ResponseWriter, r *http.Request, teamID gen.TeamId) {
	if memberFailed(w, r, h.Store.DeleteTeam(r.Context(), PrincipalFrom(r.Context()).OrgID, teamID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddTeamMember adds a user to a team.
func (h *Handlers) AddTeamMember(w http.ResponseWriter, r *http.Request, teamID gen.TeamId, userID gen.UserId) {
	if memberFailed(w, r, h.Store.SetTeamMember(r.Context(), PrincipalFrom(r.Context()).OrgID, teamID, userID, true)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveTeamMember removes a user from a team.
func (h *Handlers) RemoveTeamMember(w http.ResponseWriter, r *http.Request, teamID gen.TeamId, userID gen.UserId) {
	if memberFailed(w, r, h.Store.SetTeamMember(r.Context(), PrincipalFrom(r.Context()).OrgID, teamID, userID, false)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func projectRolesJSON(list []store.ProjectRole) []gen.ProjectRole {
	out := make([]gen.ProjectRole, 0, len(list))
	for _, r := range list {
		pr := gen.ProjectRole{Role: gen.ProjectRoleRole(r.Role)}
		if r.UserID != "" {
			pr.UserId = ptr(r.UserID)
		}
		if r.TeamID != "" {
			pr.TeamId = ptr(r.TeamID)
		}
		out = append(out, pr)
	}
	return out
}

// ListProjectRoles lists the role overrides of a project.
func (h *Handlers) ListProjectRoles(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	if _, err := h.Store.GetProject(r.Context(), PrincipalFrom(r.Context()).OrgID, projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFoundError(w, r)
			return
		}
		internalError(w, r, err)
		return
	}
	list, err := h.Store.ListProjectRoles(r.Context(), projectID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, projectRolesJSON(list))
}

// ReplaceProjectRoles replaces the role overrides of a project.
func (h *Handlers) ReplaceProjectRoles(w http.ResponseWriter, r *http.Request, projectID gen.ProjectId) {
	var body gen.ReplaceProjectRolesJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	roles := make([]store.ProjectRole, 0, len(body))
	seen := map[string]bool{}
	for _, b := range body {
		u, t := derefStr(b.UserId), derefStr(b.TeamId)
		if (u == "") == (t == "") {
			invalid(w, r, "user_id", "give exactly one of user_id and team_id")
			return
		}
		switch b.Role {
		case "admin", "developer", "viewer":
		default:
			invalid(w, r, "role", "role must be admin, developer or viewer")
			return
		}
		if key := u + "|" + t; seen[key] {
			invalid(w, r, "user_id", "a user or a team can have only one role in a project")
			return
		} else {
			seen[key] = true
		}
		roles = append(roles, store.ProjectRole{UserID: u, TeamID: t, Role: string(b.Role)})
	}
	list, err := h.Store.ReplaceProjectRoles(r.Context(), PrincipalFrom(r.Context()).OrgID, projectID, roles)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, projectRolesJSON(list))
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func tokenJSON(t store.APIToken) gen.ApiToken {
	scopes := t.Scopes
	return gen.ApiToken{Id: ptr(t.ID), Name: ptr(t.Name), TokenPrefix: ptr(t.Prefix), Scopes: &scopes, ProjectId: t.ProjectID,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

// ListTokens lists the own tokens, or all tokens with all=true and token:manage.
func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request, params gen.ListTokensParams) {
	p := PrincipalFrom(r.Context())
	owner := p.User.ID
	if params.All != nil && *params.All {
		if !auth.Can(p.OrgRole, "token:manage", false) || (p.Token != nil && !auth.ScopeAllows(p.Token.Scopes, "token:manage")) {
			WriteError(w, r, http.StatusForbidden, CodeForbidden, "you do not have the permission for this action", map[string]any{"permission": "token:manage"})
			return
		}
		owner = ""
	}
	list, err := h.Store.ListTokens(r.Context(), p.OrgID, owner)
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]gen.ApiToken, 0, len(list))
	for _, t := range list {
		out = append(out, tokenJSON(t))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateToken makes an API token. The full value is in the answer once.
func (h *Handlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	var body gen.CreateTokenJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p := PrincipalFrom(r.Context())
	name, ok := validName(body.Name)
	if !ok {
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	if len(body.Scopes) == 0 {
		invalid(w, r, "scopes", "give at least one scope")
		return
	}
	scopes := make([]string, 0, len(body.Scopes))
	for _, s := range body.Scopes {
		if !auth.ValidScope(string(s)) {
			invalid(w, r, "scopes", "a scope must be read, deploy, write or admin")
			return
		}
		scopes = append(scopes, string(s))
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(h.Clock.Now()) {
		invalid(w, r, "expires_at", "the expiry must be in the future")
		return
	}
	value, err := auth.NewAPIToken()
	if err != nil {
		internalError(w, r, err)
		return
	}
	t, err := h.Store.CreateToken(r.Context(), p.OrgID, p.User.ID, name, auth.HashToken(value), value[:8], scopes, derefStr(body.ProjectId), body.ExpiresAt)
	if memberFailed(w, r, err) {
		return
	}
	out := tokenJSON(*t)
	WriteJSON(w, http.StatusCreated, gen.ApiTokenCreated{Id: out.Id, Name: out.Name, TokenPrefix: out.TokenPrefix, Scopes: out.Scopes,
		ProjectId: out.ProjectId, ExpiresAt: out.ExpiresAt, LastUsedAt: out.LastUsedAt, RevokedAt: out.RevokedAt, Token: &value})
}

// RevokeToken revokes one of the own tokens.
func (h *Handlers) RevokeToken(w http.ResponseWriter, r *http.Request, tokenID gen.TokenId) {
	p := PrincipalFrom(r.Context())
	if memberFailed(w, r, h.Store.RevokeToken(r.Context(), p.OrgID, p.User.ID, tokenID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOrgToken revokes the token of any user.
func (h *Handlers) RevokeOrgToken(w http.ResponseWriter, r *http.Request, tokenID gen.TokenId) {
	if memberFailed(w, r, h.Store.RevokeToken(r.Context(), PrincipalFrom(r.Context()).OrgID, "", tokenID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListMySessions lists the sessions of the user.
func (h *Handlers) ListMySessions(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	list, err := h.Store.ListSessions(r.Context(), p.User.ID, h.Clock.Now())
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]gen.Session, 0, len(list))
	for _, s := range list {
		out = append(out, gen.Session{Id: ptr(s.ID), Ip: ptr(s.IP), UserAgent: ptr(s.UserAgent), CreatedAt: ptr(s.CreatedAt),
			ExpiresAt: ptr(s.ExpiresAt), Current: ptr(s.ID == p.SessionID)})
	}
	WriteJSON(w, http.StatusOK, out)
}

// RevokeMySession ends one own session.
func (h *Handlers) RevokeMySession(w http.ResponseWriter, r *http.Request, sessionID string) {
	p := PrincipalFrom(r.Context())
	if memberFailed(w, r, h.Store.DeleteUserSession(r.Context(), p.OrgID, p.User.ID, sessionID)) {
		return
	}
	if sessionID == p.SessionID {
		h.clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateOrg changes the name, sso_required and the settings of the org (REQ-AUTH-002: the 2FA policy is a setting).
func (h *Handlers) UpdateOrg(w http.ResponseWriter, r *http.Request) {
	var body gen.OrgUpdate
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name != nil {
		name, ok := validName(*body.Name)
		if !ok {
			invalid(w, r, "name", "name must have 1 to 100 characters")
			return
		}
		body.Name = &name
	}
	var patch []byte
	if body.Settings != nil {
		s := body.Settings
		for field, v := range map[string]*int{"metrics_retention_days": s.MetricsRetentionDays, "logs_retention_days": s.LogsRetentionDays, "audit_retention_days": s.AuditRetentionDays} {
			if v != nil && (*v < 1 || *v > 3650) {
				invalid(w, r, "settings."+field, "must be between 1 and 3650 days")
				return
			}
		}
		if !h.checkRetention(w, r, s) {
			return
		}
		confirm := s.ConfirmRetentionShrink
		s.ConfirmRetentionShrink = nil // a request flag, not a setting
		raw, err := json.Marshal(s)
		s.ConfirmRetentionShrink = confirm
		if err != nil {
			internalError(w, r, err)
			return
		}
		patch = raw
	}
	p := PrincipalFrom(r.Context())
	o, err := h.Store.UpdateOrg(r.Context(), p.OrgID, body.Name, body.SsoRequired, patch)
	if memberFailed(w, r, err) {
		return
	}
	var settings gen.OrgSettings
	_ = json.Unmarshal(o.Settings, &settings)
	WriteJSON(w, http.StatusOK, gen.Org{Id: &o.ID, Name: &o.Name, Slug: &o.Slug, SsoRequired: &o.SSORequired, Settings: &settings})
}

// checkRetention checks the metric and log retention of an org change (REQ-OBS-012). A value above the limit of the
// store is refused, because the store would delete the data first. A shorter value deletes older data, so it needs
// confirm_retention_shrink. It writes the answer and returns false when the change is not allowed.
func (h *Handlers) checkRetention(w http.ResponseWriter, r *http.Request, s *gen.OrgSettings) bool {
	curMetrics, curLogs := h.retentionOf(r)
	for _, c := range []struct {
		field   string
		want    *int
		limit   int
		current time.Duration
	}{
		{"metrics_retention_days", s.MetricsRetentionDays, h.Monitoring.MetricsRetentionDays, curMetrics},
		{"logs_retention_days", s.LogsRetentionDays, h.Monitoring.LogsRetentionDays, curLogs},
	} {
		if c.want == nil {
			continue
		}
		if c.limit > 0 && *c.want > c.limit {
			invalid(w, r, "settings."+c.field, fmt.Sprintf("the store keeps at most %d days (storage retention of the server)", c.limit))
			return false
		}
		if time.Duration(*c.want)*day < c.current && (s.ConfirmRetentionShrink == nil || !*s.ConfirmRetentionShrink) {
			WriteError(w, r, http.StatusConflict, "retention_shrink_needs_confirm",
				"a shorter retention deletes older data. Send confirm_retention_shrink to go on", map[string]any{"field": c.field})
			return false
		}
	}
	return true
}
