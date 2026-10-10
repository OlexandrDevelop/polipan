// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// m2Engine is an engine with the mount folders in temp folders.
func m2Engine(t *testing.T) (*Engine, *fakeDocker, *sink) {
	t.Helper()
	f := newFake()
	f.volRoot = t.TempDir()
	e, sk := newEngine(t, t.TempDir(), f)
	e.BindsDir, e.FilesDir, e.SecretsDir = t.TempDir(), t.TempDir(), t.TempDir()
	e.CheckTmpfs = func(string) error { return nil }
	e.Quota = noQuota{}
	return e, f, sk
}

type noQuota struct{}

func (noQuota) Apply(string, uint64) (bool, error) { return false, nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func mountOfSpec(sp docker.ContainerSpec, target string) *docker.Mount {
	for i := range sp.Mounts {
		if sp.Mounts[i].Target == target {
			return &sp.Mounts[i]
		}
	}
	return nil
}

// AC-VOL-01: a volume with data is kept over a redeploy and the new container mounts it.
// It is removed only with RemoveWorkload.remove_volumes.
func TestAC_VOL_01_DataSurvivesRedeploy(t *testing.T) {
	e, f, _ := m2Engine(t)
	w := wl("a", "live")
	w.Mounts = []*agentv1.Mount{{Kind: "volume", Source: "pv-data", Target: "/data"}}
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(context.Background())
	m := mountOfSpec(f.specs["pp-a"], "/data")
	if m == nil || m.Type != "volume" || m.Source != "pv-data" {
		t.Fatalf("mounts: %+v", f.specs["pp-a"].Mounts)
	}
	if f.vols["pv-data"].Labels[LabelService] != "svc_1" {
		t.Fatalf("volume labels: %v", f.vols["pv-data"].Labels)
	}
	data := filepath.Join(f.vols["pv-data"].Mountpoint, "upload.txt")
	if err := os.WriteFile(data, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldID := f.cs["pp-a"].ID

	// Redeploy: a new release recreates the container.
	w2 := wl("a", "live")
	w2.ReleaseId = "rel_2"
	w2.Mounts = w.Mounts
	e.SetDesired(&agentv1.DesiredState{Version: 2, Workloads: []*agentv1.WorkloadSpec{w2}})
	e.Reconcile(context.Background())
	if f.cs["pp-a"].ID == oldID || f.cs["pp-a"].Labels[LabelRelease] != "rel_2" {
		t.Fatal("the container was not recreated")
	}
	if m := mountOfSpec(f.specs["pp-a"], "/data"); m == nil || m.Source != "pv-data" {
		t.Fatalf("new container mounts: %+v", f.specs["pp-a"].Mounts)
	}
	if b, err := os.ReadFile(data); err != nil || string(b) != "kept" || f.has("volume rm") {
		t.Fatalf("data lost: %q %v calls %v", b, err, f.calls)
	}

	// RemoveWorkload keeps the volume, unless remove_volumes is set.
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_rm1", Body: &agentv1.Command_RemoveWorkload{
		RemoveWorkload: &agentv1.RemoveWorkload{PlacementId: "a"}}})
	if _, ok := f.vols["pv-data"]; !ok || f.has("volume rm") {
		t.Fatal("volume removed without remove_volumes")
	}
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_rm2", Body: &agentv1.Command_RemoveWorkload{
		RemoveWorkload: &agentv1.RemoveWorkload{PlacementId: "a", RemoveVolumes: true}}})
	if _, ok := f.vols["pv-data"]; ok || !f.has("volume rm pv-data") {
		t.Fatalf("volume not removed: %v", f.calls)
	}
}

