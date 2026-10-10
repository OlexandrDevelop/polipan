// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Repo is a repository a connection can reach.
type Repo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// Provider is the API of a Git hosting service. Tests use Fake.
type Provider interface {
	// ListRepos lists the repositories of the connection. q filters by name (may be empty).
	ListRepos(ctx context.Context, q string) ([]Repo, error)
	// CloneAuth returns the user name and password for an https clone. Empty means no auth.
	CloneAuth(ctx context.Context) (user, password string, err error)
	// CloneURL returns the https clone URL of a repo ("owner/name" or a full URL).
	CloneURL(repo string) string
}

// Connection is a stored Git connection with its secret values.
type Connection struct {
	Provider       string
	BaseURL        string // empty: github.com, gitlab.com
	AppID          string // GitHub App
	InstallationID string // GitHub App
	Token          string // GitLab and Gitea token
	PrivateKey     string // GitHub App private key (PEM)
}

// NewProvider makes the provider of a connection. client must be an SSRF-checked client
// (ssrf.Policy.Client), because the base URL comes from the user.
func NewProvider(c Connection, client *http.Client) (Provider, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	switch c.Provider {
	case GitHub:
		api, web := "https://api.github.com", "https://github.com"
		if base != "" {
			api, web = base+"/api/v3", base
		}
		if c.AppID == "" || c.InstallationID == "" || c.PrivateKey == "" {
			return nil, errors.New("git: a GitHub connection needs app_id, installation_id and private_key")
		}
		key, err := parseRSAKey(c.PrivateKey)
		if err != nil {
			return nil, err
		}
		return &githubApp{api: api, web: web, appID: c.AppID, installation: c.InstallationID, key: key, client: client}, nil
	case GitLab:
		if base == "" {
			base = "https://gitlab.com"
		}
		if c.Token == "" {
			return nil, errors.New("git: a GitLab connection needs a token")
		}
		return &gitlab{base: base, token: c.Token, client: client}, nil
	case Gitea:
		if base == "" {
			return nil, errors.New("git: a Gitea connection needs base_url")
		}
		if c.Token == "" {
			return nil, errors.New("git: a Gitea connection needs a token")
		}
		return &gitea{base: base, token: c.Token, client: client}, nil
	case Generic:
		return generic{base: base, token: c.Token}, nil
	}
	return nil, fmt.Errorf("git: unknown provider %q", c.Provider)
}

func parseRSAKey(p string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, errors.New("git: private_key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("git: private_key is not an RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("git: private_key is not an RSA key")
	}
	return rk, nil
}

func cloneURL(web, repo string) string {
	if strings.Contains(repo, "://") || strings.Contains(repo, "@") {
		return repo
	}
	return web + "/" + strings.Trim(repo, "/") + ".git"
}

// getJSON does a GET with the auth header and decodes the JSON answer into dst.
func getJSON(ctx context.Context, client *http.Client, u string, hdr http.Header, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return doJSON(client, req, hdr, dst)
}

func doJSON(client *http.Client, req *http.Request, hdr http.Header, dst any) error {
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("git: %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("git: %s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	return json.Unmarshal(raw, dst)
}

// ---- GitHub App ----

type githubApp struct {
	api, web     string
	appID        string
	installation string
	key          *rsa.PrivateKey
	client       *http.Client
}

// appJWT is the JWT of the app (RS256, 9 minutes).
func (g *githubApp) appJWT() (string, error) {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	head := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now - 60, "exp": now + 540, "iss": g.appID})
	signing := head + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// installationToken gets a short-lived installation token.
func (g *githubApp) installationToken(ctx context.Context) (string, error) {
	jwt, err := g.appJWT()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.api+"/app/installations/"+url.PathEscape(g.installation)+"/access_tokens", nil)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	hdr := http.Header{"Authorization": {"Bearer " + jwt}, "X-Github-Api-Version": {"2022-11-28"}}
	if err := doJSON(g.client, req, hdr, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("git: GitHub gave no installation token")
	}
	return out.Token, nil
}

func (g *githubApp) ListRepos(ctx context.Context, q string) ([]Repo, error) {
	tok, err := g.installationToken(ctx)
	if err != nil {
		return nil, err
	}
	var out []Repo
	for page := 1; page <= 10; page++ {
		var body struct {
			Repositories []Repo `json:"repositories"`
		}
		u := g.api + "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := getJSON(ctx, g.client, u, http.Header{"Authorization": {"Bearer " + tok}}, &body); err != nil {
			return nil, err
		}
		out = append(out, filterRepos(body.Repositories, q)...)
		if len(body.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

func (g *githubApp) CloneAuth(ctx context.Context) (string, string, error) {
	tok, err := g.installationToken(ctx)
	return "x-access-token", tok, err
}

func (g *githubApp) CloneURL(repo string) string { return cloneURL(g.web, repo) }

// ---- GitLab ----

type gitlab struct {
	base, token string
	client      *http.Client
}

func (g *gitlab) ListRepos(ctx context.Context, q string) ([]Repo, error) {
	var body []struct {
		PathWithNamespace string `json:"path_with_namespace"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"`
	}
	u := g.base + "/api/v4/projects?membership=true&simple=true&per_page=100&search=" + url.QueryEscape(q)
	if err := getJSON(ctx, g.client, u, http.Header{"Private-Token": {g.token}}, &body); err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(body))
	for _, p := range body {
		out = append(out, Repo{FullName: p.PathWithNamespace, DefaultBranch: p.DefaultBranch, Private: p.Visibility != "public"})
	}
	return out, nil
}

func (g *gitlab) CloneAuth(context.Context) (string, string, error) { return "oauth2", g.token, nil }

func (g *gitlab) CloneURL(repo string) string { return cloneURL(g.base, repo) }

// ---- Gitea and Forgejo ----

type gitea struct {
	base, token string
	client      *http.Client
}

func (g *gitea) ListRepos(ctx context.Context, q string) ([]Repo, error) {
	var body struct {
		Data []Repo `json:"data"`
	}
	u := g.base + "/api/v1/repos/search?limit=50&q=" + url.QueryEscape(q)
	if err := getJSON(ctx, g.client, u, http.Header{"Authorization": {"token " + g.token}}, &body); err != nil {
		return nil, err
	}
	return body.Data, nil
}

func (g *gitea) CloneAuth(context.Context) (string, string, error) { return "polipan", g.token, nil }

func (g *gitea) CloneURL(repo string) string { return cloneURL(g.base, repo) }

// ---- generic (public URL or deploy key) ----

type generic struct{ base, token string }

func (generic) ListRepos(context.Context, string) ([]Repo, error) {
	return nil, errors.New("git: a generic connection cannot list repositories")
}

func (g generic) CloneAuth(context.Context) (string, string, error) {
	if g.token == "" {
		return "", "", nil
	}
	return "polipan", g.token, nil
}

func (g generic) CloneURL(repo string) string { return cloneURL(g.base, repo) }

func filterRepos(list []Repo, q string) []Repo {
	if q == "" {
		return list
	}
	var out []Repo
	for _, r := range list {
		if strings.Contains(strings.ToLower(r.FullName), strings.ToLower(q)) {
			out = append(out, r)
		}
	}
	return out
}
