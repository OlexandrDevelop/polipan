// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/store"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "pp_session"

// CSRFHeader is the header that state-changing requests with a cookie must send.
const CSRFHeader = "X-CSRF-Token"

// Principal is the logged-in user of a request.
type Principal struct {
	User      *store.User
	SessionID string // empty for an API token
	CSRF      string // plain CSRF token of the session
	OrgID     string
	// Role is the effective role for this request: the org role, or the role in the project of the object.
	Role string
	// OrgRole is the org role of the user.
	OrgRole string
	// Token is set when the request is made with an API token.
	Token *store.APIToken
}

type principalKey struct{}

// PrincipalFrom returns the logged-in user of the request, or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// sessionOnly are the operations an API token cannot call: a token must not make tokens with more
// scopes or change the login of its user.
var sessionOnly = map[string]bool{
	"createToken": true, "updateMe": true, "enrollTotp": true, "verifyTotp": true, "disableTotp": true,
}

// twoFactorSetup are the operations a user may call while the org requires 2FA and the user has none.
var twoFactorSetup = map[string]bool{"enrollTotp": true, "verifyTotp": true, "getMe": true, "logout": true}

// pathScopeParams are the path parameters that point to an object with a project, in the order we try them.
var pathScopeParams = []string{"service_id", "deployment_id", "release_id", "domain_id", "volume_id", "variable_id",
	"secret_id", "instance_id", "policy_id", "environment_id", "project_id"}

// guard is the middleware of every operation. It checks, in this order:
// the rate limit of the client IP, the login (session cookie or API token), the CSRF header on state-changing
// requests with a cookie, the permission of the operation (x-permission, the role matrix, the effective role in the
// project of the object, the protected environment and the token scopes) and the rate limit of the user.
// It also puts the audit request info in the context and writes the audit event of a state-changing operation that
// wrote none itself (REQ-AUDIT-001).
func (h *Handlers) guard() gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return notImplementedAsJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !h.Limiter.Check(w, r, clientKey(r)) {
				return
			}
			route := Route{Method: r.Method, Pattern: normalize(chi.RouteContext(r.Context()).RoutePattern())}
			perm, ok := h.perms[route]
			if !ok {
				logging.From(r.Context()).Error("route has no permission", "method", r.Method, "path", r.URL.Path)
				WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal error", nil)
				return
			}
			opID := h.opIDs[route]
			areq := &audit.Request{ActorType: audit.ActorSystem, ActorID: "anonymous", IP: clientKey(r)[len("ip:"):],
				UserAgent: r.UserAgent(), RequestID: RequestIDFrom(r.Context())}
			ctx := audit.WithRequest(r.Context(), areq)
			var p *Principal
			if perm != PermPublic {
				var ok bool
				if p, ok = h.authenticate(w, r); !ok {
					return
				}
				areq.ActorType, areq.ActorID, areq.UserID = audit.ActorUser, p.User.ID, p.User.ID
				if p.Token != nil {
					areq.ActorType, areq.ActorID = audit.ActorToken, p.Token.ID
				}
				if !h.authorize(w, r, p, perm, opID) {
					return
				}
				if !h.Limiter.Check(w, r, userKey(p.User.ID)) {
					return
				}
				ctx = context.WithValue(ctx, principalKey{}, p)
			}
			if isReadMethod(r.Method) {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))
			h.auditFallback(r, areq, p, opID, sw.status)
		}))
	}
}

// authorize checks the permission of the operation for the principal. It writes the 403 and returns false when it fails.
func (h *Handlers) authorize(w http.ResponseWriter, r *http.Request, p *Principal, perm, opID string) bool {
	deny := func(msg string, details map[string]any) bool {
		WriteError(w, r, http.StatusForbidden, CodeForbidden, msg, details)
		return false
	}
	if p.User.Require2FA && !p.User.TotpEnabled && !twoFactorSetup[opID] {
		return deny("this organization requires two-factor login: set it up first", map[string]any{"reason": "two_factor_required"})
	}
	if p.Token != nil && sessionOnly[opID] {
		return deny("this action needs a login session, not an API token", map[string]any{"reason": "session_required"})
	}
	if perm == PermAuthenticated {
		return true
	}
	sc, found, err := h.pathScope(r, p.OrgID)
	if err != nil {
		internalError(w, r, err)
		return false
	}
	p.Role = p.OrgRole
	if found {
		overrides, err := h.Store.ProjectRoleNames(r.Context(), sc.ProjectID, p.User.ID)
		if err != nil {
			internalError(w, r, err)
			return false
		}
		p.Role = auth.EffectiveRole(p.OrgRole, overrides)
	}
	if !auth.Can(p.Role, perm, found && sc.Protected) {
		details := map[string]any{"permission": perm}
		msg := "you do not have the permission for this action"
		if found && sc.Protected && auth.Can(p.Role, perm, false) {
			msg, details["reason"] = "this environment is protected, an admin is needed", "protected_environment"
		}
		return deny(msg, details)
	}
	if t := p.Token; t != nil {
		if !auth.ScopeAllows(t.Scopes, perm) {
			return deny("the scope of the API token does not allow this action", map[string]any{"permission": perm, "reason": "token_scope"})
		}
		if t.ProjectID != nil && (!found || sc.ProjectID != *t.ProjectID) {
			return deny("the API token is limited to another project", map[string]any{"permission": perm, "reason": "token_project"})
		}
	}
	return true
}