// AC-VOL-01 (REQ-VOL-001, REQ-TPL-005): binds only under <binds>/<service>, no "..", no symlink, no Docker socket.
func TestAC_VOL_01_BindPathRules(t *testing.T) {
	e, f, _ := m2Engine(t)
	root := filepath.Join(e.BindsDir, "svc_1")
	got, err := e.bindPath("svc_1", "data/uploads", true)
	if err != nil || got != filepath.Join(root, "data", "uploads") {
		t.Fatalf("good bind: %q %v", got, err)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatal("bind folder was not created")
	}
	if got, err := e.bindPath("svc_1", root+"/data", true); err != nil || got != filepath.Join(root, "data") {
		t.Fatalf("absolute bind inside: %q %v", got, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/var/run", filepath.Join(root, "run")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ service, sub string }{
		{"svc_1", "../svc_2"},
		{"svc_1", "data/../../svc_2"},
		{"svc_1", "/etc"},
		{"svc_1", "/var/run/docker.sock"},
		{"svc_1", e.BindsDir + "/svc_2/x"},
		{"svc_1", "link"},
		{"svc_1", "link/sub"},
		{"svc_1", "run/docker.sock"},
		{"../etc", "x"},
		{"", "x"},
	} {
		if _, err := e.bindPath(bad.service, bad.sub, true); err == nil {
			t.Errorf("bind %q of %q was allowed", bad.sub, bad.service)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("a folder was made through the symlink")
	}

	// A refused mount stops the container from being made; the error has a code.
	w := wl("a", "live")
	w.Mounts = []*agentv1.Mount{{Kind: "bind", Source: "../../etc", Target: "/host"}}
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(context.Background())
	if f.has("create pp-a") {
		t.Fatal("container with a bad bind was created")
	}
	for _, tgt := range []string{"relative", "/a/../b", "/"} {
		if _, err := cleanTarget(tgt); err == nil {
			t.Errorf("target %q was allowed", tgt)
		}
	}
	// A good bind and a tmpfs with a size.
	w.Mounts = []*agentv1.Mount{{Kind: "bind", Source: "data", Target: "/data", ReadOnly: true},
		{Kind: "tmpfs", Target: "/cache", SizeLimitBytes: 1 << 20}}
	e.SetDesired(&agentv1.DesiredState{Version: 2, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(context.Background())
	sp := f.specs["pp-a"]
	if m := mountOfSpec(sp, "/data"); m == nil || m.Type != "bind" || m.Source != filepath.Join(root, "data") || !m.ReadOnly {
		t.Fatalf("bind mount: %+v", sp.Mounts)
	}
	if m := mountOfSpec(sp, "/cache"); m == nil || m.Type != "tmpfs" || m.TmpfsSize != 1<<20 {
		t.Fatalf("tmpfs mount: %+v", sp.Mounts)
	}
}

// AC-VOL-01 (REQ-VOL-006): a file mount is written read-only, up to 1 MiB. A secret file goes to
// tmpfs with mode 0400 and is removed with the workload.
func TestAC_VOL_01_FileAndSecretFileMounts(t *testing.T) {
	e, f, _ := m2Engine(t)
	w := wl("a", "live")
	w.Mounts = []*agentv1.Mount{
		{Kind: "file", Source: "app.conf", Target: "/etc/app.conf", Content: []byte("port=80")},
		{Kind: "file", Source: "key.pem", Target: "/run/key.pem", Content: []byte("PRIVATE-KEY-TEXT"), Secret: true},
	}
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(context.Background())
	sp := f.specs["pp-a"]
	conf, key := mountOfSpec(sp, "/etc/app.conf"), mountOfSpec(sp, "/run/key.pem")
	if conf == nil || key == nil || !conf.ReadOnly || !key.ReadOnly || conf.Type != "bind" {
		t.Fatalf("mounts: %+v", sp.Mounts)
	}
	if !strings.HasPrefix(conf.Source, e.FilesDir) || !strings.HasPrefix(key.Source, filepath.Join(e.SecretsDir, "a")) {
		t.Fatalf("sources: %s %s", conf.Source, key.Source)
	}
	if b, _ := os.ReadFile(conf.Source); string(b) != "port=80" {
		t.Fatalf("file content %q", b)
	}
	if fi, _ := os.Stat(conf.Source); fi.Mode().Perm() != 0o444 {
		t.Fatalf("file mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(key.Source); fi.Mode().Perm() != 0o400 {
		t.Fatalf("secret file mode %v", fi.Mode())
	}
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_rm", Body: &agentv1.Command_RemoveWorkload{
		RemoveWorkload: &agentv1.RemoveWorkload{PlacementId: "a"}}})
	if _, err := os.Stat(filepath.Join(e.SecretsDir, "a")); !os.IsNotExist(err) {
		t.Fatal("secret files must be removed with the workload")
	}

	// Too large, a bad name and a secret folder that is not tmpfs are refused.
	for _, m := range []*agentv1.Mount{
		{Kind: "file", Source: "big", Target: "/big", Content: make([]byte, maxFileMount+1)},
		{Kind: "file", Source: "../x", Target: "/x", Content: []byte("x")},
	} {
		if _, err := e.writeFile("b", 0, m); err == nil {
			t.Errorf("file %q was allowed", m.Source)
		}
	}
	e.CheckTmpfs = func(string) error { return errors.New("not tmpfs") }
	if _, err := e.writeFile("b", 0, &agentv1.Mount{Kind: "file", Source: "k", Target: "/k", Content: []byte("s"), Secret: true}); err == nil {
		t.Fatal("secret file outside tmpfs was written")
	}
}

// AC-VOL-01 (REQ-VOL-005): bytes and inodes of every volume and bind are in ActualState.volumes,
// measured every 60 s by the injected clock.
func TestAC_VOL_01_UsageReport(t *testing.T) {
	e, f, sk := m2Engine(t)
	now := time.Unix(1_700_000_000, 0)
	e.Now = func() time.Time { return now }
	w := wl("a", "live")
	w.Mounts = []*agentv1.Mount{{Kind: "volume", Source: "pv-data", Target: "/data"}, {Kind: "bind", Source: "logs", Target: "/logs"}}
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w}})
	e.Reconcile(context.Background()) // creates the volume, first sample of empty folders
	dir := f.vols["pv-data"].Mountpoint
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), make([]byte, 10_000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vol := func() *agentv1.VolumeUsage {
		for _, v := range sk.last().GetVolumes() {
			if v.Name == "pv-data" {
				return v
			}
		}
		t.Fatalf("no usage of pv-data: %v", sk.last().GetVolumes())
		return nil
	}
	now = now.Add(30 * time.Second)
	e.Reconcile(context.Background())
	if v := vol(); v.UsedInodes != 1 || v.SampledUnixMs != 1_700_000_000_000 {
		t.Fatalf("before 60 s the old sample must be sent: %+v", v)
	}
	now = now.Add(30 * time.Second)
	e.Reconcile(context.Background())
	v := vol()
	if v.Kind != "volume" || v.UsedInodes != 4 || v.UsedBytes < 30_000 || v.SampledUnixMs != now.UnixMilli() {
		t.Fatalf("usage: %+v", v)
	}
	var bind *agentv1.VolumeUsage
	for _, u := range sk.last().GetVolumes() {
		if u.Kind == "bind" {
			bind = u
		}
	}
	if bind == nil || bind.Name != filepath.Join(e.BindsDir, "svc_1", "logs") || bind.UsedInodes != 1 {
		t.Fatalf("bind usage: %+v", bind)
	}
}

