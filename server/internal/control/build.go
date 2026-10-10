// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/build"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/mask"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/registry"
	"github.com/polipan/polipan/server/internal/ssrf"
	"github.com/polipan/polipan/server/internal/store"
	"github.com/polipan/polipan/server/internal/vars"
)

// Failure reasons of a build (08-deploy-engine).
const (
	ReasonBuildFailed  = "build_failed"
	ReasonBuildTimeout = "build_timeout"
)

// defaultBuildTimeout is the glossary default of build_timeout.
const defaultBuildTimeout = 30 * time.Minute

// BuildDeps are the parts of git builds (WP2.2). Without them a git service cannot deploy.
type BuildDeps struct {
	Cloner  git.Cloner
	Builder build.Builder
	Pusher  registry.Pusher
	// Registry gives the per-node pull credentials. Nil in tests.
	Registry *registry.Registry
	// Keys holds Git tokens, app keys and deploy keys.
	Keys git.KeyStore
	// NewProvider makes the provider of a connection. Nil means git.NewProvider with an SSRF-checked client.
	NewProvider func(c git.Connection, p ssrf.Policy) (git.Provider, error)
	// WorkDir is the folder for checkouts and build outputs. Empty means the temp folder.
	WorkDir string
}

// saveBuildRequest stores the build request of ctx (orchestrator.WithBuildRequest) in the transaction of Deploy.
func saveBuildRequest(ctx context.Context, tx pgx.Tx, depID string) error {
	r, ok := orchestrator.BuildRequestFrom(ctx)
	if !ok {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO deployment_builds (deployment_id, commit_sha, branch, no_cache) VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4)`,
		depID, r.Commit, r.Branch, r.NoCache)
	return err
}

// SSRFPolicy is the deny list of an org (REQ-SEC-104): mesh CIDR and Docker networks always,
// private ranges unless the org setting allow_private_targets is on.
func (c *Control) SSRFPolicy(ctx context.Context, orgID string) ssrf.Policy {
	var allow bool
	_ = c.DB.QueryRow(ctx, `SELECT COALESCE((settings->>'allow_private_targets')::boolean, false) FROM orgs WHERE id = $1`, orgID).Scan(&allow)
	p, err := ssrf.New(allow, c.Config.MeshCIDR, ssrf.DefaultDockerNetworks)
	if err != nil {
		p, _ = ssrf.New(allow, "", ssrf.DefaultDockerNetworks)
	}
	return p
}

// buildIfGit runs the build step of a git service: clone the commit, build, push, and return the
// image ref by digest. For an image service it returns "". A failure leaves the live release alone.
func (c *Control) buildIfGit(ctx context.Context, depID, serviceID string) (string, *deployErr) {
	fail := func(reason, format string, a ...any) (string, *deployErr) {
		return "", &deployErr{reason: reason, detail: fmt.Sprintf(format, a...)}
	}
	var sourceType, orgID, projectID string
	var raw []byte
	var timeoutMin *int
	err := c.DB.QueryRow(ctx, `SELECT s.source_type, s.config, p.org_id, p.id, NULLIF(p.settings->>'build_timeout_minutes','')::int
		FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id
		WHERE s.id = $1`, serviceID).Scan(&sourceType, &raw, &orgID, &projectID, &timeoutMin)
	if err != nil {
		return fail(ReasonInvalidConfig, "service not found: %v", err)
	}
	if sourceType != "git" {
		return "", nil
	}
	src, ok := git.SourceOf(raw)
	if !ok {
		return fail(ReasonInvalidConfig, "the service config has no git source")
	}
	b := c.Build
	if b == nil || b.Cloner == nil || b.Builder == nil || b.Pusher == nil {
		return fail(ReasonBuildFailed, "builds are not set up on this server (BuildKit and registry)")
	}
	var req orchestrator.BuildRequest
	_ = c.DB.QueryRow(ctx, `SELECT COALESCE(commit_sha,''), COALESCE(branch,''), no_cache FROM deployment_builds WHERE deployment_id = $1`, depID).
		Scan(&req.Commit, &req.Branch, &req.NoCache)
	timeout := defaultBuildTimeout
	if timeoutMin != nil && *timeoutMin > 0 {
		timeout = time.Duration(*timeoutMin) * time.Minute
	}

	// Mask: the secret values of the service, the build secrets and the clone password.
	var maskValues []string
	if c.Secrets != nil {
		v, err := c.Secrets.ValuesForService(ctx, serviceID)
		if err != nil {
			c.Log.Warn("build: secret values for the mask", "service", serviceID, "err", err)
		}
		maskValues = append(maskValues, v...)
	}
	buildSecrets, buildArgs, err := c.buildTimeValues(ctx, serviceID, projectID)
	if err != nil {
		return fail(ReasonBuildFailed, "read build-time variables: %v", err)
	}
	for _, v := range buildSecrets {
		maskValues = append(maskValues, v)
	}

	// Where to clone from.
	policy := c.SSRFPolicy(ctx, orgID)
	clone := git.CloneRequest{URL: src.Repo, Branch: src.Branch, Commit: req.Commit, Policy: policy}
	if src.ConnectionID != "" {
		prov, err := c.providerOf(ctx, orgID, src.ConnectionID, policy)
		if err != nil {
			return fail(ReasonBuildFailed, "git connection: %v", err)
		}
		clone.URL = prov.CloneURL(src.Repo)
		if clone.Username, clone.Password, err = prov.CloneAuth(ctx); err != nil {
			return fail(ReasonBuildFailed, "git connection: the token was refused or revoked: %v", err)
		}
	}
	if src.DeployKeyRef != "" && b.Keys != nil && !strings.HasPrefix(clone.URL, "http") {
		if clone.SSHKey, err = b.Keys.Get(ctx, src.DeployKeyRef); err != nil {
			return fail(ReasonBuildFailed, "deploy key: %v", err)
		}
	}
	if clone.Password != "" {
		maskValues = append(maskValues, clone.Password)
	}
	m := mask.New(maskValues)
	logLine := func(line string) { c.buildLog(ctx, depID, m.Mask(line)) }
	clone.Log = logLine

	work, err := os.MkdirTemp(b.WorkDir, "pp-build-")
	if err != nil {
		return fail(ReasonBuildFailed, "work folder: %v", err)
	}
	defer os.RemoveAll(work)
	srcDir := filepath.Join(work, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		return fail(ReasonBuildFailed, "work folder: %v", err)
	}
	clone.Dir = srcDir
	logLine(fmt.Sprintf("cloning %s (%s)", git.RepoName(src.Repo), firstNonEmpty(req.Commit, src.Branch)))
	cctx, cancel := context.WithTimeout(ctx, timeout)
	commit, err := b.Cloner.Clone(cctx, clone)
	cancel()
	if err != nil {
		return fail(ReasonBuildFailed, "%s", m.Mask(err.Error()))
	}
	logLine("commit " + commit)

	// Build.
	cacheKey := strings.ToLower(serviceID)
	out := filepath.Join(work, "image.tar")
	contextDir, err := build.ResolveInside(srcDir, src.Path)
	if err != nil {
		return fail(ReasonBuildFailed, "build path: %v", err)
	}
	breq := build.Request{
		ContextDir: contextDir,
		Builder:    src.Builder, Dockerfile: src.Dockerfile,
		Secrets: buildSecrets, BuildArgs: buildArgs,
		CacheKey: cacheKey, NoCache: req.NoCache, Timeout: timeout, Output: out, Log: logLine,
	}
	logLine("building with " + src.Builder)
	if err := b.Builder.Build(ctx, breq); err != nil {
		var de *build.DetectError
		switch {
		case errors.Is(err, build.ErrTimeout):
			return fail(ReasonBuildTimeout, "the build ran longer than %s", timeout)
		case errors.As(err, &de):
			return fail(ReasonBuildFailed, "%s", m.Mask(de.Msg))
		default:
			return fail(ReasonBuildFailed, "%s", m.Mask(err.Error()))
		}
	}

	// Push: done by the server, never by build steps (REQ-SEC-108).
	tag := commit
	if len(tag) > 12 {
		tag = tag[:12]
	}
	ref, digest, err := b.Pusher.Push(ctx, out, registry.RepoName(serviceID), tag)
	if err != nil {
		return fail(ReasonBuildFailed, "push to the registry: %v", err)
	}
	logLine("pushed " + ref)
	now := c.Clock.Now()
	if _, err := c.DB.Exec(ctx, `INSERT INTO deployment_builds (deployment_id, commit_sha, branch, no_cache, builder, image_ref, image_digest, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (deployment_id) DO UPDATE SET commit_sha = $2, branch = $3, builder = $5, image_ref = $6, image_digest = $7, finished_at = $8`,
		depID, commit, src.Branch, req.NoCache, src.Builder, ref, digest, now); err != nil {
		return fail(ReasonBuildFailed, "save build: %v", err)
	}
	var cleared *time.Time
	if req.NoCache {
		cleared = &now
	}
	_, _ = c.DB.Exec(ctx, `INSERT INTO build_caches (service_id, cache_key, last_build_at, cleared_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (service_id) DO UPDATE SET last_build_at = $3, cleared_at = COALESCE($4, build_caches.cleared_at)`,
		serviceID, cacheKey, now, cleared)
	return ref, nil
}

// buildTimeValues returns the build-time secrets (values) and plain build-time variables of a service.
func (c *Control) buildTimeValues(ctx context.Context, serviceID, projectID string) (secretValues, plain map[string]string, err error) {
	secretValues, plain = map[string]string{}, map[string]string{}
	scope, err := vars.LoadScope(ctx, c.DB, serviceID)
	if err != nil {
		return nil, nil, err
	}
	entries, err := vars.LoadEntries(ctx, c.DB, projectID)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range vars.Effective(entries, scope.EnvironmentID, serviceID, scope.Preview) {
		if !e.BuildTime {
			continue
		}
		if !e.Secret {
			plain[e.Name] = e.Value
			continue
		}
		if c.Secrets == nil {
			continue
		}
		v, err := c.Secrets.Provider.Get(ctx, c.DB, projectID, e.ID, e.Version)
		if err != nil {
			return nil, nil, fmt.Errorf("secret %s: %w", e.Name, err)
		}
		secretValues[e.Name] = string(v)
	}
	return secretValues, plain, nil
}

func (c *Control) providerOf(ctx context.Context, orgID, connID string, policy ssrf.Policy) (git.Provider, error) {
	var provider string
	var baseURL *string
	var authRaw []byte
	err := c.DB.QueryRow(ctx, `SELECT provider, base_url, auth FROM git_connections WHERE id = $1 AND org_id = $2`, connID, orgID).
		Scan(&provider, &baseURL, &authRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("the connection was deleted")
	}
	if err != nil {
		return nil, err
	}
	var auth struct {
		AppID          string `json:"app_id"`
		InstallationID string `json:"installation_id"`
		SecretRef      string `json:"secret_ref"`
	}
	_ = json.Unmarshal(authRaw, &auth)
	conn := git.Connection{Provider: provider, AppID: auth.AppID, InstallationID: auth.InstallationID}
	if baseURL != nil {
		conn.BaseURL = *baseURL
	}
	if auth.SecretRef != "" && c.Build.Keys != nil {
		v, err := c.Build.Keys.Get(ctx, auth.SecretRef)
		if err != nil {
			return nil, err
		}
		if provider == git.GitHub {
			conn.PrivateKey = v
		} else {
			conn.Token = v
		}
	}
	if c.Build.NewProvider != nil {
		return c.Build.NewProvider(conn, policy)
	}
	return git.NewProvider(conn, policy.Client(30*time.Second))
}

// buildLog stores one masked build log line (stream build). Live readers poll deployment_logs.
func (c *Control) buildLog(ctx context.Context, depID, line string) {
	if len(line) > 64*1024 {
		line = line[:64*1024]
	}
	if _, err := c.DB.Exec(ctx, `INSERT INTO deployment_logs (deployment_id, seq, stream, line)
		SELECT $1, COALESCE(MAX(seq),0)+1, 'build', $2 FROM deployment_logs WHERE deployment_id = $1`, depID, line); err != nil {
		c.Log.Warn("build log", "deployment", depID, "err", err)
	}
}

// releaseSource is releases.source: git commit and branch for a built image, else image.
func (c *Control) releaseSource(ctx context.Context, depID, actorID string) map[string]any {
	src := map[string]any{"type": "image", "actor_id": actorID}
	var commit, branch string
	err := c.DB.QueryRow(ctx, `SELECT COALESCE(commit_sha,''), COALESCE(branch,'') FROM deployment_builds
		WHERE deployment_id = $1 AND image_digest IS NOT NULL`, depID).Scan(&commit, &branch)
	if err == nil {
		src["type"], src["commit_sha"], src["branch"] = "git", commit, branch
	}
	return src
}

// registryAuthRef is the registry_auth_ref of an image: the built-in registry or none.
func (c *Control) registryAuthRef(imageRef string) string {
	if c.Build != nil && c.Build.Registry.IsOwnImage(imageRef) {
		return registry.AuthRef
	}
	return ""
}

// addRegistryAuth gives a node its own pull credential of the built-in registry.
func (c *Control) addRegistryAuth(ctx context.Context, nodeID string, ds *pb.DesiredState) {
	if c.Build == nil || !c.Build.Registry.Enabled() {
		return
	}
	if err := c.Build.Registry.EnsureNode(ctx, nodeID); err != nil {
		c.Log.Warn("registry: node credential", "node", nodeID, "err", err)
	}
	ds.RegistryAuths = append(ds.RegistryAuths, c.Build.Registry.Auth(nodeID))
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// SetupBuilds makes the build parts from the environment: the Git key store, the built-in
// registry (POLIPAN_REGISTRY_*), BuildKit (POLIPAN_BUILDKIT_*) and the git command line.
// It writes the registry and BuildKit certificates. A missing part is logged: git services
// then fail to deploy with a clear message, image services keep working.
func SetupBuilds(ctx context.Context, pool *pgxpool.Pool, ca *pki.CA, kek []byte, getenv func(string) string, log *slog.Logger) *BuildDeps {
	d := &BuildDeps{Cloner: git.CLI{}, Keys: store.NewGitSecrets(pool, kek), WorkDir: getenv("POLIPAN_BUILD_DIR")}
	reg := registry.New(registry.ConfigFromEnv(getenv), pool, ca, kek)
	if reg.Enabled() {
		if err := reg.Setup(ctx); err != nil {
			log.Error("registry setup", "err", err)
		} else {
			d.Registry, d.Pusher = reg, reg
		}
	} else {
		log.Warn("no built-in registry (POLIPAN_REGISTRY_URL): git services cannot deploy")
	}
	if bk := build.FromEnv(getenv); bk != nil {
		if err := build.WriteCerts(bk.CertDir, ca, []string{getenv("POLIPAN_BUILDKIT_HOST")}); err != nil {
			log.Error("buildkit certificates", "err", err)
		} else {
			d.Builder = bk
		}
	} else {
		log.Warn("no BuildKit (POLIPAN_BUILDKIT_ADDR): git services cannot deploy")
	}
	return d
}
