// SPDX-License-Identifier: Apache-2.0

package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/store"
)

func userJSON(u *store.User) gen.User {
	return gen.User{Id: &u.ID, Email: &u.Email, Name: &u.Name, TotpEnabled: &u.TotpEnabled, CreatedAt: &u.CreatedAt}
}

// dummyHash is checked when the user does not exist, so the answer takes the same time.
var (
	dummyOnce sync.Once
	dummyHash string
)

func burnPasswordTime(password string) {
	dummyOnce.Do(func() { dummyHash, _ = auth.HashPassword("not-a-real-password") })
	_, _ = auth.VerifyPassword(password, dummyHash)
}

// startSession makes a session, sets the cookie and the CSRF header.
func (h *Handlers) startSession(w http.ResponseWriter, r *http.Request, userID string) error {
	value, err := auth.NewSessionValue()
	if err != nil {
		return err
	}
	now := h.Clock.Now()
	expires := now.Add(h.Settings.SessionTTL)
	absolute := now.Add(h.Settings.SessionAbsoluteTTL)
	if expires.After(absolute) {
		expires = absolute
	}
	csrf := auth.CSRFToken(value)
	ip := clientKey(r)[len("ip:"):]
	if err := h.Store.CreateSession(r.Context(), auth.SessionID(value), userID, auth.HashToken(csrf), ip, r.UserAgent(), expires, absolute); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/",
		Expires: absolute, MaxAge: int(absolute.Sub(now).Seconds()),
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set(CSRFHeader, csrf)
	return nil
}

func (h *Handlers) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handlers) tooManyAttempts(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	w.Header().Set("Retry-After", retryAfter(wait))
	WriteError(w, r, http.StatusTooManyRequests, CodeRateLimited, "too many failed attempts, try again later",
		map[string]any{"retry_after_seconds": int((wait + time.Second - 1) / time.Second)})
}

// SetupFirstUser creates the org and the owner while there is no user (AC-AUTH-03). Afterwards: 404.
func (h *Handlers) SetupFirstUser(w http.ResponseWriter, r *http.Request) {
	var body gen.SetupFirstUserJSONBody
	closed, err := h.Store.HasUsers(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	if closed {
		notFoundError(w, r)
		return
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	key := "setup:" + clientKey(r)
	if locked, wait := h.Throttle.Locked(key); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	if h.Settings.SetupToken != "" && subtle.ConstantTimeCompare([]byte(body.SetupToken), []byte(h.Settings.SetupToken)) != 1 {
		h.Throttle.Fail(key)
		WriteError(w, r, http.StatusForbidden, CodeForbidden, "the setup token is wrong", nil)
		return
	}
	email := strings.ToLower(strings.TrimSpace(string(body.Email)))
	name, ok := validName(body.Name)
	switch {
	case !emailRe.MatchString(email):
		invalid(w, r, "email", "email is not valid")
		return
	case !ok:
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	if err := auth.CheckPasswordPolicy(body.Password); err != nil {
		invalid(w, r, "password", err.Error())
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		internalError(w, r, err)
		return
	}
	user, err := h.Store.CreateFirstUser(r.Context(), "Polipan", "default", email, name, hash)
	if errors.Is(err, store.ErrSetupClosed) {
		notFoundError(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	if err := h.startSession(w, r, user.ID); err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, userJSON(user))
}

// Login checks email and password and starts a session. 5 failed tries in 15 minutes for one account
// or one IP lock it for 15 minutes (429 with Retry-After).
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var body gen.LoginJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(string(body.Email)))
	ipKey, acctKey := clientKey(r), "acct:"+email
	if locked, wait := h.Throttle.Locked(ipKey, acctKey); locked {
		h.tooManyAttempts(w, r, wait)
		return
	}
	user, err := h.Store.UserByEmail(r.Context(), email)
	good := false
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && (user.PasswordHash == nil || user.Disabled)):
		burnPasswordTime(body.Password)
	case err != nil:
		internalError(w, r, err)
		return
	default:
		good, err = auth.VerifyPassword(body.Password, *user.PasswordHash)
		if err != nil {
			internalError(w, r, err)
			return
		}
	}
	// Two-factor login comes with M5. A user with TOTP on cannot log in with a password alone.
	if good && user.TotpEnabled {
		good = false
	}
	if !good {
		h.Throttle.Fail(ipKey, acctKey)
		WriteError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "email or password is wrong", nil)
		return
	}
	h.Throttle.Reset(acctKey)
	if err := h.startSession(w, r, user.ID); err != nil {
		internalError(w, r, err)
		return
	}
	_ = h.Store.SetLastLogin(r.Context(), user.ID, h.Clock.Now())
	WriteJSON(w, http.StatusOK, userJSON(user))
}

