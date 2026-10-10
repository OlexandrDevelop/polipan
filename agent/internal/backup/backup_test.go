// SPDX-License-Identifier: Apache-2.0

package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polipan/polipan/agent/internal/backup"
	"github.com/polipan/polipan/agent/internal/docker"
	pb "github.com/polipan/polipan/proto/agentv1"
)

// These tests use the real restic binary and local repositories. They are skipped when restic is not installed.
func needRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
}

type fakeConn struct {
	r    io.Reader
	code int
	in   bytes.Buffer
}

func (f *fakeConn) Read(p []byte) (int, error)                   { return f.r.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error)                  { return f.in.Write(p) }
func (f *fakeConn) CloseWrite() error                            { return nil }
func (f *fakeConn) Resize(context.Context, uint32, uint32) error { return nil }
func (f *fakeConn) Wait(context.Context) (int, error)            { return f.code, nil }
func (f *fakeConn) Close() error                                 { return nil }

// fakeDocker answers Exec by looking for a word in the script.
type fakeDocker struct {
	dump     string
	dumpCode int
	manifest string
	volume   string
	scripts  []string
}

func (f *fakeDocker) Exec(_ context.Context, _ string, o docker.ExecOptions) (docker.ExecConn, error) {
	script := o.Cmd[len(o.Cmd)-1]
	f.scripts = append(f.scripts, script)
	switch {
	case strings.Contains(script, "pg_dump"):
		return &fakeConn{r: strings.NewReader(f.dump), code: f.dumpCode}, nil
	case strings.Contains(script, "psql"):
		return &fakeConn{r: strings.NewReader(f.manifest)}, nil
	}
	return &fakeConn{r: strings.NewReader("")}, nil
}
func (f *fakeDocker) Inspect(context.Context, string) (*docker.Container, error) {
	return &docker.Container{ID: "c1", State: "running"}, nil
}
func (f *fakeDocker) List(context.Context, map[string]string) ([]docker.Container, error) {
	return nil, nil
}
func (f *fakeDocker) CreateAndStart(context.Context, docker.ContainerSpec) (string, error) {
	return "", io.ErrUnexpectedEOF
}
func (f *fakeDocker) Remove(context.Context, string, bool) error { return nil }
func (f *fakeDocker) Logs(context.Context, string, int) (string, error) {
	return "", nil
}
func (f *fakeDocker) VolumeCreate(context.Context, string, map[string]string) (*docker.Volume, error) {
	return &docker.Volume{}, nil
}
func (f *fakeDocker) VolumeInspect(context.Context, string) (*docker.Volume, error) {
	return &docker.Volume{Name: "v", Mountpoint: f.volume}, nil
}
func (f *fakeDocker) VolumeRemove(context.Context, string) error { return nil }
func (f *fakeDocker) PullImage(context.Context, string, string) (string, error) {
	return "", nil
}

func target(id, dir string) *pb.BackupTargetSpec {
	return &pb.BackupTargetSpec{TargetId: id, Kind: "local", Repository: dir}
}

func newRunner(f *fakeDocker, t *testing.T) *backup.Runner {
	return &backup.Runner{Docker: f, TmpDir: t.TempDir()}
}

func pgJob(targets ...*pb.BackupTargetSpec) *pb.RunBackup {
	return &pb.RunBackup{PolicyId: "pol_1", RunId: "bkr_1", RepoPassword: "pw-secret-1", Targets: targets, Tag: "manual",
		Source: &pb.BackupSource{Engine: "postgres", Container: "pp-x"}, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}
}

// resticDump reads a file of a snapshot with the restic binary alone (AC-OSS-05 on the agent side).
func resticDump(t *testing.T, repo, password, snapshot, file string) string {
	t.Helper()
	cmd := exec.Command("restic", "-r", repo, "dump", snapshot, "/"+file)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("restic dump: %v", err)
	}
	return string(out)
}

func resticSnapshots(t *testing.T, repo, password string) []map[string]any {
	t.Helper()
	cmd := exec.Command("restic", "-r", repo, "snapshots", "--json")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("restic snapshots: %v", err)
	}
	var list []map[string]any
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

// AC-BKP-01 and AC-BKP-02: the same data goes to every target. A target that fails does not stop the others.
func TestRunTwoTargetsOneFails(t *testing.T) {
	needRestic(t)
	f := &fakeDocker{dump: strings.Repeat("row data\n", 5000), manifest: "tables=3\nrows=40\n"}
	r := newRunner(f, t)
	a, b := t.TempDir(), t.TempDir()
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), pgJob(target("bkp_a", a), target("bkp_bad", filepath.Join(bad, "repo")), target("bkp_b", b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Snapshots) != 3 || !res.Snapshots[0].OK || res.Snapshots[1].OK || !res.Snapshots[2].OK || res.Snapshots[1].Error == "" {
		t.Fatalf("snapshots: %+v", res.Snapshots)
	}
	for i, dir := range map[int]string{0: a, 2: b} {
		if got := resticDump(t, dir, "pw-secret-1", res.Snapshots[i].SnapshotID, "dump.pgdump"); got != f.dump {
			t.Fatalf("target %d: the dump is not the same (%d bytes)", i, len(got))
		}
	}
	if res.Manifest.Tables == nil || *res.Manifest.Tables != 3 || *res.Manifest.Rows != 40 || res.Manifest.Engine != "postgres" {
		t.Fatalf("manifest: %+v", res.Manifest)
	}
	// The password never appears in a script.
	for _, s := range f.scripts {
		if strings.Contains(s, "pw-secret-1") {
			t.Fatalf("script has a secret: %s", s)
		}
	}
}

