// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/build"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/registry"
	"github.com/polipan/polipan/server/internal/secrets"
)

// fakePusher "pushes" the build output: the digest is the hash of the tag (the commit).
type fakePusher struct{ host string }

func (p fakePusher) Push(_ context.Context, ociTar, repo, tag string) (string, string, error) {
	sum := sha256.Sum256([]byte(tag))
	d := "sha256:" + hex.EncodeToString(sum[:])
	return p.host + "/" + repo + "@" + d, d, nil
}

type gitFixture struct {
	env       *gatewaytest.Env
	ctl       *control.Control
	cloner    *git.FakeCloner
	builder   *build.Fake
	serviceID string
	projectID string
	agent     *gatewaytest.Agent

	mu    sync.Mutex
	pulls []*pb.PullImage
}

const pullHost = "registry.test:5000"

func gitSetup(t *testing.T, builder string) *gitFixture {
	t.Helper()
	env := gatewaytest.Start(t)
	kek := make([]byte, 32)
	sec, err := secrets.NewManager(env.Pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	f := &gitFixture{env: env,
		cloner:  &git.FakeCloner{Commit: "1111111aaaaaaa", Files: map[string]string{"Dockerfile": "FROM nginx:alpine\n"}},
		builder: &build.Fake{Lines: []string{"#1 building"}},
	}
	reg := registry.New(registry.Config{URL: "https://" + pullHost, PullHost: pullHost, Dir: t.TempDir()}, env.Pool, env.CA, kek)
	f.ctl = control.New(control.Deps{DB: env.Pool, Clock: env.Clock, Config: env.Cfg, CA: env.CA, Sender: env.GW, Secrets: sec,
		Build: &control.BuildDeps{Cloner: f.cloner, Builder: f.builder, Pusher: fakePusher{pullHost}, Registry: reg,
			Keys: &git.MemoryKeyStore{}, WorkDir: t.TempDir()}})
	t.Cleanup(f.ctl.Close)
	env.GW.State = f.ctl

	ctx := context.Background()
	prj, envID, svc := gateway.NewID("prj"), gateway.NewID("env"), gateway.NewID("svc")
	cfg := `{"port":8080,"strategy_options":{"drain_seconds":0},"git":{"repo":"https://git.example.com/acme/shop.git","branch":"main","path":"/","builder":"` + builder + `","dockerfile":"Dockerfile"}}`
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO projects (id, org_id, name, slug) VALUES ($1,$2,'p','p')`, []any{prj, env.OrgID}},
		{`INSERT INTO environments (id, project_id, name, slug, kind) VALUES ($1,$2,'staging','staging','staging')`, []any{envID, prj}},
		{`INSERT INTO services (id, environment_id, name, type, source_type, config) VALUES ($1,$2,'web','app','git',$3)`, []any{svc, envID, cfg}},
	} {
		if _, err := env.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	f.serviceID, f.projectID = svc, prj
	f.agent = env.NewReadyNode(t, "n1")
	// The fake agent pulls by digest: it answers with the same ref.
	f.agent.OnCommand = func(c *pb.Command) *pb.CommandAck {
		if p, ok := c.Body.(*pb.Command_PullImage); ok {
			f.mu.Lock()
			f.pulls = append(f.pulls, p.PullImage)
			f.mu.Unlock()
			ref := p.PullImage.ImageRef
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"` + ref + `","digest":"` + ref[strings.LastIndex(ref, "@")+1:] + `"}`)}
		}
		return &pb.CommandAck{Ok: true}
	}
	return f
}

func (f *gitFixture) deploy(t *testing.T, ctx context.Context, trigger string) (state, reason, detail string) {
	t.Helper()
	id, err := f.ctl.Deploy(ctx, f.serviceID, "", trigger)
	if err != nil {
		t.Fatal(err)
	}
	ff := &fixture{env: f.env, ctl: f.ctl, serviceID: f.serviceID}
	state, reason = ff.waitDeployment(t, id)
	var d *string
	_ = f.env.Pool.QueryRow(ctx, `SELECT failure_detail FROM deployments WHERE id = $1`, id).Scan(&d)
	if d != nil {
		detail = *d
	}
	return
}

