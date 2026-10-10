// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	pb "github.com/polipan/polipan/proto/agentv1"
)

// Check is one line of the result of a restore check (REQ-BKP-102).
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// CheckResult is the result of a restore check. It is the result_json of the ack.
type CheckResult struct {
	Passed     bool    `json:"passed"`
	Checks     []Check `json:"checks"`
	DurationMS int64   `json:"duration_ms"`
}

// LabelRole marks the temporary containers of restore checks.
const LabelRole = "polipan.role"

// RoleRestoreCheck is the value of LabelRole.
const RoleRestoreCheck = "restore-check"

// staleAfter is the age after which a temporary container of an older check is removed.
const staleAfter = 3 * time.Hour

func (c *CheckResult) add(name string, ok bool, format string, a ...any) {
	c.Checks = append(c.Checks, Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, a...)})
}

func (c *CheckResult) finish(start time.Time) *CheckResult {
	c.Passed = len(c.Checks) > 0
	for _, ch := range c.Checks {
		if !ch.OK {
			c.Passed = false
		}
	}
	c.DurationMS = since(start)
	return c
}

// RunCheck restores a snapshot in a temporary place and checks it (REQ-BKP-100, REQ-BKP-101, REQ-BKP-104).
// A problem with the snapshot is a failed check, not an error. An error means the check could not run.
func (r *Runner) RunCheck(ctx context.Context, cmd *pb.RunRestoreCheck) (*CheckResult, error) {
	start := time.Now()
	res := &CheckResult{}
	rp, err := r.openRepo(cmd.Target, cmd.RepoPassword)
	if err != nil {
		return nil, fail(CodeRestoreFailed, "%v", r.scrub(err.Error()))
	}
	defer rp.Close()
	var m Manifest
	if len(cmd.ManifestJson) > 0 {
		_ = json.Unmarshal(cmd.ManifestJson, &m)
	}
	engine := cmd.GetSource().GetEngine()
	if engine == "volume" {
		r.checkVolume(ctx, rp, cmd, &m, res)
		return res.finish(start), nil
	}
	if engine == "controlplane" {
		engine = "postgres"
	}
	// The snapshot must be readable before a container is started.
	if _, err := r.snapshots(ctx, rp, cmd.SnapshotId); err != nil {
		res.add("snapshot", false, "cannot read the snapshot: %s", r.scrub(firstLine(err.Error())))
		return res.finish(start), nil
	}
	if err := r.checkDatabase(ctx, rp, cmd, engine, &m, res); err != nil {
		return nil, err
	}
	return res.finish(start), nil
}

// sweep removes the temporary containers of old checks that were not cleaned (a crash of the agent).
func (r *Runner) sweep(ctx context.Context) {
	list, err := r.Docker.List(ctx, map[string]string{LabelRole: RoleRestoreCheck})
	if err != nil {
		return
	}
	for _, c := range list {
		if time.Since(c.StartedAt) > staleAfter || (c.State != "running" && time.Since(c.StartedAt) > time.Minute) {
			_ = r.Docker.Remove(ctx, c.ID, true)
			_ = r.Docker.VolumeRemove(ctx, strings.TrimPrefix(c.Name, "/"))
		}
	}
}

// tempDB is a temporary database container: no published ports, no network, limits, and its own data volume.
type tempDB struct {
	r    *Runner
	id   string
	name string
}

func (r *Runner) startTemp(ctx context.Context, cmd *pb.RunRestoreCheck, name string, env map[string]string, dataPath string, command []string) (*tempDB, error) {
	r.sweep(ctx)
	mem := int64(cmd.MemoryLimitMb)
	if mem == 0 {
		mem = 768
	}
	cpu := int64(cmd.CpuLimitMillis)
	if cpu == 0 {
		cpu = 1000
	}
	if _, err := r.Docker.VolumeCreate(ctx, name, map[string]string{LabelRole: RoleRestoreCheck}); err != nil {
		return nil, fmt.Errorf("temporary volume: %w", err)
	}
	spec := docker.ContainerSpec{
		Name: name, Image: cmd.ImageRef, Env: env, Cmd: command,
		Labels:  map[string]string{LabelRole: RoleRestoreCheck, "polipan.restore-check": cmd.CheckId},
		Network: "none", RestartPolicy: "no", MemoryBytes: mem << 20, NanoCPUs: cpu * 1_000_000,
		Mounts: []docker.Mount{{Type: "volume", Source: name, Target: dataPath}},
	}
	id, err := r.Docker.CreateAndStart(ctx, spec)
	if err != nil && strings.Contains(err.Error(), "No such image") {
		auth := ""
		if r.PullAuth != nil && cmd.RegistryAuthRef != "" {
			if auth, err = r.PullAuth(cmd.RegistryAuthRef); err != nil {
				return nil, err
			}
		}
		if _, perr := r.Docker.PullImage(ctx, cmd.ImageRef, auth); perr != nil {
			_ = r.Docker.VolumeRemove(context.Background(), name)
			return nil, fmt.Errorf("pull %s: %w", cmd.ImageRef, perr)
		}
		id, err = r.Docker.CreateAndStart(ctx, spec)
	}
	t := &tempDB{r: r, id: id, name: name}
	if err != nil {
		t.remove()
		return nil, err
	}
	return t, nil
}

