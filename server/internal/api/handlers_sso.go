// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/sso"
	"github.com/polipan/polipan/server/internal/store"
)

// SSODeps are the addresses and the HTTP client of the identity providers. All fields are optional:
// the defaults are the real providers and the SSRF-checked client of the org. Tests and GitHub Enterprise set them.
type SSODeps struct {
	// HTTP makes the client for the calls to a provider.
	HTTP func(ctx context.Context, orgID string) *http.Client
	// GoogleIssuer replaces https://accounts.google.com.
	GoogleIssuer string
	// GitHubAuthURL, GitHubTokenURL and GitHubAPIURL replace the github.com addresses.
	GitHubAuthURL, GitHubTokenURL, GitHubAPIURL string
}

// ssoCookie holds the state of a login in progress. It is sealed with the server key, so the browser
// cannot read or change it, and it ties the callback to the browser that started the login.
const (
	ssoCookie    = "pp_sso"
	ssoStateTTL  = 10 * time.Minute
	ssoCookieAAD = "sso-state"
)

type ssoState struct {
	Provider string    `json:"p"`
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Test     bool      `json:"t,omitempty"`
	Expires  time.Time `json:"e"`
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

func (h *Handlers) ssoConfig(r *http.Request, p *store.SSOProvider) (sso.Config, error) {
	secret, err := auth.Open(h.KEK, p.ClientSecretEnc, "sso:"+p.ID)
	if err != nil {
		return sso.Config{}, err
	}
	redirect, err := h.ssoRedirectURL(p.ID)
	if err != nil {
		return sso.Config{}, err
	}
	c := sso.Config{Kind: p.Kind, ClientID: p.ClientID, ClientSecret: string(secret), Scopes: p.Scopes, Now: h.Clock.Now,
		RedirectURL:   redirect,
		GitHubAuthURL: h.SSO.GitHubAuthURL, GitHubTokenURL: h.SSO.GitHubTokenURL, GitHubAPIURL: h.SSO.GitHubAPIURL}
	if p.GroupsClaim != nil {
		c.GroupsClaim = *p.GroupsClaim
	}
	switch p.Kind {
	case sso.KindOIDC:
		if p.IssuerURL != nil {
			c.Issuer = *p.IssuerURL
		}
	case sso.KindGoogle:
		c.Issuer = h.SSO.GoogleIssuer
		if c.Issuer == "" {
			c.Issuer = sso.GoogleIssuer
		}
	}
	if h.SSO.HTTP != nil {
		c.HTTP = h.SSO.HTTP(r.Context(), p.OrgID)
	} else {
		c.HTTP = h.policy(r.Context(), p.OrgID).Client(15 * time.Second)
	}
	return c, nil
}

// ssoRedirectURL builds the callback address from the external_url setting. It never uses the Host header:
// a forged Host would send the code of a login to another site.
func (h *Handlers) ssoRedirectURL(id string) (string, error) {
	base := strings.TrimRight(h.Settings.ExternalURL, "/")
	u, err := url.Parse(base)
	if err != nil || base == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("sso: external_url is not set")
	}
	return base + BasePath + "/auth/sso/" + id + "/callback", nil
}

func (h *Handlers) ssoCookiePath() string { return BasePath + "/auth/sso" }

func (h *Handlers) setSSOCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: value, Path: h.ssoCookiePath(), MaxAge: maxAge,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode})
}

// ListLoginProviders lists the enabled providers for the login page, with the org policy: the page hides the
// password form when sso_required is on, and shows the recovery login link only when break-glass is on.
func (h *Handlers) ListLoginProviders(w http.ResponseWriter, r *http.Request) {
	if h.Store == nil {
		WriteJSON(w, http.StatusOK, []struct{}{})
		return
	}
	list, err := h.Store.ListLoginProviders(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		SSORequired bool   `json:"sso_required"`
		BreakGlass  bool   `json:"break_glass"`
	}
	out := make([]item, len(list))
	for i, p := range list {
		out[i] = item{p.ID, p.Name, p.Kind, p.SSORequired, p.BreakGlass}
	}
	WriteJSON(w, http.StatusOK, out)
}