// pathScope finds the project and the protected flag of the object in the path.
func (h *Handlers) pathScope(r *http.Request, orgID string) (store.AuthScope, bool, error) {
	if h.Store == nil {
		return store.AuthScope{}, false, nil
	}
	for _, name := range pathScopeParams {
		if id := chi.URLParam(r, name); id != "" {
			return h.Store.ScopeOf(r.Context(), orgID, name, id)
		}
	}
	return store.AuthScope{}, false, nil
}

// auditFallback writes the generic audit event of a state-changing operation that wrote no event of its own.
func (h *Handlers) auditFallback(r *http.Request, areq *audit.Request, p *Principal, opID string, status int) {
	if areq.Wrote() || h.Store == nil {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	var orgID string
	if p != nil {
		orgID = p.OrgID
	} else if id, err := h.Store.OnlyOrgID(ctx); err == nil {
		orgID = id
	} else {
		return // no org yet: nothing to audit
	}
	e := audit.Event{OrgID: orgID, Action: "api." + opID, TargetType: "api", TargetID: opID,
		Diff: map[string]any{"method": r.Method, "path": chi.RouteContext(r.Context()).RoutePattern(), "status": status}}
	if err := audit.Write(audit.WithRequest(ctx, areq), h.Store.Pool, e); err != nil {
		logging.From(r.Context()).Error("audit event not written", "error", err, "operation", opID)
	}
}

// authenticate finds the login of the request: an API token (Authorization: Bearer pp_...) or the session
// cookie, and checks CSRF for a cookie. It writes the error answer when it fails.
func (h *Handlers) authenticate(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	unauthenticated := func() (*Principal, bool) {
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "login is required", nil)
		return nil, false
	}
	if h.Store == nil {
		internalError(w, r, errors.New("no database"))
		return nil, false
	}
	now := h.Clock.Now()
	if hdr := r.Header.Get("Authorization"); hdr != "" {
		token, ok := strings.CutPrefix(hdr, "Bearer ")
		if !ok || !strings.HasPrefix(token, auth.APITokenPrefix) {
			return unauthenticated()
		}
		ta, err := h.Store.GetTokenByHash(r.Context(), auth.HashToken(strings.TrimSpace(token)), now)
		if errors.Is(err, store.ErrNotFound) {
			return unauthenticated()
		}
		if err != nil {
			internalError(w, r, err)
			return nil, false
		}
		if err := h.Store.TouchToken(r.Context(), ta.Token.ID, now); err != nil {
			logging.From(r.Context()).Warn("token last use not stored", "error", err)
		}
		return &Principal{User: &ta.User, OrgID: ta.User.OrgID, Role: ta.User.Role, OrgRole: ta.User.Role, Token: &ta.Token}, true
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return unauthenticated()
	}
	sess, err := h.Store.GetSession(r.Context(), auth.SessionID(c.Value), now)
	if errors.Is(err, store.ErrNotFound) {
		return unauthenticated()
	}
	if err != nil {
		internalError(w, r, err)
		return nil, false
	}
	if !isReadMethod(r.Method) {
		got := auth.HashToken(r.Header.Get(CSRFHeader))
		if subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFHash)) != 1 {
			WriteError(w, r, http.StatusForbidden, CodeForbidden, "the CSRF token is missing or wrong",
				map[string]any{"reason": "csrf"})
			return nil, false
		}
	}
	// Sliding expiry. The cookie itself lasts until the absolute end, so it is not set again.
	if next := minTime(now.Add(h.Settings.SessionTTL), sess.AbsoluteExpiresAt); next.Sub(sess.ExpiresAt) > time.Minute {
		if err := h.Store.ExtendSession(r.Context(), sess.ID, next); err != nil {
			logging.From(r.Context()).Warn("extend session failed", "error", err)
		}
	}
	return &Principal{User: &sess.User, SessionID: sess.ID, CSRF: auth.CSRFToken(c.Value), OrgID: sess.User.OrgID,
		Role: sess.User.Role, OrgRole: sess.User.Role}, true
}

func isReadMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// denyProtected answers 403 and returns true when a developer does something with a
// permission marked P in a protected environment (10-security, section 3).
func denyProtected(w http.ResponseWriter, r *http.Request, perm string, protected bool) bool {
	p := PrincipalFrom(r.Context())
	if p == nil || auth.Can(p.Role, perm, protected) {
		return false
	}
	WriteError(w, r, http.StatusForbidden, CodeForbidden, "this environment is protected, an admin is needed",
		map[string]any{"permission": perm, "reason": "protected_environment"})
	return true
}

// requireAdmin answers 403 and returns true when the user is not owner or admin.
func requireAdmin(w http.ResponseWriter, r *http.Request, what string) bool {
	p := PrincipalFrom(r.Context())
	if p != nil && auth.IsAdmin(p.Role) {
		return false
	}
	WriteError(w, r, http.StatusForbidden, CodeForbidden, what+" needs an admin", nil)
	return true
}