// A dump that fails makes no snapshot.
func TestDumpFailureMakesNoSnapshot(t *testing.T) {
	needRestic(t)
	f := &fakeDocker{dump: "partial", dumpCode: 1}
	r := newRunner(f, t)
	dir := t.TempDir()
	_, err := r.Run(context.Background(), pgJob(target("bkp_a", dir)))
	var be *backup.Error
	if err == nil || !errors.As(err, &be) || be.Code != backup.CodeDumpFailed {
		t.Fatalf("err = %v", err)
	}
	if list := resticSnapshots(t, dir, "pw-secret-1"); len(list) != 0 {
		t.Fatalf("%d snapshots from a broken dump", len(list))
	}
}

// AC-BKP-05: with keep_daily 7, the 8th daily snapshot removes the oldest.
func TestRetentionKeepDaily(t *testing.T) {
	needRestic(t)
	f := &fakeDocker{dump: "data", manifest: "tables=1\nrows=1\n"}
	r := newRunner(f, t)
	dir := t.TempDir()
	var first string
	for days := 7; days >= 0; days-- {
		job := pgJob(target("bkp_a", dir))
		job.KeepWeekly, job.KeepMonthly = 0, 0 // keep_daily alone: weekly and monthly rules would keep the oldest snapshot too
		job.SnapshotTimeUnixMs = time.Now().Add(-time.Duration(days)*24*time.Hour - time.Hour).UnixMilli()
		res, err := r.Run(context.Background(), job)
		if err != nil || !res.Snapshots[0].OK {
			t.Fatalf("day %d: %v %+v", days, err, res)
		}
		if days == 7 {
			first = res.Snapshots[0].SnapshotID
		}
		if want, got := min(8-days, 7), len(resticSnapshots(t, dir, "pw-secret-1")); got != want {
			t.Fatalf("day %d: %d snapshots, want %d", days, got, want)
		}
	}
	for _, s := range resticSnapshots(t, dir, "pw-secret-1") {
		if s["id"] == first {
			t.Fatal("the oldest snapshot is still there")
		}
	}
}