// remove deletes the container and its data. It also runs after a failure or a cancel (REQ-BKP-104).
func (t *tempDB) remove() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if t.id != "" {
		_ = t.r.Docker.Remove(ctx, t.id, true)
	}
	_ = t.r.Docker.VolumeRemove(ctx, t.name)
}

// waitFor runs script in the container until it exits 0 or the time is over.
func (r *Runner) waitFor(ctx context.Context, container, script string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		if _, err := r.execOK(ctx, container, script); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("not ready in %s: %v", limit, last)
}

// checkDatabase is the check of REQ-BKP-101 for the dump engines and for redis.
func (r *Runner) checkDatabase(ctx context.Context, rp *repo, cmd *pb.RunRestoreCheck, engine string, m *Manifest, res *CheckResult) error {
	name := "pp-check-" + strings.ToLower(strings.TrimPrefix(cmd.CheckId, "rck_"))
	var env map[string]string
	var dataPath, readyScript string
	var command []string
	eng := engines[engine]
	switch engine {
	case "postgres":
		env = map[string]string{"POSTGRES_PASSWORD": "check", "POSTGRES_USER": "postgres", "POSTGRES_DB": "postgres"}
		dataPath = "/var/lib/postgresql/data"
		readyScript = `PGPASSWORD=check psql -h 127.0.0.1 -U postgres -d postgres -tA -c 'select 1'` // TCP opens after the init phase
	case "mysql", "mariadb":
		env = map[string]string{"MYSQL_ROOT_PASSWORD": "check", "MARIADB_ROOT_PASSWORD": "check"}
		dataPath = "/var/lib/mysql"
		readyScript = `T=$(command -v mariadb-admin || command -v mysqladmin); "$T" ping -h 127.0.0.1 -uroot -pcheck --silent`
	case "mongo":
		dataPath = "/data/db"
		readyScript = `mongosh --quiet --eval 'db.runCommand({ping:1}).ok' | grep -q 1`
	case "redis":
		dataPath = "/data"
		// Redis starts when the file "ready" exists, so the dump is in place before the first load.
		command = []string{"sh", "-c", `while [ ! -f /data/ready ]; do sleep 0.2; done; exec redis-server --dir /data --dbfilename dump.rdb --appendonly no --save ""`}
		readyScript = `redis-cli ping | grep -q PONG`
	default:
		return fail(CodeRestoreFailed, "a restore check of %q is not supported", engine)
	}
	t, err := r.startTemp(ctx, cmd, name, env, dataPath, command)
	if err != nil {
		res.add("temp_container", false, "cannot start the temporary container: %s", r.scrub(firstLine(err.Error())))
		return nil
	}
	defer t.remove()

	if engine == "redis" {
		if _, err := r.feed(ctx, rp, cmd.SnapshotId, eng.file, t.id, `cat > /data/dump.rdb && touch /data/ready`); err != nil {
			res.add("restore", false, "%s", r.scrub(firstLine(err.Error())))
			return nil
		}
		res.add("restore", true, "dump loaded")
	}
	if err := r.waitFor(ctx, t.id, readyScript, 3*time.Minute); err != nil {
		res.add("start", false, "the temporary database did not start: %s", r.scrub(firstLine(err.Error())))
		return nil
	}
	res.add("start", true, "the temporary database is ready")
	if engine != "redis" {
		restore := restoreForCheck(engine)
		out, err := r.feed(ctx, rp, cmd.SnapshotId, eng.file, t.id, restore)
		switch {
		case err != nil:
			res.add("restore", false, "%s", r.scrub(firstLine(err.Error())))
			return nil
		case out.Code != 0:
			res.add("restore", false, "exit code %d: %s", out.Code, r.scrub(firstLine(out.Stderr)))
			return nil
		}
		res.add("restore", true, "loaded without errors")
	}
	r.checkNumbers(ctx, t.id, engine, m, res)
	return nil
}

// restoreForCheck is the restore command in the temporary container: it knows the fixed credentials.
func restoreForCheck(engine string) string {
	switch engine {
	case "postgres":
		return `PGPASSWORD=check exec pg_restore -h 127.0.0.1 -U postgres -d postgres --no-owner --no-privileges`
	case "mysql":
		return `exec mysql -uroot -pcheck`
	case "mariadb":
		return `T=$(command -v mariadb || command -v mysql); exec "$T" -uroot -pcheck`
	case "mongo":
		return `exec mongorestore --archive --drop --quiet`
	}
	return ""
}