// AC-APP-01: a public repo with a Dockerfile builds; the release has the image digest, the
// deployment is live and the workload runs that digest (pulled from the built-in registry with
// the node credential).
func TestAC_APP_01_GitDockerfileBuildRelease(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	ctx := context.Background()
	commit := "abcdef1234567"
	if st, reason, detail := f.deploy(t, orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{Commit: commit}), "manual"); st != "live" {
		t.Fatalf("deployment %s %s %s", st, reason, detail)
	}
	var ref, digest, srcType, srcCommit string
	if err := f.env.Pool.QueryRow(ctx, `SELECT image_ref, image_digest, source->>'type', source->>'commit_sha' FROM releases WHERE service_id = $1`,
		f.serviceID).Scan(&ref, &digest, &srcType, &srcCommit); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") || !strings.HasSuffix(ref, "@"+digest) || !strings.HasPrefix(ref, pullHost+"/") {
		t.Fatalf("release %s %s", ref, digest)
	}
	if srcType != "git" || srcCommit != commit {
		t.Fatalf("release source %s %s", srcType, srcCommit)
	}
	if len(f.cloner.Calls) != 1 || f.cloner.Calls[0].Commit != commit || f.builder.Calls[0].Builder != "dockerfile" {
		t.Fatalf("clone %v build %v", f.cloner.Calls, f.builder.Calls)
	}
	if len(f.pulls) != 1 || f.pulls[0].ImageRef != ref || f.pulls[0].RegistryAuthRef != registry.AuthRef {
		t.Fatalf("pull %v", f.pulls)
	}
	ds := lastState(t, f.agent)
	if len(ds.Workloads) != 1 || ds.Workloads[0].ImageRef != ref || ds.Workloads[0].RegistryAuthRef != registry.AuthRef {
		t.Fatalf("workloads %v", ds.Workloads)
	}
	if len(ds.RegistryAuths) != 1 || ds.RegistryAuths[0].Username != registry.NodeUser(f.agent.NodeID) || ds.RegistryAuths[0].Password == "" ||
		ds.RegistryAuths[0].Server != pullHost {
		t.Fatalf("registry auths %v", ds.RegistryAuths)
	}

	// The same commit again: a new release row (the cache may be reused).
	if st, _, _ := f.deploy(t, orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{Commit: commit}), "git_push"); st != "live" {
		t.Fatal(st)
	}
	var n int
	_ = f.env.Pool.QueryRow(ctx, `SELECT count(*) FROM releases WHERE service_id = $1 AND image_digest = $2`, f.serviceID, digest).Scan(&n)
	if n != 2 {
		t.Fatalf("releases with the same digest: %d", n)
	}
}