// Logout ends the current session.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	if err := h.Store.DeleteSession(r.Context(), p.SessionID); err != nil {
		internalError(w, r, err)
		return
	}
	h.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetMe returns the user and the org role. The CSRF token of the session is in the header X-CSRF-Token.
func (h *Handlers) GetMe(w http.ResponseWriter, r *http.Request) {
	p := PrincipalFrom(r.Context())
	role := gen.Role(p.Role)
	w.Header().Set(CSRFHeader, p.CSRF)
	WriteJSON(w, http.StatusOK, gen.Me{User: ptr(userJSON(p.User)), OrgRole: &role, ProjectRoles: &[]gen.ProjectRole{}})
}

// UpdateMe changes the own name, and the own password when current_password and new_password are given.
func (h *Handlers) UpdateMe(w http.ResponseWriter, r *http.Request) {
	var body gen.UpdateMeJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p := PrincipalFrom(r.Context())
	if body.NewPassword != nil || body.CurrentPassword != nil {
		if body.NewPassword == nil || body.CurrentPassword == nil {
			invalid(w, r, "new_password", "current_password and new_password are both needed")
			return
		}
		if err := auth.CheckPasswordPolicy(*body.NewPassword); err != nil {
			invalid(w, r, "new_password", err.Error())
			return
		}
		key := "pw:" + p.User.ID
		if locked, wait := h.Throttle.Locked(key); locked {
			h.tooManyAttempts(w, r, wait)
			return
		}
		good := false
		if p.User.PasswordHash != nil {
			good, _ = auth.VerifyPassword(*body.CurrentPassword, *p.User.PasswordHash)
		}
		if !good {
			h.Throttle.Fail(key)
			invalid(w, r, "current_password", "current password is wrong")
			return
		}
		hash, err := auth.HashPassword(*body.NewPassword)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if err := h.Store.SetPassword(r.Context(), p.User.ID, hash, p.SessionID); err != nil {
			internalError(w, r, err)
			return
		}
	}
	if body.Name != nil {
		name, ok := validName(*body.Name)
		if !ok {
			invalid(w, r, "name", "name must have 1 to 100 characters")
			return
		}
		if err := h.Store.UpdateUserName(r.Context(), p.User.ID, name); err != nil {
			internalError(w, r, err)
			return
		}
	}
	user, err := h.Store.UserByID(r.Context(), p.User.ID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, userJSON(user))
}

// GetOrg returns the organization.
func (h *Handlers) GetOrg(w http.ResponseWriter, r *http.Request) {
	o, err := h.Store.GetOrg(r.Context(), PrincipalFrom(r.Context()).OrgID)
	if errors.Is(err, store.ErrNotFound) {
		notFoundError(w, r)
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	var settings gen.OrgSettings
	_ = json.Unmarshal(o.Settings, &settings)
	WriteJSON(w, http.StatusOK, gen.Org{Id: &o.ID, Name: &o.Name, Slug: &o.Slug, SsoRequired: &o.SSORequired, Settings: &settings})
}

// ListMembers lists the members of the org.
func (h *Handlers) ListMembers(w http.ResponseWriter, r *http.Request, _ gen.ListMembersParams) {
	list, err := h.Store.ListMembers(r.Context(), PrincipalFrom(r.Context()).OrgID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	items := make([]gen.Member, 0, len(list))
	for _, m := range list {
		role, source, status := gen.Role(m.Role), gen.MemberSource(m.Source), gen.MemberStatus("active")
		if m.Disabled {
			status = "disabled"
		}
		items = append(items, gen.Member{UserId: ptr(m.UserID), Email: ptr(m.Email), Name: ptr(m.Name), Role: &role,
			Source: &source, Status: &status, LastLoginAt: m.LastLoginAt})
	}
	WriteJSON(w, http.StatusOK, page[gen.Member]{Items: items})
}
