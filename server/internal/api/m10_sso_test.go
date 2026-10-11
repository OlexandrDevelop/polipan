// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/shared/clock"
	"github.com/polipan/polipan/server/internal/sso/ssotest"
	"github.com/polipan/polipan/server/internal/ssrf"
)

// hostResolver sends every name to a private address, so the SSRF check of the issuer passes for the fake.
type hostResolver struct{}

func (hostResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.9.8.7")}, nil
}

// ssoEnv is a test server that talks to a fake identity provider on a made-up host name (idp.test).
type ssoEnv struct {
	*testEnv
	fake *ssotest.Fake
	real *http.Client
}

func newSSOEnv(t *testing.T) *ssoEnv {
	t.Helper()
	fake := ssotest.New()
	t.Cleanup(fake.Server.Close)
	addr := fake.Server.Listener.Addr().String()
	fake.Base = "http://idp.test:" + addr[strings.LastIndex(addr, ":")+1:]
	fake.Issuer = fake.Base
	// Every connection to idp.test goes to the fake.
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.NewManager(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	orch := &fakeOrch{pool: pool}
	clk := clock.NewFake(time.Now())
	policy, _ := ssrf.New(true, "", nil)
	policy.Resolver = hostResolver{}
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: pool, Clock: clk, RateLimit: 100000,
		Pool: pool, Orchestrator: orch, ExternalURL: "http://panel.test", AgentListen: ":9443", SetupToken: "setup-secret", KEK: testKEK, Secrets: sec,
		Git: &api.GitDeps{Policy: func(context.Context, string) ssrf.Policy { return policy }},
		SSO: api.SSODeps{
			HTTP:           func(context.Context, string) *http.Client { return client },
			GoogleIssuer:   fake.Base,
			GitHubAuthURL:  func() string { a, _, _ := fake.GitHubURLs(); return a }(),
			GitHubTokenURL: func() string { _, tk, _ := fake.GitHubURLs(); return tk }(),
			GitHubAPIURL:   func() string { _, _, a := fake.GitHubURLs(); return a }(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &ssoEnv{testEnv: &testEnv{t: t, h: h, pool: pool, orch: orch, clk: clk}, fake: fake, real: client}
}

// browse runs the whole login in a browser: start, the provider page, the callback. It returns the callback
// answer. The browser keeps the cookies of the panel.
func (e *ssoEnv) browse(providerID string, query string) *httptest.ResponseRecorder {
	e.t.Helper()
	c := e.newClient()
	start := c.do("GET", "/api/v1/auth/sso/"+providerID+"/start"+query, nil)
	if start.Code != http.StatusFound {
		e.t.Fatalf("start: %d %s", start.Code, start.Body)
	}
	var state *http.Cookie
	for _, ck := range start.Result().Cookies() {
		if ck.Name == "pp_sso" {
			state = ck
		}
	}
	if state == nil || !state.HttpOnly || state.Path != "/api/v1/auth/sso" {
		e.t.Fatalf("state cookie missing or open: %+v", state)
	}
	return e.finish(c, state, start.Header().Get("Location"), "")
}

// finish goes to the provider page (it signs in at once) and calls the callback with the code.
// wrongState replaces the state of the callback when it is not empty.
func (e *ssoEnv) finish(c *client, state *http.Cookie, authURL, wrongState string) *httptest.ResponseRecorder {
	e.t.Helper()
	resp, err := e.real.Get(authURL)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		e.t.Fatalf("provider page: %d %v", resp.StatusCode, err)
	}
	if wrongState != "" {
		q := back.Query()
		q.Set("state", wrongState)
		back.RawQuery = q.Encode()
	}
	req := httptest.NewRequest("GET", back.RequestURI(), nil)
	req.RemoteAddr = c.addr
	req.AddCookie(state)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func sessionOf(rec *httptest.ResponseRecorder) bool {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "pp_session" && ck.Value != "" && ck.MaxAge >= 0 {
			return true
		}
	}
	return false
}

func (e *ssoEnv) provider(owner *client, body map[string]any) string {
	e.t.Helper()
	body["client_id"], body["client_secret"] = e.fake.ClientID, e.fake.ClientSecret
	return str(owner.ok(201, "POST", "/api/v1/sso-providers", body), "id")
}

func (e *ssoEnv) roleOf(email string) string {
	return e.queryString(`SELECT coalesce((SELECT m.role FROM memberships m JOIN users u ON u.id = m.user_id WHERE u.email = $1), '')`, email)
}

func reason(rec *httptest.ResponseRecorder) string {
	u, _ := url.Parse(rec.Header().Get("Location"))
	return u.Query().Get("sso_error")
}

// AC-SSO-01: a Google provider with an allowed domain makes the user with the mapped (default) role, and checks hd.
// A generic OIDC provider maps groups to roles on every login. Roles that were set by hand stay.
func TestAC_SSO_01_GoogleAndOIDCWithMapping(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()

	// Google: allowed_domains empty with auto-create and a default role is refused.
	if rec := owner.do("POST", "/api/v1/sso-providers", map[string]any{"kind": "google", "name": "Google", "client_id": "x", "client_secret": "y",
		"default_role": "developer", "auto_create_users": true}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "unsafe_jit") {
		t.Fatalf("unsafe provider: %d %s", rec.Code, rec.Body)
	}
	google := e.provider(owner, map[string]any{"kind": "google", "name": "Google", "allowed_domains": []string{"example.com"}, "default_role": "developer"})

	e.fake.Next = map[string]any{"sub": "g-1", "email": "ann@example.com", "email_verified": true, "name": "Ann", "hd": "example.com"}
	rec := e.browse(google, "")
	if rec.Code != 302 || rec.Header().Get("Location") != "/" || !sessionOf(rec) {
		t.Fatalf("google login: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if got := e.roleOf("ann@example.com"); got != "developer" {
		t.Fatalf("role of ann: %q", got)
	}
	if e.count(`SELECT count(*) FROM users WHERE email = 'ann@example.com' AND password_hash IS NULL`) != 1 {
		t.Fatal("the SSO user has a password")
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.sso_login'`) != 1 || e.count(`SELECT count(*) FROM audit_events WHERE action = 'sso.user_create'`) != 1 {
		t.Fatal("the login or the user creation is not audited")
	}

	// A mail address at the domain without the matching hd is not enough (hd is the Workspace of the company).
	e.fake.Next = map[string]any{"sub": "g-2", "email": "eve@example.com", "email_verified": true, "name": "Eve"}
	if rec := e.browse(google, ""); reason(rec) != "domain_not_allowed" || sessionOf(rec) {
		t.Fatalf("login without hd: %s", rec.Header().Get("Location"))
	}
	e.fake.Next = map[string]any{"sub": "g-3", "email": "mal@evil.com", "email_verified": true, "name": "Mal", "hd": "evil.com"}
	if rec := e.browse(google, ""); reason(rec) != "domain_not_allowed" {
		t.Fatalf("login of another domain: %s", rec.Header().Get("Location"))
	}
	if e.count(`SELECT count(*) FROM users WHERE email IN ('eve@example.com','mal@evil.com')`) != 0 {
		t.Fatal("a user was made for a refused login")
	}

	// Generic OIDC with groups. Only an owner may map a group to owner.
	oidc := e.provider(owner, map[string]any{"kind": "oidc", "name": "Keycloak", "issuer_url": e.fake.Base, "groups_claim": "groups",
		"scopes": []string{"openid", "email", "groups"}})
	prj := str(owner.ok(201, "POST", "/api/v1/projects", map[string]any{"name": "Shop"}), "id")
	maps := []map[string]any{
		{"group_value": "eng", "role": "developer"},
		{"group_value": "ops", "role": "admin"},
		{"group_value": "shop-ro", "role": "viewer", "project_id": prj},
	}
	if rec := owner.do("PUT", "/api/v1/sso-providers/"+oidc+"/group-mappings", maps); rec.Code != 200 {
		t.Fatalf("mappings: %d %s", rec.Code, rec.Body)
	}
	admin := e.userClient("usr_adm", "adm@example.com", "admin")
	if rec := admin.do("PUT", "/api/v1/sso-providers/"+oidc+"/group-mappings", []map[string]any{{"group_value": "eng", "role": "owner"}}); rec.Code != 403 {
		t.Fatalf("an admin mapped a group to owner: %d", rec.Code)
	}
	if rec := owner.do("PUT", "/api/v1/sso-providers/"+oidc+"/group-mappings", []map[string]any{{"group_value": "eng", "role": "owner", "project_id": prj}}); rec.Code != 422 {
		t.Fatalf("an owner project role: %d", rec.Code)
	}

	e.fake.Next = map[string]any{"sub": "o-1", "email": "bob@example.com", "email_verified": true, "name": "Bob", "groups": []string{"eng", "ops", "shop-ro"}}
	if rec := e.browse(oidc, ""); rec.Header().Get("Location") != "/" || !sessionOf(rec) {
		t.Fatalf("oidc login: %s", rec.Header().Get("Location"))
	}
	if got := e.roleOf("bob@example.com"); got != "admin" {
		t.Fatalf("highest role must win: %q", got)
	}
	if e.count(`SELECT count(*) FROM project_roles pr JOIN users u ON u.id = pr.user_id WHERE u.email = 'bob@example.com' AND pr.source = 'sso' AND pr.role = 'viewer'`) != 1 {
		t.Fatal("the project mapping was not applied")
	}
	if e.count(`SELECT count(*) FROM memberships WHERE source = 'sso'`) != 2 {
		t.Fatal("the mapped membership has no source sso")
	}

	// The groups change: roles are replaced on the next login. A manual role is not touched.
	e.fake.Next = map[string]any{"sub": "o-1", "email": "bob@example.com", "email_verified": true, "name": "Bob", "groups": []string{"eng"}}
	e.browse(oidc, "")
	if got := e.roleOf("bob@example.com"); got != "developer" || e.count(`SELECT count(*) FROM project_roles WHERE source = 'sso'`) != 0 {
		t.Fatalf("roles after the group change: %q", got)
	}
	e.fake.Next = map[string]any{"sub": "o-2", "email": "adm@example.com", "email_verified": true, "name": "Adm", "groups": []string{"nothing"}}
	if rec := e.browse(oidc, ""); !sessionOf(rec) || e.roleOf("adm@example.com") != "admin" {
		t.Fatalf("a manual role must stay: %q", e.roleOf("adm@example.com"))
	}

	// No group matches and no default role: the user loses access and gets no session.
	e.fake.Next = map[string]any{"sub": "o-1", "email": "bob@example.com", "email_verified": true, "name": "Bob", "groups": []string{"other"}}
	if rec := e.browse(oidc, ""); reason(rec) != "no_access" || sessionOf(rec) || e.roleOf("bob@example.com") != "" {
		t.Fatalf("no access: %s role %q", rec.Header().Get("Location"), e.roleOf("bob@example.com"))
	}
	// A new user with no mapping is not kept.
	e.fake.Next = map[string]any{"sub": "o-9", "email": "new@example.com", "email_verified": true, "groups": []string{"other"}}
	e.browse(oidc, "")
	if e.count(`SELECT count(*) FROM users WHERE email = 'new@example.com'`) != 0 {
		t.Fatal("a user without access was kept")
	}

	// The client secret is stored encrypted and never returned.
	var enc []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT client_secret_enc FROM sso_providers LIMIT 1`).Scan(&enc)
	if len(enc) == 0 || strings.Contains(string(enc), e.fake.ClientSecret) {
		t.Fatal("the client secret is not encrypted")
	}
	if rec := owner.do("GET", "/api/v1/sso-providers", nil); strings.Contains(rec.Body.String(), e.fake.ClientSecret) || strings.Contains(rec.Body.String(), "client_secret") {
		t.Fatalf("the list shows the secret: %s", rec.Body)
	}
}

// REQ-SSO-001, REQ-SSO-002: GitHub OAuth2 with org/team mapping.
func TestREQ_SSO_001_GitHubTeams(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()
	gh := e.provider(owner, map[string]any{"kind": "github", "name": "GitHub", "allowed_domains": []string{"example.com"}})
	if rec := owner.do("PUT", "/api/v1/sso-providers/"+gh+"/group-mappings", []map[string]any{{"group_value": "nope", "role": "viewer"}}); rec.Code != 422 {
		t.Fatalf("a GitHub group needs org/team: %d", rec.Code)
	}
	owner.ok(200, "PUT", "/api/v1/sso-providers/"+gh+"/group-mappings", []map[string]any{{"group_value": "Acme/Devs", "role": "developer"}})

	e.fake.GitHub = ssotest.GitHubAccount{ID: 77, Login: "cat", Name: "Cat", Email: "cat@example.com", Verified: true, Teams: []string{"acme/devs"}}
	if rec := e.browse(gh, ""); rec.Header().Get("Location") != "/" || e.roleOf("cat@example.com") != "developer" {
		t.Fatalf("github login: %s role %q", rec.Header().Get("Location"), e.roleOf("cat@example.com"))
	}
	// An unverified primary email is not an email.
	e.fake.GitHub = ssotest.GitHubAccount{ID: 78, Login: "dog", Email: "dog@example.com", Verified: false, Teams: []string{"acme/devs"}}
	if rec := e.browse(gh, ""); reason(rec) != "email_not_verified" {
		t.Fatalf("unverified github email: %s", rec.Header().Get("Location"))
	}
}

// REQ-SSO-006 and the login CSRF: a wrong state, no cookie, a cookie of another provider, and a bad nonce.
func TestREQ_SSO_006_StateAndNonce(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()
	idp := e.provider(owner, map[string]any{"kind": "oidc", "name": "Keycloak", "issuer_url": e.fake.Base, "default_role": "viewer"})
	e.fake.Next = map[string]any{"sub": "s-1", "email": "sam@example.com", "email_verified": true}

	c := e.newClient()
	start := c.do("GET", "/api/v1/auth/sso/"+idp+"/start", nil)
	var state *http.Cookie
	for _, ck := range start.Result().Cookies() {
		if ck.Name == "pp_sso" {
			state = ck
		}
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("response_type") != "code" {
		t.Fatalf("the authorization request lacks PKCE, state or nonce: %v", q)
	}
	if rec := e.finish(c, state, start.Header().Get("Location"), "another-state"); reason(rec) != "bad_state" || sessionOf(rec) {
		t.Fatalf("wrong state: %s", rec.Header().Get("Location"))
	}
	// No cookie at all.
	req := httptest.NewRequest("GET", "/api/v1/auth/sso/"+idp+"/callback?code=x&state=y", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if reason(rec) != "bad_state" {
		t.Fatalf("no cookie: %s", rec.Header().Get("Location"))
	}
	// A token with a wrong nonce or audience is refused.
	for name, claims := range map[string]map[string]any{
		"nonce":    {"nonce": "other"},
		"audience": {"aud": "someone-else"},
		"expired":  {"exp": time.Now().Add(-time.Hour).Unix()},
		"issuer":   {"iss": "http://evil.test"},
	} {
		e.fake.Next = map[string]any{"sub": "s-2", "email": "sam@example.com", "email_verified": true}
		for k, v := range claims {
			e.fake.Next[k] = v
		}
		if rec := e.browse(idp, ""); reason(rec) != "provider_error" || sessionOf(rec) {
			t.Fatalf("a token with a bad %s was accepted: %s", name, rec.Header().Get("Location"))
		}
	}
	// A bad issuer address cannot be saved.
	if rec := owner.do("POST", "/api/v1/sso-providers", map[string]any{"kind": "oidc", "name": "Bad", "issuer_url": "ftp://x.example.com", "client_id": "a", "client_secret": "b"}); rec.Code != 422 {
		t.Fatalf("bad issuer: %d", rec.Code)
	}
}

// AC-SSO-02: an IdP that says email_verified = false for the email of a local user: no link, login refused.
func TestAC_SSO_02_UnverifiedEmailIsNotLinked(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()
	idp := e.provider(owner, map[string]any{"kind": "oidc", "name": "Keycloak", "issuer_url": e.fake.Base, "default_role": "viewer"})

	e.fake.Next = map[string]any{"sub": "u-1", "email": "owner@example.com", "email_verified": false, "name": "Fake Olga"}
	rec := e.browse(idp, "")
	if reason(rec) != "email_not_verified" || sessionOf(rec) {
		t.Fatalf("unverified email: %s", rec.Header().Get("Location"))
	}
	if e.count(`SELECT count(*) FROM sso_identities`) != 0 {
		t.Fatal("an identity was linked to the local user")
	}
	// The text "true" is not a verified email either way: a missing claim counts as false.
	e.fake.Next = map[string]any{"sub": "u-1", "email": "owner@example.com", "name": "Fake Olga"}
	if rec := e.browse(idp, ""); reason(rec) != "email_not_verified" {
		t.Fatalf("missing email_verified: %s", rec.Header().Get("Location"))
	}
	// A verified email links to the local user by email. Next time the subject is enough.
	e.fake.Next = map[string]any{"sub": "u-1", "email": "owner@example.com", "email_verified": true}
	if rec := e.browse(idp, ""); !sessionOf(rec) || e.count(`SELECT count(*) FROM sso_identities`) != 1 || e.roleOf("owner@example.com") != "owner" {
		t.Fatalf("verified link: %s role %q", rec.Header().Get("Location"), e.roleOf("owner@example.com"))
	}
	e.fake.Next = map[string]any{"sub": "u-1", "email": "changed@example.com", "email_verified": true}
	if rec := e.browse(idp, ""); !sessionOf(rec) || e.count(`SELECT count(*) FROM users`) != 1 {
		t.Fatalf("login by subject: %s", rec.Header().Get("Location"))
	}
}

// AC-SSO-03: sso_required blocks the password of everybody but an owner with allow_owner_password and 2FA.
// The owner login is audited as auth.break_glass.
func TestAC_SSO_03_SSORequiredAndBreakGlass(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()
	dev := e.userClient("usr_dev", "dev@example.com", "developer")

	// Turning it on needs an enabled provider.
	if rec := owner.do("PATCH", "/api/v1/org", map[string]any{"sso_required": true}); rec.Code != 409 {
		t.Fatalf("sso_required without a provider: %d %s", rec.Code, rec.Body)
	}
	idp := e.provider(owner, map[string]any{"kind": "oidc", "name": "Keycloak", "issuer_url": e.fake.Base, "default_role": "viewer"})
	owner.ok(200, "PATCH", "/api/v1/org", map[string]any{"sso_required": true})
	if rec := owner.do("PATCH", "/api/v1/sso-providers/"+idp, map[string]any{"enabled": false}); rec.Code != 409 {
		t.Fatalf("the last provider was disabled: %d", rec.Code)
	}
	if rec := owner.do("DELETE", "/api/v1/sso-providers/"+idp, nil); rec.Code != 409 {
		t.Fatalf("the last provider was deleted: %d", rec.Code)
	}
	_ = dev

	login := func(email, code string) *httptest.ResponseRecorder {
		body := map[string]any{"email": email, "password": ownerPassword}
		if code != "" {
			body["totp_code"] = code
		}
		return e.newClient().do("POST", "/api/v1/auth/login", body)
	}
	if rec := login("dev@example.com", ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "sso_required") {
		t.Fatalf("developer password login: %d %s", rec.Code, rec.Body)
	}
	// The wrong password is still the normal 401: sso_required says nothing to a stranger.
	if rec := e.newClient().do("POST", "/api/v1/auth/login", map[string]any{"email": "dev@example.com", "password": "wrong wrong wrong"}); rec.Code != 401 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	// An owner without 2FA is blocked too.
	if rec := login("owner@example.com", ""); rec.Code != 403 {
		t.Fatalf("owner without 2FA: %d %s", rec.Code, rec.Body)
	}
	// The list for the login page tells the page to hide the form, and that break-glass is on.
	list := e.newClient().do("GET", "/api/v1/auth/sso/providers", nil)
	if !strings.Contains(list.Body.String(), `"sso_required":true`) || !strings.Contains(list.Body.String(), `"break_glass":true`) {
		t.Fatalf("login providers: %s", list.Body)
	}

	// The owner turns on 2FA (while the session is still open), then the break-glass login works with the code.
	en := owner.ok(200, "POST", "/api/v1/auth/totp/enroll", nil)
	secret := secretOf(t, str(en, "otpauth_uri"))
	owner.ok(200, "POST", "/api/v1/auth/totp/verify", map[string]any{"totp_code": auth.TOTPCode(secret, e.clk.Now())})
	e.clk.Advance(time.Minute)
	if rec := login("owner@example.com", ""); rec.Code != 401 || !strings.Contains(rec.Body.String(), "totp_required") {
		t.Fatalf("owner with 2FA, no code: %d %s", rec.Code, rec.Body)
	}
	if rec := login("owner@example.com", auth.TOTPCode(secret, e.clk.Now())); rec.Code != 200 {
		t.Fatalf("break-glass login: %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.break_glass'`) != 1 {
		t.Fatal("the break-glass login is not audited as auth.break_glass")
	}
	// allow_owner_password off: even the owner is blocked.
	owner.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"allow_owner_password": false}})
	e.clk.Advance(time.Minute)
	if rec := login("owner@example.com", auth.TOTPCode(secret, e.clk.Now())); rec.Code != 403 {
		t.Fatalf("owner with allow_owner_password off: %d", rec.Code)
	}
	list = e.newClient().do("GET", "/api/v1/auth/sso/providers", nil)
	if !strings.Contains(list.Body.String(), `"break_glass":false`) {
		t.Fatalf("break-glass must show off: %s", list.Body)
	}
}