// StartSsoLogin sends the browser to the identity provider (authorization code with PKCE, state and nonce).
func (h *Handlers) StartSsoLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	p, err := h.Store.GetSSOProviderByID(r.Context(), providerID)
	if errors.Is(err, store.ErrNotFound) {
		notFoundError(w, r)
		return
	}
	if err != nil || len(h.KEK) == 0 {
		internalError(w, r, errors.Join(err, errors.New("sso: no key")))
		return
	}
	cfg, err := h.ssoConfig(r, p)
	if err != nil {
		internalError(w, r, err)
		return
	}
	start, err := sso.BeginLogin(r.Context(), cfg)
	if err != nil {
		logging.From(r.Context()).Warn("sso start failed", "error", err, "provider", p.ID)
		h.ssoFail(w, r, p, "provider_error", false)
		return
	}
	raw, _ := json.Marshal(ssoState{Provider: p.ID, State: start.State, Nonce: start.Nonce, Verifier: start.Verifier,
		Test: r.URL.Query().Get("test") == "1", Expires: h.Clock.Now().Add(ssoStateTTL)})
	sealed, err := auth.Seal(h.KEK, raw, ssoCookieAAD)
	if err != nil {
		internalError(w, r, err)
		return
	}
	h.setSSOCookie(w, base64.RawURLEncoding.EncodeToString(sealed), int(ssoStateTTL.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, start.URL, http.StatusFound)
}

