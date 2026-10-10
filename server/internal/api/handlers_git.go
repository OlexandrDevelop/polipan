// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
)

// GitDeps are the parts of the Git operations (WP2.1).
type GitDeps struct {
	// Keys holds tokens, app keys, webhook secrets and deploy keys.
	Keys git.KeyStore
	// Policy is the SSRF deny list of an org (REQ-SEC-104).
	Policy func(ctx context.Context, orgID string) ssrf.Policy
	// NewProvider makes the provider of a connection. Nil means git.NewProvider with Policy.Client.
	NewProvider func(c git.Connection, p ssrf.Policy) (git.Provider, error)
	// Cloner reads the config file of a project (M6). Nil means git.CLI.
	Cloner git.Cloner
}

func (h *Handlers) gitReady(w http.ResponseWriter, r *http.Request) bool {
	if h.Git == nil || h.Git.Keys == nil {
		internalError(w, r, errors.New("git is not set up on this server"))
		return false
	}
	return true
}

func (h *Handlers) policy(ctx context.Context, orgID string) ssrf.Policy {
	if h.Git != nil && h.Git.Policy != nil {
		return h.Git.Policy(ctx, orgID)
	}
	p, _ := ssrf.New(false, "", ssrf.DefaultDockerNetworks)
	return p
}

// webhookURL is the public address of /webhooks/git/{id}.
func (h *Handlers) webhookURL(r *http.Request, id string) string {
	base := strings.TrimRight(h.Settings.ExternalURL, "/")
	if base == "" {
		base = "http://" + r.Host
	}
	return base + BasePath + "/webhooks/git/" + id
}

func (h *Handlers) gitConnJSON(r *http.Request, c *store.GitConnection) gen.GitConnection {
	p := gen.GitConnectionProvider(c.Provider)
	return gen.GitConnection{Id: ptr(c.ID), Name: ptr(c.Name), Provider: &p, BaseUrl: c.BaseURL, WebhookUrl: ptr(h.webhookURL(r, c.ID))}
}