// T-E2E-11: SSO with a test OIDC provider: group mapping, unverified email, sso_required, break-glass, in one run.
func TestT_E2E_11_SSOWithFakeProvider(t *testing.T) {
	e := newSSOEnv(t)
	owner := e.owner()
	idp := e.provider(owner, map[string]any{"kind": "oidc", "name": "Fake IdP", "issuer_url": e.fake.Base, "groups_claim": "groups"})
	owner.ok(200, "PUT", "/api/v1/sso-providers/"+idp+"/group-mappings", []map[string]any{{"group_value": "devs", "role": "developer"}})

	// 1. group mapping and user creation
	e.fake.Next = map[string]any{"sub": "e-1", "email": "dana@example.com", "email_verified": true, "name": "Dana", "groups": []string{"devs"}}
	rec := e.browse(idp, "")
	if !sessionOf(rec) || e.roleOf("dana@example.com") != "developer" {
		t.Fatalf("mapped login: %s", rec.Header().Get("Location"))
	}
	// the session works
	dana := e.newClient()
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "pp_session" {
			dana.cookie = ck
		}
	}
	me := dana.ok(200, "GET", "/api/v1/me", nil)
	if str(me, "org_role") != "developer" {
		t.Fatalf("me: %v", me)
	}
	// 2. unverified email of a local user
	e.fake.Next = map[string]any{"sub": "e-2", "email": "owner@example.com", "email_verified": false}
	if rec := e.browse(idp, ""); reason(rec) != "email_not_verified" || sessionOf(rec) {
		t.Fatalf("unverified: %s", rec.Header().Get("Location"))
	}
	// 3. sso_required: a local developer is blocked, the SSO user has no password anyway
	owner.ok(200, "PATCH", "/api/v1/org", map[string]any{"sso_required": true})
	if rec := e.newClient().do("POST", "/api/v1/auth/login", map[string]any{"email": "dana@example.com", "password": ownerPassword}); rec.Code != 401 {
		t.Fatalf("an SSO user has no password: %d", rec.Code)
	}
	// 4. break-glass
	en := owner.ok(200, "POST", "/api/v1/auth/totp/enroll", nil)
	secret := secretOf(t, str(en, "otpauth_uri"))
	owner.ok(200, "POST", "/api/v1/auth/totp/verify", map[string]any{"totp_code": auth.TOTPCode(secret, e.clk.Now())})
	e.clk.Advance(time.Minute)
	if rec := e.newClient().do("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": ownerPassword, "totp_code": auth.TOTPCode(secret, e.clk.Now())}); rec.Code != 200 {
		t.Fatalf("break-glass: %d %s", rec.Code, rec.Body)
	}
	// 5. the test login of the settings page makes no session and no user
	e.fake.Next = map[string]any{"sub": "e-3", "email": "test@example.com", "email_verified": true}
	rec = e.browse(idp, "?test=1")
	if !strings.HasPrefix(rec.Header().Get("Location"), "/settings/sso?sso_test=ok") || sessionOf(rec) || e.count(`SELECT count(*) FROM users WHERE email = 'test@example.com'`) != 0 {
		t.Fatalf("test login: %s", rec.Header().Get("Location"))
	}
}