// ssoFail sends the browser back to the login page (or the SSO settings in test mode) with a short reason.
// The reason is a code from a fixed list, never text of the provider.
func (h *Handlers) ssoFail(w http.ResponseWriter, r *http.Request, p *store.SSOProvider, reason string, test bool) {
	h.setSSOCookie(w, "", -1)
	h.auditSSOFailure(r, p, reason)
	target := "/login?sso_error=" + url.QueryEscape(reason)
	if test {
		target = h.ssoSettingsPath(r, p) + "?sso_test=error&reason=" + url.QueryEscape(reason)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// ssoSettingsPath is the address of the SSO settings page of the org of the provider.
func (h *Handlers) ssoSettingsPath(r *http.Request, p *store.SSOProvider) string {
	org, err := h.Store.GetOrg(r.Context(), p.OrgID)
	if err != nil {
		return "/"
	}
	return "/o/" + url.PathEscape(org.Slug) + "/settings/sso"
}

func (h *Handlers) auditSSOFailure(r *http.Request, p *store.SSOProvider, reason string) {
	if p == nil {
		return
	}
	e := audit.Event{OrgID: p.OrgID, ActorType: audit.ActorSSO, ActorID: p.ID, Action: "auth.sso_login_failed", TargetType: "sso_provider",
		TargetID: p.ID, Diff: map[string]any{"reason": reason}}
	if err := audit.Write(audit.WithRequest(context.WithoutCancel(r.Context()), requestInfo(r)), h.Store.Pool, e); err != nil {
		logging.From(r.Context()).Error("sso audit event not written", "error", err)
	}
}

// requestInfo is the audit request info of a public request.
func requestInfo(r *http.Request) *audit.Request {
	return &audit.Request{ActorType: audit.ActorSystem, ActorID: "anonymous", IP: clientKey(r)[len("ip:"):], UserAgent: r.UserAgent(), RequestID: RequestIDFrom(r.Context())}
}

// FinishSsoLogin is the return address of the provider. It checks state, nonce, PKCE and the ID token, applies the
// rules of the provider (verified email, allowed domains, the Google hd claim), makes or finds the user, applies the
// group mappings and starts a session (REQ-SSO-002 to REQ-SSO-006).
func (h *Handlers) FinishSsoLogin(w http.ResponseWriter, r *http.Request, providerID string, params gen.FinishSsoLoginParams) {
	ctx := r.Context()
	p, err := h.Store.GetSSOProviderByID(ctx, providerID)
	if errors.Is(err, store.ErrNotFound) {
		notFoundError(w, r)
		return
	}
	if err != nil || len(h.KEK) == 0 {
		internalError(w, r, errors.Join(err, errors.New("sso: no key")))
		return
	}
	st, ok := h.readSSOState(r, p.ID, params.State)
	if !ok {
		h.ssoFail(w, r, p, "bad_state", false)
		return
	}
	cfg, err := h.ssoConfig(r, p)
	if err != nil {
		internalError(w, r, err)
		return
	}
	id, err := sso.FinishLogin(ctx, cfg, params.Code, sso.Start{State: st.State, Nonce: st.Nonce, Verifier: st.Verifier})
	if err != nil {
		logging.From(ctx).Warn("sso login failed", "error", err, "provider", p.ID)
		h.ssoFail(w, r, p, "provider_error", st.Test)
		return
	}
	if err := sso.CheckIdentity(p.Kind, p.AllowedDomains, id); err != nil {
		reason := map[error]string{sso.ErrUnverified: "email_not_verified", sso.ErrDomain: "domain_not_allowed", sso.ErrHostedDomain: "domain_not_allowed"}[err]
		h.ssoFail(w, r, p, reason, st.Test)
		return
	}
	if st.Test {
		h.setSSOCookie(w, "", -1)
		http.Redirect(w, r, h.ssoSettingsPath(r, p)+"?sso_test=ok", http.StatusFound)
		return
	}
	defaultRole := ""
	if p.DefaultRole != nil {
		defaultRole = *p.DefaultRole
	}
	res, err := h.Store.SSOLogin(ctx, store.SSOLoginInput{Provider: p, Subject: id.Subject, Email: id.Email, Name: id.Name, Groups: id.Groups,
		AllowCreate: p.AutoCreateUsers && !sso.UnsafeJIT(p.Kind, p.AllowedDomains, p.AutoCreateUsers, defaultRole)})
	switch {
	case errors.Is(err, store.ErrSSONoAccount):
		reason := "no_account"
		if sso.UnsafeJIT(p.Kind, p.AllowedDomains, p.AutoCreateUsers, defaultRole) {
			reason = "unsafe_config"
		}
		h.ssoFail(w, r, p, reason, false)
		return
	case errors.Is(err, store.ErrSSODisabled), errors.Is(err, store.ErrSSOForeignOrg):
		h.ssoFail(w, r, p, "no_access", false)
		return
	case err != nil:
		internalError(w, r, err)
		return
	}
	if res.NoAccess {
		h.ssoFail(w, r, p, "no_access", false)
		return
	}
	if err := h.startSession(w, r, res.UserID); err != nil {
		internalError(w, r, err)
		return
	}
	h.setSSOCookie(w, "", -1)
	if user, err := h.Store.UserByID(ctx, res.UserID); err == nil {
		h.auditLogin(r, user, user.Email, "auth.sso_login", map[string]any{"provider": p.Name, "created": res.Created, "linked": res.Linked})
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusFound)
}

// readSSOState opens the cookie and checks the provider, the expiry and the state (constant time).
func (h *Handlers) readSSOState(r *http.Request, providerID, state string) (*ssoState, bool) {
	c, err := r.Cookie(ssoCookie)
	if err != nil || c.Value == "" || state == "" {
		return nil, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, false
	}
	raw, err := auth.Open(h.KEK, sealed, ssoCookieAAD)
	if err != nil {
		return nil, false
	}
	var st ssoState
	if json.Unmarshal(raw, &st) != nil || st.Provider != providerID || !h.Clock.Now().Before(st.Expires) {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(st.State), []byte(state)) != 1 {
		return nil, false
	}
	return &st, true
}

func ssoProviderJSON(p *store.SSOProvider) gen.SsoProvider {
	kind := gen.SsoProviderKind(p.Kind)
	out := gen.SsoProvider{Id: &p.ID, Kind: &kind, Name: &p.Name, IssuerUrl: p.IssuerURL, ClientId: &p.ClientID, Scopes: &p.Scopes,
		GroupsClaim: p.GroupsClaim, AllowedDomains: &p.AllowedDomains, AutoCreateUsers: &p.AutoCreateUsers, Enabled: &p.Enabled}
	if p.DefaultRole != nil {
		role := gen.Role(*p.DefaultRole)
		out.DefaultRole = &role
	}
	return out
}

// ListSsoProviders lists the providers of the org. The client secret is never returned (REQ-SEC-002).
func (h *Handlers) ListSsoProviders(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListSSOProviders(r.Context(), h.orgID(r))
	if err != nil {
		internalError(w, r, err)
		return
	}
	out := make([]gen.SsoProvider, len(list))
	for i := range list {
		out[i] = ssoProviderJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// buildProvider merges a write body into the current provider (nil for a new one) and checks every rule.
// It writes the answer and returns nil when the result is not valid.
func (h *Handlers) buildProvider(w http.ResponseWriter, r *http.Request, cur *store.SSOProvider, body gen.SsoProviderWrite) *store.SSOProvider {
	p := &store.SSOProvider{ID: store.NewID("sso"), OrgID: h.orgID(r), AutoCreateUsers: true, Enabled: true}
	if cur != nil {
		c := *cur
		p = &c
		p.ClientSecretEnc = nil // nil keeps the stored secret
	}
	if cur == nil {
		if body.Kind == nil || (*body.Kind != gen.SsoProviderWriteKindOidc && *body.Kind != gen.SsoProviderWriteKindGoogle && *body.Kind != gen.SsoProviderWriteKindGithub) {
			invalid(w, r, "kind", "kind must be oidc, google or github")
			return nil
		}
		p.Kind = string(*body.Kind)
	} else if body.Kind != nil && string(*body.Kind) != cur.Kind {
		invalid(w, r, "kind", "the kind of a provider cannot change")
		return nil
	}
	if body.Name != nil {
		name, ok := validName(*body.Name)
		if !ok {
			invalid(w, r, "name", "name must have 1 to 100 characters")
			return nil
		}
		p.Name = name
	}
	if p.Name == "" {
		invalid(w, r, "name", "name is required")
		return nil
	}
	if body.ClientId != nil {
		p.ClientID = strings.TrimSpace(*body.ClientId)
	}
	if p.ClientID == "" || len(p.ClientID) > 512 {
		invalid(w, r, "client_id", "client_id is required")
		return nil
	}
	if body.ClientSecret != nil {
		if len(*body.ClientSecret) == 0 || len(*body.ClientSecret) > 4096 {
			invalid(w, r, "client_secret", "client_secret must have 1 to 4096 characters")
			return nil
		}
	} else if cur == nil {
		invalid(w, r, "client_secret", "client_secret is required")
		return nil
	}
	if body.IssuerUrl != nil {
		p.IssuerURL = body.IssuerUrl
	}
	if p.Kind == sso.KindOIDC {
		if !h.checkIssuer(w, r, p) {
			return nil
		}
	} else {
		p.IssuerURL = nil
	}
	if body.Scopes != nil {
		p.Scopes = *body.Scopes
	}
	p.Scopes = defaultScopes(p.Kind, p.Scopes)
	if body.GroupsClaim != nil {
		gc := strings.TrimSpace(*body.GroupsClaim)
		p.GroupsClaim = &gc
		if gc == "" {
			p.GroupsClaim = nil
		}
	}
	if p.Kind != sso.KindOIDC {
		p.GroupsClaim = nil
	}
	// A generic OIDC provider decides who is "verified" for any email, so whoever sets its issuer, client or groups claim
	// can sign in as any member, an owner too. That needs an owner (REQ-RBAC-008).
	if p.Kind == sso.KindOIDC && PrincipalFrom(r.Context()).OrgRole != auth.RoleOwner {
		changed := cur == nil || !samePtr(p.IssuerURL, cur.IssuerURL) || p.ClientID != cur.ClientID ||
			body.ClientSecret != nil || !samePtr(p.GroupsClaim, cur.GroupsClaim)
		if changed {
			forbidOwnerChange(w, r)
			return nil
		}
	}
	if body.AllowedDomains != nil {
		p.AllowedDomains = sso.NormalizeDomains(*body.AllowedDomains)
	}
	for _, d := range p.AllowedDomains {
		if !domainRe.MatchString(d) {
			invalid(w, r, "allowed_domains", "a domain is not valid: "+d)
			return nil
		}
	}
	if p.AllowedDomains == nil {
		p.AllowedDomains = []string{}
	}
	if body.DefaultRole != nil {
		role := string(*body.DefaultRole)
		if !validRole(role) {
			invalid(w, r, "default_role", "role must be admin, developer or viewer")
			return nil
		}
		p.DefaultRole = &role
	}
	if p.DefaultRole != nil && *p.DefaultRole == auth.RoleOwner {
		invalid(w, r, "default_role", "the default role cannot be owner: map a group to owner instead")
		return nil
	}
	if body.AutoCreateUsers != nil {
		p.AutoCreateUsers = *body.AutoCreateUsers
	}
	if body.Enabled != nil {
		p.Enabled = *body.Enabled
	}
	role := ""
	if p.DefaultRole != nil {
		role = *p.DefaultRole
	}
	if sso.UnsafeJIT(p.Kind, p.AllowedDomains, p.AutoCreateUsers, role) {
		WriteError(w, r, http.StatusUnprocessableEntity, CodeValidationFailed,
			"with a default role, auto-create needs allowed_domains on Google and GitHub: anybody with an account would get access",
			map[string]any{"field": "allowed_domains", "reason": "unsafe_jit"})
		return nil
	}
	if body.ClientSecret != nil {
		enc, err := auth.Seal(h.KEK, []byte(*body.ClientSecret), "sso:"+p.ID)
		if err != nil {
			internalError(w, r, err)
			return nil
		}
		p.ClientSecretEnc = enc
	}
	return p
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func defaultScopes(kind string, given []string) []string {
	seen, out := map[string]bool{}, []string{}
	for _, s := range given {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s], out = true, append(out, s)
		}
	}
	switch kind {
	case sso.KindGitHub:
		if len(out) == 0 {
			return append([]string{}, sso.GitHubScopes...)
		}
		return out
	default:
		for _, need := range []string{"openid", "email"} {
			if !seen[need] {
				out = append(out, need)
			}
		}
		if len(given) == 0 {
			out = append(out, "profile")
		}
		return out
	}
}

// checkIssuer checks the issuer address of a generic provider: https, no query, and not a private address
// (threat 18; the org setting allow_private_targets allows a private address and plain http).
func (h *Handlers) checkIssuer(w http.ResponseWriter, r *http.Request, p *store.SSOProvider) bool {
	if p.IssuerURL == nil || strings.TrimSpace(*p.IssuerURL) == "" {
		invalid(w, r, "issuer_url", "issuer_url is required for a generic OIDC provider")
		return false
	}
	raw := strings.TrimRight(strings.TrimSpace(*p.IssuerURL), "/")
	p.IssuerURL = &raw
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		invalid(w, r, "issuer_url", "issuer_url must be an https address without query")
		return false
	}
	policy := h.policy(r.Context(), p.OrgID)
	if u.Scheme == "http" && !policy.AllowPrivate {
		invalid(w, r, "issuer_url", "issuer_url must use https")
		return false
	}
	if err := policy.CheckURL(r.Context(), raw); err != nil {
		invalid(w, r, "issuer_url", "this address is private or not allowed (the organization setting allow_private_targets is off)")
		return false
	}
	return true
}

// CreateSsoProvider adds a provider.
func (h *Handlers) CreateSsoProvider(w http.ResponseWriter, r *http.Request) {
	var body gen.SsoProviderWrite
	if !decodeJSON(w, r, &body) || len(h.KEK) == 0 {
		return
	}
	p := h.buildProvider(w, r, nil, body)
	if p == nil {
		return
	}
	created, err := h.Store.CreateSSOProvider(r.Context(), p)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusCreated, ssoProviderJSON(created))
}

// UpdateSsoProvider changes a provider. A missing client_secret keeps the stored one.
func (h *Handlers) UpdateSsoProvider(w http.ResponseWriter, r *http.Request, providerID string) {
	var body gen.SsoProviderWrite
	if !decodeJSON(w, r, &body) || len(h.KEK) == 0 {
		return
	}
	cur, err := h.Store.GetSSOProvider(r.Context(), h.orgID(r), providerID)
	if fail(w, r, err) {
		return
	}
	p := h.buildProvider(w, r, cur, body)
	if p == nil {
		return
	}
	if cur.Enabled && !p.Enabled && h.lastProviderGuard(w, r, cur) {
		return
	}
	updated, err := h.Store.UpdateSSOProvider(r.Context(), p)
	if memberFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, ssoProviderJSON(updated))
}

