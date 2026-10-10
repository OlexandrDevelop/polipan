// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/polipan/polipan/api/gen"
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
	SessionID string
	CSRF      string // plain CSRF token of the session
	OrgID     string
	Role      string
}

type principalKey struct{}

// PrincipalFrom returns the logged-in user of the request, or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// guard is the middleware of every operation. It checks, in this order:
// the rate limit of the client IP, the session cookie, the CSRF header on state-changing
// requests, the permission of the operation (x-permission and the role matrix) and the
// rate limit of the user (a key that is used only after the session is valid).
func (h *Handlers) guard() gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return notImplementedAsJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !h.Limiter.Check(w, r, clientKey(r)) {
				return
			}
			perm, ok := h.perms[Route{Method: r.Method, Pattern: normalize(chi.RouteContext(r.Context()).RoutePattern())}]
			if !ok {
				logging.From(r.Context()).Error("route has no permission", "method", r.Method, "path", r.URL.Path)
				WriteError(w, r, http.StatusInternalServerError, CodeInternal, "internal error", nil)
				return
			}
			if perm == PermPublic {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := h.authenticate(w, r)
			if !ok {
				return
			}
			if perm != PermAuthenticated && !auth.Can(p.Role, perm, false) {
				WriteError(w, r, http.StatusForbidden, CodeForbidden, "you do not have the permission for this action",
					map[string]any{"permission": perm})
				return
			}
			if !h.Limiter.Check(w, r, userKey(p.User.ID)) {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		}))
	}
}

// authenticate finds the session of the cookie and checks CSRF. It writes the error answer when it fails.
func (h *Handlers) authenticate(w http.ResponseWriter, r *http.Request) (*Principal, bool) {
	unauthenticated := func() (*Principal, bool) {
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "login is required", nil)
		return nil, false
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		// API tokens (Authorization: Bearer) come with M5.
		return unauthenticated()
	}
	if h.Store == nil {
		internalError(w, r, errors.New("no database"))
		return nil, false
	}
	now := h.Clock.Now()
	sess, err := h.Store.GetSession(r.Context(), auth.SessionID(c.Value), now)
	if errors.Is(err, store.ErrNotFound) {
		return unauthenticated()
	}
	if err != nil {
		internalError(w, r, err)
		return nil, false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
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
	return &Principal{User: &sess.User, SessionID: sess.ID, CSRF: auth.CSRFToken(c.Value), OrgID: sess.User.OrgID, Role: sess.User.Role}, true
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