// ListGitConnections lists the Git connections of the org.
func (h *Handlers) ListGitConnections(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListGitConnections(r.Context(), h.orgID(r))
	if fail(w, r, err) {
		return
	}
	out := make([]gen.GitConnection, len(list))
	for i := range list {
		out[i] = h.gitConnJSON(r, &list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateGitConnection adds a connection. The token, the app key and the webhook secret go to
// the key store. The webhook secret is in the answer once.
func (h *Handlers) CreateGitConnection(w http.ResponseWriter, r *http.Request) {
	var body gen.GitConnectionWrite
	if !decodeJSON(w, r, &body) {
		return
	}
	if !h.gitReady(w, r) {
		return
	}
	ctx, orgID := r.Context(), h.orgID(r)
	provider := string(body.Provider)
	name, ok := validName(body.Name)
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	conn := git.Connection{Provider: provider, BaseURL: strings.TrimRight(val(body.BaseUrl), "/"),
		AppID: val(body.AppId), InstallationID: val(body.InstallationId), Token: val(body.Token), PrivateKey: val(body.PrivateKey)}
	switch {
	case !git.ValidProvider(provider):
		invalid(w, r, "provider", "provider must be github, gitlab, gitea or generic")
		return
	case !ok:
		invalid(w, r, "name", "name must have 1 to 100 characters")
		return
	}
	if conn.BaseURL != "" {
		if !strings.HasPrefix(conn.BaseURL, "https://") && !strings.HasPrefix(conn.BaseURL, "http://") {
			invalid(w, r, "base_url", "base_url must be an http or https URL")
			return
		}
		if err := h.policy(ctx, orgID).CheckURL(ctx, conn.BaseURL); err != nil {
			invalid(w, r, "base_url", "base_url is not allowed: "+err.Error())
			return
		}
	}
	// The provider checks the fields (and the key format) before anything is stored.
	if _, err := git.NewProvider(conn, http.DefaultClient); err != nil {
		invalid(w, r, "provider", err.Error())
		return
	}
	scope := git.Scope{OrgID: orgID}
	auth := store.GitAuth{AppID: conn.AppID, InstallationID: conn.InstallationID}
	secret := conn.Token
	if provider == git.GitHub {
		secret = conn.PrivateKey
	}
	var err error
	if secret != "" {
		if auth.SecretRef, err = h.Git.Keys.Put(ctx, scope, "git-token", secret); fail(w, r, err) {
			return
		}
	}
	hook := val(body.WebhookSecret)
	if hook == "" {
		if hook, err = git.NewWebhookSecret(); fail(w, r, err) {
			return
		}
	}
	if auth.WebhookSecretRef, err = h.Git.Keys.Put(ctx, scope, "git-webhook", hook); fail(w, r, err) {
		return
	}
	var base *string
	if conn.BaseURL != "" {
		base = &conn.BaseURL
	}
	c, err := h.Store.CreateGitConnection(ctx, store.GitConnection{OrgID: orgID, Provider: provider, Name: name, BaseURL: base, Auth: auth})
	if fail(w, r, err) {
		return
	}
	out := h.gitConnJSON(r, c)
	out.WebhookSecret = &hook
	WriteJSON(w, http.StatusCreated, out)
}

// DeleteGitConnection removes a connection that no service uses, and its secrets.
func (h *Handlers) DeleteGitConnection(w http.ResponseWriter, r *http.Request, connectionID gen.ConnectionId) {
	c, err := h.Store.DeleteGitConnection(r.Context(), h.orgID(r), connectionID)
	if errors.Is(err, store.ErrInUse) {
		WriteError(w, r, http.StatusConflict, CodeConflict, "services use this connection", map[string]any{"reason": "in_use"})
		return
	}
	if fail(w, r, err) {
		return
	}
	if h.Git != nil && h.Git.Keys != nil {
		for _, ref := range []string{c.Auth.SecretRef, c.Auth.WebhookSecretRef} {
			if ref != "" {
				if err := h.Git.Keys.Delete(r.Context(), ref); err != nil {
					logging.From(r.Context()).Warn("delete git secret", "error", err)
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// providerOf makes the provider of a stored connection.
func (h *Handlers) providerOf(ctx context.Context, c *store.GitConnection) (git.Provider, error) {
	conn := git.Connection{Provider: c.Provider, AppID: c.Auth.AppID, InstallationID: c.Auth.InstallationID}
	if c.BaseURL != nil {
		conn.BaseURL = *c.BaseURL
	}
	if c.Auth.SecretRef != "" {
		v, err := h.Git.Keys.Get(ctx, c.Auth.SecretRef)
		if err != nil {
			return nil, err
		}
		if c.Provider == git.GitHub {
			conn.PrivateKey = v
		} else {
			conn.Token = v
		}
	}
	p := h.policy(ctx, c.OrgID)
	if h.Git.NewProvider != nil {
		return h.Git.NewProvider(conn, p)
	}
	return git.NewProvider(conn, p.Client(30*time.Second))
}

// ListGitRepos lists the repositories the connection reaches.
func (h *Handlers) ListGitRepos(w http.ResponseWriter, r *http.Request, connectionID gen.ConnectionId, params gen.ListGitReposParams) {
	if !h.gitReady(w, r) {
		return
	}
	c, err := h.Store.GetGitConnection(r.Context(), h.orgID(r), connectionID)
	if fail(w, r, err) {
		return
	}
	prov, err := h.providerOf(r.Context(), c)
	if fail(w, r, err) {
		return
	}
	q := ""
	if params.Q != nil {
		q = *params.Q
	}
	repos, err := prov.ListRepos(r.Context(), q)
	if err != nil {
		WriteError(w, r, http.StatusConflict, CodeConflict, "the Git provider did not answer: "+err.Error(), map[string]any{"reason": "provider_error"})
		return
	}
	if repos == nil {
		repos = []git.Repo{}
	}
	WriteJSON(w, http.StatusOK, repos)
}

// CreateDeployKey makes an SSH key pair for a git service (the private key goes to the key store,
// the public key stays in the service source) and the webhook of the service.
// The webhook secret is in the answer once.
func (h *Handlers) CreateDeployKey(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId) {
	if !h.gitReady(w, r) {
		return
	}
	ctx := r.Context()
	s, err := h.Store.GetService(ctx, h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:update", s.EnvProtected) {
		return
	}
	src, ok := git.SourceOf(s.Config)
	if s.SourceType != "git" || !ok {
		invalid(w, r, "service_id", "deploy keys are only for git services")
		return
	}
	key, err := git.NewDeployKey("polipan-" + s.Name)
	if fail(w, r, err) {
		return
	}
	scope := git.Scope{OrgID: s.OrgID, ProjectID: s.ProjectID, ServiceID: s.ID}
	keyRef, err := h.Git.Keys.Put(ctx, scope, "deploy-key", key.PrivatePEM)
	if fail(w, r, err) {
		return
	}
	hookSecret, err := git.NewWebhookSecret()
	if fail(w, r, err) {
		return
	}
	hookRef, err := h.Git.Keys.Put(ctx, scope, "git-webhook", hookSecret)
	if fail(w, r, err) {
		return
	}
	provider := src.Provider
	if provider == "" {
		provider = git.Generic
	}
	hookID, oldHook, err := h.Store.PutServiceWebhook(ctx, s.ID, "git", provider, hookRef)
	if fail(w, r, err) {
		return
	}
	oldKey := src.DeployKeyRef
	src.DeployKeyRef, src.DeployKeyPublic = keyRef, key.Public
	cfg, err := setGitSource(s.Config, src)
	if fail(w, r, err) {
		return
	}
	if _, err := h.Store.UpdateService(ctx, h.orgID(r), s.ID, cfg, nil, nil); fail(w, r, err) {
		return
	}
	for _, ref := range []string{oldKey, oldHook} {
		if ref != "" {
			_ = h.Git.Keys.Delete(ctx, ref)
		}
	}
	WriteJSON(w, http.StatusCreated, map[string]string{
		"public_key": key.Public, "webhook_url": h.webhookURL(r, hookID), "webhook_secret": hookSecret,
	})
}

// setGitSource replaces config.git.
func setGitSource(config json.RawMessage, src *git.Source) (json.RawMessage, error) {
	m := map[string]any{}
	if err := json.Unmarshal(config, &m); err != nil {
		return nil, err
	}
	m["git"] = src
	return json.Marshal(m)
}

// ---- service config of git services ----

// gitSourceOf finds a git source in a create request: source {"type":"git", ...},
// source {"git": {...}} or config {"git": {...}}. ok is false for an image source.
func gitSourceOf(config, source map[string]any) (map[string]any, bool) {
	if g, ok := source["git"].(map[string]any); ok {
		return g, true
	}
	if t, _ := source["type"].(string); t == "git" {
		out := map[string]any{}
		for k, v := range source {
			if k != "type" {
				out[k] = v
			}
		}
		return out, true
	}
	if g, ok := config["git"].(map[string]any); ok {
		return g, true
	}
	return nil, false
}

// buildGitConfig checks a git source and the rest of the config. The rest follows the
// image config rules. auto_deploy on a protected environment needs an admin (REQ-APP-004).
func (h *Handlers) buildGitConfig(r *http.Request, protected bool, config, source map[string]any, current *git.Source) (json.RawMessage, error) {
	gitRaw, ok := gitSourceOf(config, source)
	if !ok {
		return nil, errors.New("config.git is required for a git service")
	}
	raw, _ := json.Marshal(gitRaw)
	var src git.Source
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&src); err != nil {
		return nil, errors.New("git source: " + err.Error())
	}
	// Only Polipan sets the deploy key fields.
	src.DeployKeyPublic, src.DeployKeyRef = "", ""
	if current != nil {
		src.DeployKeyPublic, src.DeployKeyRef = current.DeployKeyPublic, current.DeployKeyRef
	}
	if err := src.Normalize(); err != nil {
		return nil, err
	}
	ctx, orgID := r.Context(), h.orgID(r)
	if src.ConnectionID != "" {
		if _, err := h.Store.GetGitConnection(ctx, orgID, src.ConnectionID); err != nil {
			return nil, errors.New("git.connection_id is not a connection of this org")
		}
	} else if err := h.policy(ctx, orgID).CheckURL(ctx, src.Repo); err != nil {
		return nil, errors.New("git.repo is not allowed: " + err.Error())
	}
	if protected && src.AutoDeploy != nil && *src.AutoDeploy {
		if p := PrincipalFrom(ctx); p == nil || !auth.IsAdmin(p.Role) {
			return nil, errAdminAutoDeploy
		}
	}
	rest := map[string]any{}
	for k, v := range config {
		if k != "git" {
			rest[k] = v
		}
	}
	rest["image"] = "placeholder"
	checked, err := checkImageConfig(rest)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(checked, &out)
	delete(out, "image")
	out["git"] = src
	return json.Marshal(out)
}

var errAdminAutoDeploy = errors.New("only an admin can turn on auto_deploy on a protected environment")

// writeGitConfigError answers a bad git config: 403 for the admin rule, else 422.
func writeGitConfigError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errAdminAutoDeploy) {
		WriteError(w, r, http.StatusForbidden, CodeForbidden, err.Error(), map[string]any{"reason": "protected_environment"})
		return
	}
	invalid(w, r, "config", err.Error())
}

// deployContext adds the commit and no_cache of a deploy request of a git service.
func deployContext(ctx context.Context, sourceType string, body gen.DeployRequest) context.Context {
	if sourceType != "git" {
		return ctx
	}
	req := orchestrator.BuildRequest{}
	if body.Commit != nil {
		req.Commit = strings.TrimSpace(*body.Commit)
	}
	if body.NoCache != nil {
		req.NoCache = *body.NoCache
	}
	return orchestrator.WithBuildRequest(ctx, req)
}
