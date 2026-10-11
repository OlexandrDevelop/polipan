// SPDX-License-Identifier: Apache-2.0

package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Provider kinds, as in sso_providers.kind.
const (
	KindOIDC   = "oidc"
	KindGoogle = "google"
	KindGitHub = "github"
)

// GoogleIssuer is the issuer of the Google preset.
const GoogleIssuer = "https://accounts.google.com"

// Config is what a login with one provider needs.
type Config struct {
	Kind         string
	Issuer       string // oidc: issuer_url. google: GoogleIssuer, or a test server.
	ClientID     string
	ClientSecret string
	Scopes       []string
	GroupsClaim  string
	RedirectURL  string
	HTTP         *http.Client
	Now          func() time.Time

	// GitHub endpoints. Empty means the real ones. Tests and GitHub Enterprise set them.
	GitHubAuthURL  string
	GitHubTokenURL string
	GitHubAPIURL   string
}

// Identity is who the provider says the user is.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
	// HostedDomain is the Google hd claim (the Google Workspace domain).
	HostedDomain string
}

// Start is the first step of a login: the address to send the browser to, and the secrets to keep
// until the callback.
type Start struct {
	URL      string
	State    string
	Nonce    string
	Verifier string
}

const maxBody = 1 << 20

func randomText(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BeginLogin makes the state, the nonce and the PKCE verifier and the authorization address.
func BeginLogin(ctx context.Context, c Config) (*Start, error) {
	state, err1 := randomText(24)
	nonce, err2 := randomText(24)
	verifier, err3 := randomText(48)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	s := &Start{State: state, Nonce: nonce, Verifier: verifier}
	q := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {c.RedirectURL},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	if c.Kind == KindGitHub {
		q.Set("scope", strings.Join(c.Scopes, " "))
		base := orDefault(c.GitHubAuthURL, "https://github.com/login/oauth/authorize")
		s.URL = base + "?" + q.Encode()
		return s, nil
	}
	doc, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	q.Set("scope", strings.Join(c.Scopes, " "))
	q.Set("nonce", nonce)
	sep := "?"
	if strings.Contains(doc.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	s.URL = doc.AuthorizationEndpoint + sep + q.Encode()
	return s, nil
}

func orDefault(v, d string) string {
	if v != "" {
		return v
	}
	return d
}

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

func (c Config) getJSON(ctx context.Context, address, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.do(req, out)
}

func (c Config) do(req *http.Request, out any) error {
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("sso: request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sso: %s answered %d", req.URL.Host, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sso: %s sent a bad answer", req.URL.Host)
	}
	return nil
}

// discover reads the discovery document and checks that it names the same issuer.
func (c Config) discover(ctx context.Context) (*discovery, error) {
	var doc discovery
	if err := c.getJSON(ctx, strings.TrimRight(c.Issuer, "/")+"/.well-known/openid-configuration", "", &doc); err != nil {
		return nil, err
	}
	if strings.TrimRight(doc.Issuer, "/") != strings.TrimRight(c.Issuer, "/") {
		return nil, errors.New("sso: the discovery document names another issuer")
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return nil, errors.New("sso: the discovery document is incomplete")
	}
	return &doc, nil
}

type tokenAnswer struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	Error       string `json:"error"`
}

func (c Config) exchange(ctx context.Context, endpoint, code, verifier string) (*tokenAnswer, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out tokenAnswer
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	if out.Error != "" || out.AccessToken == "" {
		return nil, errors.New("sso: the provider refused the code")
	}
	return &out, nil
}

// FinishLogin trades the code for the identity. For OIDC the ID token is verified; the issuer, the
// audience, the nonce and the signature must be right.
func FinishLogin(ctx context.Context, c Config, code string, s Start) (*Identity, error) {
	if c.Kind == KindGitHub {
		return c.finishGitHub(ctx, code, s.Verifier)
	}
	doc, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := c.exchange(ctx, doc.TokenEndpoint, code, s.Verifier)
	if err != nil {
		return nil, err
	}
	if tok.IDToken == "" {
		return nil, errors.New("sso: the provider sent no ID token")
	}
	var keys jwks
	if err := c.getJSON(ctx, doc.JWKSURI, "", &keys); err != nil {
		return nil, err
	}
	issuers := []string{c.Issuer, strings.TrimRight(c.Issuer, "/")}
	if c.Kind == KindGoogle {
		issuers = append(issuers, "accounts.google.com")
	}
	claims, err := verifyIDToken(tok.IDToken, keys.Keys, tokenCheck{Issuers: issuers, Audience: c.ClientID, Nonce: s.Nonce, Now: c.now()})
	if err != nil {
		return nil, err
	}
	id := identityFrom(claims, c.GroupsClaim)
	// Some providers keep the email and the groups out of the ID token.
	if (id.Email == "" || (c.GroupsClaim != "" && id.Groups == nil)) && doc.UserinfoEndpoint != "" {
		var info Claims
		if err := c.getJSON(ctx, doc.UserinfoEndpoint, tok.AccessToken, &info); err == nil && info.str("sub") == id.Subject {
			more := identityFrom(info, c.GroupsClaim)
			if id.Email == "" {
				id.Email, id.EmailVerified = more.Email, more.EmailVerified
			}
			if id.Name == "" {
				id.Name = more.Name
			}
			if id.Groups == nil {
				id.Groups = more.Groups
			}
		}
	}
	return id, nil
}

func identityFrom(c Claims, groupsClaim string) *Identity {
	id := &Identity{
		Subject:       c.str("sub"),
		Email:         strings.ToLower(strings.TrimSpace(c.str("email"))),
		EmailVerified: c.boolClaim("email_verified"),
		Name:          c.str("name"),
		HostedDomain:  strings.ToLower(c.str("hd")),
	}
	if groupsClaim != "" {
		id.Groups = c.strings(groupsClaim)
	}
	return id
}