// AC-APP-02: an image service with tag latest; the tag moves; a new deploy gives a release with
// the new digest and the old release keeps its digest.
func TestAC_APP_02_MovingTagNewDigest(t *testing.T) {
	f := setup(t) // image service nginx:1.27
	ctx := context.Background()
	a := f.env.NewReadyNode(t, "n1")
	if _, err := f.env.Pool.Exec(ctx, `UPDATE services SET config = '{"image":"nginx:latest","port":8080,"strategy_options":{"drain_seconds":0}}' WHERE id = $1`, f.serviceID); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("1", 64)
	a.OnCommand = func(c *pb.Command) *pb.CommandAck {
		if _, ok := c.Body.(*pb.Command_PullImage); ok {
			return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"image_ref":"nginx@` + digest + `","digest":"` + digest + `"}`)}
		}
		return &pb.CommandAck{Ok: true}
	}
	for i, want := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		digest = "sha256:" + want
		id, err := f.ctl.Deploy(ctx, f.serviceID, "", "manual")
		if err != nil {
			t.Fatal(err)
		}
		if st, reason := f.waitDeployment(t, id); st != "live" {
			t.Fatalf("deploy %d: %s %s", i, st, reason)
		}
	}
	rows, err := f.env.Pool.Query(ctx, `SELECT number, image_digest, image_ref FROM releases WHERE service_id = $1 ORDER BY number`, f.serviceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n int
		var d, ref string
		if err := rows.Scan(&n, &d, &ref); err != nil {
			t.Fatal(err)
		}
		if ref != "nginx@"+d {
			t.Fatalf("release %d ref %s", n, ref)
		}
		got = append(got, d)
	}
	if len(got) != 2 || got[0] != "sha256:"+strings.Repeat("1", 64) || got[1] != "sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("digests %v", got)
	}
}

// AC-APP-04: no Dockerfile and builder railpack: railpack builds and the app runs. A detection
// failure gives build_failed with the detector message, and the live release stays.
// A deleted repo (clone failure) also gives build_failed and leaves the live release alone.
func TestAC_APP_04_RailpackAndDetectionFailure(t *testing.T) {
	f := gitSetup(t, "railpack")
	f.cloner.Files = map[string]string{"package.json": `{"scripts":{"start":"node index.js"}}`, "index.js": "1"}
	ctx := context.Background()
	if st, reason, detail := f.deploy(t, ctx, "manual"); st != "live" {
		t.Fatalf("railpack deploy %s %s %s", st, reason, detail)
	}
	if b := f.builder.Calls[0]; b.Builder != build.Railpack || b.CacheKey == "" {
		t.Fatalf("build request %+v", b)
	}
	var live string
	_ = f.env.Pool.QueryRow(ctx, `SELECT live_release_id FROM services WHERE id = $1`, f.serviceID).Scan(&live)

	f.builder.DetectErr = "No start command could be found"
	st, reason, detail := f.deploy(t, ctx, "manual")
	if st != "failed" || reason != control.ReasonBuildFailed || !strings.Contains(detail, "No start command") {
		t.Fatalf("detection failure: %s %s %s", st, reason, detail)
	}
	f.builder.DetectErr = ""
	f.cloner.Err = &git.CloneError{Msg: "repository or branch not found"}
	if st, reason, detail = f.deploy(t, ctx, "manual"); st != "failed" || reason != control.ReasonBuildFailed || !strings.Contains(detail, "not found") {
		t.Fatalf("clone failure: %s %s %s", st, reason, detail)
	}
	var after string
	_ = f.env.Pool.QueryRow(ctx, `SELECT live_release_id FROM services WHERE id = $1`, f.serviceID).Scan(&after)
	if after != live || live == "" {
		t.Fatalf("live release changed: %s -> %s", live, after)
	}
	f.cloner.Err = nil

	// The dockerfile builder needs a Dockerfile.
	if _, err := f.env.Pool.Exec(ctx, `UPDATE services SET config = jsonb_set(config, '{git,builder}', '"dockerfile"') WHERE id = $1`, f.serviceID); err != nil {
		t.Fatal(err)
	}
	if st, reason, _ = f.deploy(t, ctx, "manual"); st != "failed" || reason != control.ReasonBuildFailed {
		t.Fatalf("no Dockerfile: %s %s", st, reason)
	}
}

// AC-APP-06: a build log line with a secret value of the service is stored with ***.
func TestAC_APP_06_BuildLogMasked(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	ctx := context.Background()
	secretID := gateway.NewID("sec")
	if _, err := f.env.Pool.Exec(ctx, `INSERT INTO secrets (id, project_id, service_id, name, build_time) VALUES ($1,$2,$3,'API_TOKEN',true)`,
		secretID, f.projectID, f.serviceID); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.Secrets.Provider.Put(ctx, f.env.Pool, f.projectID, secretID, 1, []byte("tok-s3cr3t-value"), ""); err != nil {
		t.Fatal(err)
	}
	f.builder.Lines = []string{"#5 RUN echo token=tok-s3cr3t-value", "#6 done"}
	if st, reason, detail := f.deploy(t, ctx, "manual"); st != "live" {
		t.Fatalf("deploy %s %s %s", st, reason, detail)
	}
	// The build got the secret as a BuildKit secret (not a build arg).
	if b := f.builder.Calls[0]; b.Secrets["API_TOKEN"] != "tok-s3cr3t-value" || len(b.BuildArgs) != 0 {
		t.Fatalf("build secrets %v args %v", b.Secrets, b.BuildArgs)
	}
	var leaked, masked int
	_ = f.env.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE line LIKE '%tok-s3cr3t-value%'), count(*) FILTER (WHERE line = '#5 RUN echo token=***')
		FROM deployment_logs WHERE stream = 'build'`).Scan(&leaked, &masked)
	if leaked != 0 || masked != 1 {
		t.Fatalf("leaked %d masked %d", leaked, masked)
	}
}

// Builds that are not set up fail with build_failed, never with a panic.
func TestAC_APP_01_NoBuilderFails(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	f.ctl.Build.Builder = nil
	if st, reason, _ := f.deploy(t, context.Background(), "manual"); st != "failed" || reason != control.ReasonBuildFailed {
		t.Fatalf("%s %s", st, reason)
	}
}