// AC-SEC-03 (agent side): secret values never reach a log or an error that the agent sends.
// Registry credentials go only to the pull (X-Registry-Auth).
func TestAC_SEC_03_AgentSecretsNotLogged(t *testing.T) {
	e, f, sk := m2Engine(t)
	var logs bytes.Buffer
	e.Log = slog.New(slog.NewTextHandler(&logs, nil))
	w := wl("a", "live")
	w.Env = map[string]string{"DB_PASSWORD": "hunter2-secret", "PLAIN": "x"}
	w.SecretEnvKeys = []string{"DB_PASSWORD"}
	w.RegistryAuthRef = "reg_1"
	ds := &agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{w},
		RegistryAuths: []*agentv1.RegistryAuth{{Ref: "reg_1", Server: "ghcr.io", Username: "bot", Password: "reg-pass-value"}}}
	f.createErr = errors.New("docker: bad env DB_PASSWORD=hunter2-secret auth reg-pass-value")
	e.SetDesired(ds)
	e.Reconcile(context.Background())

	if len(f.auths) != 1 {
		t.Fatalf("pulls: %v", f.calls)
	}
	raw, _ := base64.URLEncoding.DecodeString(f.auths[0])
	var a map[string]string
	_ = json.Unmarshal(raw, &a)
	if a["username"] != "bot" || a["password"] != "reg-pass-value" || a["serveraddress"] != "ghcr.io" {
		t.Fatalf("registry auth: %v", a)
	}
	errs := strings.Join(sk.last().GetErrors(), "\n")
	if !strings.Contains(errs, "***") {
		t.Fatalf("errors: %s", errs)
	}
	for _, secret := range []string{"hunter2-secret", "reg-pass-value"} {
		if strings.Contains(errs, secret) || strings.Contains(logs.String(), secret) {
			t.Fatalf("secret %q leaked: %s\n%s", secret, errs, logs.String())
		}
	}

	// A pull with an unknown credentials ref fails with image_pull_failed.
	e.HandleCommand(context.Background(), &agentv1.Command{CommandId: "cmd_p", Body: &agentv1.Command_PullImage{
		PullImage: &agentv1.PullImage{ImageRef: "ghcr.io/x/y:1", RegistryAuthRef: "reg_missing"}}})
	if ack := sk.find(func(m *agentv1.AgentMessage) bool { return m.GetAck().GetCommandId() == "cmd_p" }).GetAck(); ack.ErrorCode != "image_pull_failed" {
		t.Fatalf("ack: %+v", ack)
	}
}