// lastProviderGuard refuses to remove the last enabled provider while sso_required is on: nobody could log in.
func (h *Handlers) lastProviderGuard(w http.ResponseWriter, r *http.Request, cur *store.SSOProvider) bool {
	org, err := h.Store.GetOrg(r.Context(), cur.OrgID)
	if err != nil {
		internalError(w, r, err)
		return true
	}
	if !org.SSORequired {
		return false
	}
	n, err := h.Store.CountEnabledSSOProviders(r.Context(), cur.OrgID, cur.ID)
	if err != nil {
		internalError(w, r, err)
		return true
	}
	if n > 0 {
		return false
	}
	WriteError(w, r, http.StatusConflict, CodeConflict, "sso_required is on: turn it off first, or keep one enabled provider",
		map[string]any{"reason": "sso_required_needs_provider"})
	return true
}

// DeleteSsoProvider removes a provider.
func (h *Handlers) DeleteSsoProvider(w http.ResponseWriter, r *http.Request, providerID string) {
	cur, err := h.Store.GetSSOProvider(r.Context(), h.orgID(r), providerID)
	if fail(w, r, err) {
		return
	}
	if cur.Enabled && h.lastProviderGuard(w, r, cur) {
		return
	}
	if fail(w, r, h.Store.DeleteSSOProvider(r.Context(), cur.OrgID, cur.ID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mappingJSON(m store.GroupMapping) gen.GroupMapping {
	return gen.GroupMapping{GroupValue: m.GroupValue, Role: gen.Role(m.Role), ProjectId: m.ProjectID}
}

// ListGroupMappings lists the group to role mappings of a provider.
func (h *Handlers) ListGroupMappings(w http.ResponseWriter, r *http.Request, providerID string) {
	list, err := h.Store.ListGroupMappings(r.Context(), h.orgID(r), providerID)
	if fail(w, r, err) {
		return
	}
	out := make([]gen.GroupMapping, len(list))
	for i, m := range list {
		out[i] = mappingJSON(m)
	}
	WriteJSON(w, http.StatusOK, out)
}

// ReplaceGroupMappings replaces all mappings. Only an owner can map a group to owner (REQ-RBAC-008).
func (h *Handlers) ReplaceGroupMappings(w http.ResponseWriter, r *http.Request, providerID string) {
	var body gen.ReplaceGroupMappingsJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := h.Store.GetSSOProvider(r.Context(), h.orgID(r), providerID)
	if fail(w, r, err) {
		return
	}
	if len(body) > 500 {
		invalid(w, r, "mappings", "at most 500 mappings")
		return
	}
	list, seen := make([]store.GroupMapping, 0, len(body)), map[string]bool{}
	for _, m := range body {
		group, role := strings.TrimSpace(m.GroupValue), string(m.Role)
		switch {
		case group == "" || len(group) > 255:
			invalid(w, r, "group_value", "group_value must have 1 to 255 characters")
			return
		case !validRole(role):
			invalid(w, r, "role", "role must be owner, admin, developer or viewer")
			return
		case m.ProjectId != nil && role == auth.RoleOwner:
			invalid(w, r, "role", "a project role cannot be owner")
			return
		case p.Kind == sso.KindGitHub && strings.Count(group, "/") != 1:
			invalid(w, r, "group_value", "a GitHub group is written org/team")
			return
		}
		if p.Kind == sso.KindGitHub {
			group = strings.ToLower(group)
		}
		key := group + "\x00"
		if m.ProjectId != nil {
			key += *m.ProjectId
		}
		if seen[key] {
			invalid(w, r, "group_value", "a group is mapped twice for the same scope: "+group)
			return
		}
		seen[key] = true
		list = append(list, store.GroupMapping{GroupValue: group, Role: role, ProjectID: m.ProjectId})
	}
	// Only an owner may add, change or remove a mapping to owner: the store checks it inside its transaction.
	out, err := h.Store.ReplaceGroupMappings(r.Context(), p.OrgID, p.ID, list, PrincipalFrom(r.Context()).OrgRole == auth.RoleOwner)
	if memberFailed(w, r, err) {
		return
	}
	res := make([]gen.GroupMapping, len(out))
	for i, m := range out {
		res[i] = mappingJSON(m)
	}
	WriteJSON(w, http.StatusOK, res)
}