// checkNumbers runs SELECT 1 and compares the counts with the manifest of the backup run.
func (r *Runner) checkNumbers(ctx context.Context, container, engine string, m *Manifest, res *CheckResult) {
	num := func(script string) (int64, error) {
		out, err := r.execOK(ctx, container, script)
		if err != nil {
			return 0, err
		}
		fields := strings.Fields(out)
		if len(fields) == 0 {
			return 0, errors.New("no answer")
		}
		return strconv.ParseInt(fields[len(fields)-1], 10, 64)
	}
	compare := func(name string, want *int64, script string, tolerance func(want int64) int64) {
		got, err := num(script)
		switch {
		case err != nil:
			res.add(name, false, "cannot count: %s", r.scrub(firstLine(err.Error())))
		case want == nil:
			res.add(name, true, "%d found (the backup run has no number to compare)", got)
		case abs64(got-*want) > tolerance(*want):
			res.add(name, false, "found %d, the backup run had %d", got, *want)
		default:
			res.add(name, true, "found %d, the backup run had %d", got, *want)
		}
	}
	exact := func(int64) int64 { return 0 }
	switch engine {
	case "postgres":
		compare("select_1", ptr1(), `PGPASSWORD=check psql -h 127.0.0.1 -U postgres -d postgres -tA -c 'select 1'`, exact)
		compare("table_count", m.Tables, `PGPASSWORD=check psql -h 127.0.0.1 -U postgres -d postgres -tA -c "`+pgTables+`"`, exact)
		compare("row_estimate", m.Rows, `PGPASSWORD=check psql -h 127.0.0.1 -U postgres -d postgres -tA -c "`+pgRows+`"`,
			func(want int64) int64 { return max64(want/20, 5) }) // 5 % or 5 rows: the source may change during the dump
	case "mysql", "mariadb":
		tool := `T=$(command -v mariadb || command -v mysql); "$T" -N -uroot -pcheck -e `
		compare("select_1", ptr1(), tool+`'select 1'`, exact)
		compare("table_count", m.Tables, tool+`"select count(*) from information_schema.tables where table_schema not in ('mysql','information_schema','performance_schema','sys')"`, exact)
	case "mongo":
		compare("ping", ptr1(), `mongosh --quiet --eval 'print(db.runCommand({ping:1}).ok)' | sed 's/\..*//'`, exact)
		compare("collection_count", m.Collections, `mongosh --quiet --eval 'let n=0; db.adminCommand({listDatabases:1}).databases.forEach(d=>{ if(!["admin","local","config"].includes(d.name)) n+=db.getSiblingDB(d.name).getCollectionNames().length }); print(n)'`, exact)
	case "redis":
		compare("dbsize", m.Keys, `redis-cli dbsize`, func(want int64) int64 { return max64(want/100, 0) })
	}
}

func ptr1() *int64 { v := int64(1); return &v }

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// checkVolume is the check of a volume policy (REQ-BKP-101): restic check of a part of the data, then a restore to a
// temporary folder and a comparison of the file count and the size with the manifest (within 1 %).
func (r *Runner) checkVolume(ctx context.Context, rp *repo, cmd *pb.RunRestoreCheck, m *Manifest, res *CheckResult) {
	if err := r.restic(ctx, rp, nil, io.Discard, "check", "--read-data-subset=5%"); err != nil {
		res.add("restic_check", false, "%s", r.scrub(firstLine(err.Error())))
		return
	}
	res.add("restic_check", true, "5%% of the data was read and is intact")
	if cmd.Mode == "sample" {
		return
	}
	list, err := r.snapshots(ctx, rp, cmd.SnapshotId)
	if err != nil || len(list) != 1 || len(list[0].Paths) == 0 {
		res.add("snapshot", false, "snapshot %s was not found", cmd.SnapshotId)
		return
	}
	dir, err := os.MkdirTemp(r.tmp(), "pp-check-")
	if err != nil {
		res.add("restore", false, "cannot make a temporary folder: %v", err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := r.restic(ctx, rp, nil, io.Discard, "restore", cmd.SnapshotId+":"+list[0].Paths[0], "--target", dir); err != nil {
		res.add("restore", false, "%s", r.scrub(firstLine(err.Error())))
		return
	}
	res.add("restore", true, "restored to a temporary folder")
	files, size, err := countFiles(filepath.Clean(dir))
	if err != nil {
		res.add("file_count", false, "cannot read the restored files: %v", err)
		return
	}
	within := func(got int64, want *int64) bool {
		if want == nil {
			return true
		}
		diff := abs64(got - *want)
		return diff*100 <= *want || diff == 0
	}
	res.add("file_count", within(files, m.Files), "restored %d files, the backup run had %s", files, num(m.Files))
	res.add("total_size", within(size, m.Bytes), "restored %d bytes, the backup run had %s", size, num(m.Bytes))
}

func num(v *int64) string {
	if v == nil {
		return "no number"
	}
	return strconv.FormatInt(*v, 10)
}