// AC-APP-07 (REQ-APP-010, agent side): an exec session streams output as StreamChunk, takes input and
// resizes in order, ends with eof and an ack, and is closed when the container stops.
func TestAC_APP_07_AgentExecSession(t *testing.T) {
	e, f, sk := m2Engine(t)
	e.SetDesired(&agentv1.DesiredState{Version: 1, Workloads: []*agentv1.WorkloadSpec{wl("a", "live")}})
	e.Reconcile(context.Background())
	ctx := context.Background()
	start := func(id string) *fakeExec {
		go e.HandleCommand(ctx, &agentv1.Command{CommandId: id, Body: &agentv1.Command_ExecSession{
			ExecSession: &agentv1.ExecSession{PlacementId: "a", Tty: true, Rows: 24, Cols: 80}}})
		n := len(f.execs)
		waitFor(t, "exec started", func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.execs) > n
		})
		return f.lastExec()
	}
	input := func(id string, in *agentv1.ExecInput) {
		in.SessionCommandId = id
		e.HandleCommand(ctx, &agentv1.Command{CommandId: "cmd_in", Body: &agentv1.Command_ExecInput{ExecInput: in}})
	}
	chunk := func(id string, eof bool, data string) bool {
		return sk.find(func(m *agentv1.AgentMessage) bool {
			c := m.GetChunk()
			return c != nil && c.CommandId == id && c.Eof == eof && (data == "" || string(c.Data) == data)
		}) != nil
	}
	ack := func(id string) *agentv1.CommandAck {
		return sk.find(func(m *agentv1.AgentMessage) bool { return m.GetAck().GetCommandId() == id }).GetAck()
	}

	x := start("cmd_s1")
	if !x.opts.Tty || x.opts.Cmd[0] != "/bin/sh" || x.opts.Rows != 24 {
		t.Fatalf("exec options: %+v", x.opts)
	}
	go func() { _, _ = x.outW.Write([]byte("hello")) }()
	waitFor(t, "output chunk", func() bool { return chunk("cmd_s1", false, "hello") })
	input("cmd_s1", &agentv1.ExecInput{Data: []byte("ls\n")})
	input("cmd_s1", &agentv1.ExecInput{Rows: 40, Cols: 100})
	input("cmd_s1", &agentv1.ExecInput{Eof: true})
	waitFor(t, "session end", func() bool { return ack("cmd_s1") != nil })
	in, sizes, cw, _ := x.state()
	if in != "ls\n" || len(sizes) != 1 || sizes[0] != "40x100" || !cw {
		t.Fatalf("input %q sizes %v closeWrite %v", in, sizes, cw)
	}
	if !chunk("cmd_s1", true, "") || !ack("cmd_s1").Ok || string(ack("cmd_s1").ResultJson) != `{"exit_code":0}` {
		t.Fatalf("end: %+v", ack("cmd_s1"))
	}
	if sk.find(func(m *agentv1.AgentMessage) bool { return m.GetAck().GetCommandId() == "cmd_in" }) != nil {
		t.Fatal("exec input must not be acked")
	}

	// The container stops: the session is closed.
	x2 := start("cmd_s2")
	e.HandleCommand(ctx, &agentv1.Command{CommandId: "cmd_stop", Body: &agentv1.Command_StopWorkload{StopWorkload: &agentv1.StopWorkload{PlacementId: "a"}}})
	waitFor(t, "session closed on stop", func() bool { return ack("cmd_s2") != nil })
	if _, _, _, closed := x2.state(); !closed || !chunk("cmd_s2", true, "") {
		t.Fatal("session was not closed with eof")
	}
	// No running container: the session is refused.
	e.HandleCommand(ctx, &agentv1.Command{CommandId: "cmd_s3", Body: &agentv1.Command_ExecSession{ExecSession: &agentv1.ExecSession{PlacementId: "a"}}})
	if a := ack("cmd_s3"); a == nil || a.Ok {
		t.Fatalf("exec into a stopped container: %+v", a)
	}
}
