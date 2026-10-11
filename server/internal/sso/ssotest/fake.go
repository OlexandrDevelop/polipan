// SPDX-License-Identifier: Apache-2.0

// Package ssotest has a fake identity provider for tests and the end to end runs (T-E2E-11):
// an OpenID Connect provider (discovery, authorize, token, JWKS, userinfo) and the GitHub OAuth2 and API
// endpoints. It checks the same things a real provider checks: the client secret and the PKCE verifier.
package ssotest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Fake is one running fake provider. Set Next before the browser reaches /authorize.
type Fake struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string

	mu    sync.Mutex
	key   *rsa.PrivateKey
	codes map[string]grant
	last  map[string]any // claims of the last token, for userinfo
	// Next are the claims of the next login (sub, email, email_verified, name, hd, groups...).
	Next map[string]any
	// GitHub is the account of the next GitHub login.
	GitHub GitHubAccount
	// Base is the public address of the fake: the issuer and the endpoint addresses start with it. Default: the
	// server address. A test can set a host name and dial the server itself.
	Base string
	// Issuer is the issuer the discovery document names. Default: Base.
	Issuer string
}

// GitHubAccount is what the fake GitHub API knows about the next user.
type GitHubAccount struct {
	ID       int64
	Login    string
	Name     string
	Email    string
	Verified bool
	Teams    []string // "org/team"
}

type grant struct {
	nonce, challenge string
	claims           map[string]any
	gh               GitHubAccount
}

// New starts the fake provider. The caller closes Server.
func New() *Fake {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	f := &Fake{ClientID: "polipan-test", ClientSecret: "fake-client-secret", key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/jwks", f.jwks)
	mux.HandleFunc("/userinfo", f.userinfo)
	mux.HandleFunc("/github/login/oauth/authorize", f.authorize)
	mux.HandleFunc("/github/login/oauth/access_token", f.githubToken)
	mux.HandleFunc("/github/api/user", f.githubUser)
	mux.HandleFunc("/github/api/user/emails", f.githubEmails)
	mux.HandleFunc("/github/api/user/teams", f.githubTeams)
	f.Server = httptest.NewServer(mux)
	f.Base, f.Issuer = f.Server.URL, f.Server.URL
	return f
}

// GitHubURLs are the three GitHub addresses, for sso.Config.
func (f *Fake) GitHubURLs() (auth, token, api string) {
	return f.Base + "/github/login/oauth/authorize", f.Base + "/github/login/oauth/access_token", f.Base + "/github/api"
}

func randomText() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{
		"issuer": f.Issuer, "authorization_endpoint": f.Base + "/authorize", "token_endpoint": f.Base + "/token",
		"jwks_uri": f.Base + "/jwks", "userinfo_endpoint": f.Base + "/userinfo",
	})
}

// authorize is the login page: it signs the user in at once and sends the browser back with a code.
func (f *Fake) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" {
		http.Error(w, "bad client", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	code := randomText()
	f.mu.Lock()
	claims := map[string]any{}
	for k, v := range f.Next {
		claims[k] = v
	}
	f.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), claims: claims, gh: f.GitHub}
	f.mu.Unlock()
	rq := redirect.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// take checks the client secret and the PKCE verifier and uses up the code.
func (f *Fake) take(r *http.Request) (grant, bool) {
	_ = r.ParseForm()
	if subtle.ConstantTimeCompare([]byte(r.Form.Get("client_secret")), []byte(f.ClientSecret)) != 1 || r.Form.Get("client_id") != f.ClientID {
		return grant{}, false
	}
	f.mu.Lock()
	g, ok := f.codes[r.Form.Get("code")]
	delete(f.codes, r.Form.Get("code"))
	f.mu.Unlock()
	if !ok {
		return grant{}, false
	}
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if g.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		return grant{}, false
	}
	return g, true
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	g, ok := f.take(r)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	claims := map[string]any{"iss": f.Issuer, "aud": f.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": g.nonce}
	for k, v := range g.claims {
		claims[k] = v
	}
	f.mu.Lock()
	f.last = g.claims
	f.mu.Unlock()
	writeJSON(w, map[string]any{"access_token": "at-" + randomText(), "token_type": "Bearer", "id_token": f.Sign(claims)})
}

// Sign makes a signed ID token. Tests use it to make a bad token too.
func (f *Fake) Sign(claims map[string]any) string {
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	head := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	signed := head + "." + enc(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *Fake) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}})
}

func (f *Fake) userinfo(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	claims := f.last
	f.mu.Unlock()
	writeJSON(w, claims)
}

func (f *Fake) githubToken(w http.ResponseWriter, r *http.Request) {
	g, ok := f.take(r)
	if !ok {
		writeJSON(w, map[string]string{"error": "bad_verification_code"})
		return
	}
	f.mu.Lock()
	f.GitHub = g.gh
	f.mu.Unlock()
	writeJSON(w, map[string]string{"access_token": "gho_" + randomText(), "token_type": "bearer"})
}

func (f *Fake) githubUser(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	a := f.GitHub
	f.mu.Unlock()
	writeJSON(w, map[string]any{"id": a.ID, "login": a.Login, "name": a.Name})
}

func (f *Fake) githubEmails(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	a := f.GitHub
	f.mu.Unlock()
	writeJSON(w, []map[string]any{
		{"email": "noreply@users.example", "primary": false, "verified": true},
		{"email": a.Email, "primary": true, "verified": a.Verified},
	})
}

func (f *Fake) githubTeams(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	a := f.GitHub
	f.mu.Unlock()
	out := []map[string]any{}
	if r.URL.Query().Get("page") == "1" || r.URL.Query().Get("page") == "" {
		for _, t := range a.Teams {
			org, slug := split(t)
			out = append(out, map[string]any{"slug": slug, "organization": map[string]string{"login": org}})
		}
	}
	writeJSON(w, out)
}

func split(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// Subject makes a stable numeric text, for tests that need many users.
func Subject(n int) string { return "sub-" + strconv.Itoa(n) }