func makeVolume(t *testing.T, files int) string {
	dir := t.TempDir()
	for i := 0; i < files; i++ {
		p := filepath.Join(dir, "d", string(rune('a'+i%5)))
		_ = os.MkdirAll(p, 0o755)
		if err := os.WriteFile(filepath.Join(p, "f"+string(rune('0'+i%10))+strings.Repeat("x", i)), []byte(strings.Repeat("y", 100+i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func volumeJob(dir string) *pb.RunBackup {
	return &pb.RunBackup{PolicyId: "pol_v", RunId: "bkr_v", RepoPassword: "vol-pw", Targets: []*pb.BackupTargetSpec{target("bkp_a", dir)},
		Source: &pb.BackupSource{Engine: "volume", VolumeName: "v"}, KeepDaily: 7}
}

// AC-BKP-09: a volume restore check restores to a temporary folder and compares the file count and the size with the manifest.
func TestVolumeBackupAndCheck(t *testing.T) {
	needRestic(t)
	vol := makeVolume(t, 40)
	f := &fakeDocker{volume: vol}
	r := newRunner(f, t)
	repoDir := t.TempDir()
	res, err := r.Run(context.Background(), volumeJob(repoDir))
	if err != nil || !res.Snapshots[0].OK {
		t.Fatalf("run: %v %+v", err, res)
	}
	if res.Manifest.Files == nil || *res.Manifest.Files != 40 {
		t.Fatalf("manifest: %+v", res.Manifest)
	}
	mj, _ := json.Marshal(res.Manifest)
	check := func(manifest []byte) *backup.CheckResult {
		out, err := r.RunCheck(context.Background(), &pb.RunRestoreCheck{CheckId: "rck_1", RepoPassword: "vol-pw", SnapshotId: res.Snapshots[0].SnapshotID,
			Target: target("bkp_a", repoDir), Source: &pb.BackupSource{Engine: "volume"}, ManifestJson: manifest, Mode: "full"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := check(mj); !out.Passed || len(out.Checks) != 4 {
		t.Fatalf("check: %+v", out)
	}
	// A manifest with another file count fails the check.
	var m backup.Manifest
	_ = json.Unmarshal(mj, &m)
	wrong := int64(80)
	m.Files = &wrong
	bad, _ := json.Marshal(m)
	if out := check(bad); out.Passed {
		t.Fatalf("a wrong file count passed: %+v", out)
	}
	// The temporary folder is gone.
	left, _ := filepath.Glob(filepath.Join(r.TmpDir, "pp-check-*"))
	if len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

// AC-BKP-08: a corrupted snapshot fails the check.
func TestCorruptedSnapshotFailsCheck(t *testing.T) {
	needRestic(t)
	vol := makeVolume(t, 20)
	f := &fakeDocker{volume: vol}
	r := newRunner(f, t)
	repoDir := t.TempDir()
	res, err := r.Run(context.Background(), volumeJob(repoDir))
	if err != nil || !res.Snapshots[0].OK {
		t.Fatalf("run: %v %+v", err, res)
	}
	packs, _ := filepath.Glob(filepath.Join(repoDir, "data", "*", "*"))
	if len(packs) == 0 {
		t.Fatal("no pack files")
	}
	for _, p := range packs {
		if err := os.WriteFile(p, []byte("corrupted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mj, _ := json.Marshal(res.Manifest)
	out, err := r.RunCheck(context.Background(), &pb.RunRestoreCheck{CheckId: "rck_2", RepoPassword: "vol-pw", SnapshotId: res.Snapshots[0].SnapshotID,
		Target: target("bkp_a", repoDir), Source: &pb.BackupSource{Engine: "volume"}, ManifestJson: mj, Mode: "full"})
	if err != nil || out.Passed {
		t.Fatalf("a corrupted repository passed: %v %+v", err, out)
	}
}

// Restore of a volume replaces the files.
func TestVolumeRestore(t *testing.T) {
	needRestic(t)
	vol := makeVolume(t, 10)
	f := &fakeDocker{volume: vol}
	r := newRunner(f, t)
	repoDir := t.TempDir()
	res, err := r.Run(context.Background(), volumeJob(repoDir))
	if err != nil || !res.Snapshots[0].OK {
		t.Fatalf("run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vol, "new.txt"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(context.Background(), &pb.RestoreSnapshot{RepoPassword: "vol-pw", SnapshotId: res.Snapshots[0].SnapshotID,
		Target: target("bkp_a", repoDir), Source: &pb.BackupSource{Engine: "volume", VolumeName: "v"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vol, "new.txt")); err == nil {
		t.Fatal("a file that is not in the snapshot is still there")
	}
	files, _ := filepath.Glob(filepath.Join(vol, "d", "*", "*"))
	if len(files) != 10 {
		t.Fatalf("restored %d files", len(files))
	}
}

// The test of a target writes and reads back.
func TestTargetTest(t *testing.T) {
	needRestic(t)
	r := newRunner(&fakeDocker{}, t)
	if _, err := r.TestTarget(context.Background(), &pb.TestBackupTarget{Target: target("bkp_a", t.TempDir()), RepoPassword: "x-pass"}); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(bad, nil, 0o600)
	if _, err := r.TestTarget(context.Background(), &pb.TestBackupTarget{Target: target("bkp_a", filepath.Join(bad, "x")), RepoPassword: "x-pass"}); err == nil {
		t.Fatal("a target that cannot be written passed")
	}
}

// Delete of all snapshots of a policy.
func TestDeleteSnapshots(t *testing.T) {
	needRestic(t)
	f := &fakeDocker{dump: "data", manifest: "tables=1\nrows=1\n"}
	r := newRunner(f, t)
	dir := t.TempDir()
	if _, err := r.Run(context.Background(), pgJob(target("bkp_a", dir))); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteSnapshots(context.Background(), &pb.DeleteSnapshots{PolicyId: "pol_1", RepoPassword: "pw-secret-1", Targets: []*pb.BackupTargetSpec{target("bkp_a", dir)}}); err != nil {
		t.Fatal(err)
	}
	if list := resticSnapshots(t, dir, "pw-secret-1"); len(list) != 0 {
		t.Fatalf("%d snapshots left", len(list))
	}
}

// A safety backup (before a restore) does not prune: it must not remove the snapshot to restore.
func TestSafetyRunDoesNotPrune(t *testing.T) {
	needRestic(t)
	f := &fakeDocker{dump: "data", manifest: "tables=1\nrows=1\n"}
	r := newRunner(f, t)
	dir := t.TempDir()
	for days := 3; days >= 1; days-- {
		job := pgJob(target("bkp_a", dir))
		job.KeepDaily, job.KeepWeekly, job.KeepMonthly = 3, 0, 0
		job.SnapshotTimeUnixMs = time.Now().Add(-time.Duration(days)*24*time.Hour - time.Hour).UnixMilli()
		if _, err := r.Run(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	job := pgJob(target("bkp_a", dir))
	job.KeepDaily, job.KeepWeekly, job.KeepMonthly, job.Tag = 3, 0, 0, "safety"
	if _, err := r.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if n := len(resticSnapshots(t, dir, "pw-secret-1")); n != 4 {
		t.Fatalf("%d snapshots after a safety run, want 4 (nothing pruned)", n)
	}
	job.Tag = "manual"
	if _, err := r.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if n := len(resticSnapshots(t, dir, "pw-secret-1")); n != 3 {
		t.Fatalf("%d snapshots after a normal run, want 3", n)
	}
}
