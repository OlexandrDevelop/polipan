// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	pb "github.com/polipan/polipan/proto/agentv1"
)

// TargetResult is the result of one target of a run (REQ-BKP-002).
type TargetResult struct {
	TargetID   string `json:"target_id"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

// Manifest is what a backup run records for the restore check (REQ-BKP-102).
type Manifest struct {
	Engine      string `json:"engine"`
	Tables      *int64 `json:"tables,omitempty"`
	Rows        *int64 `json:"rows,omitempty"`
	Collections *int64 `json:"collections,omitempty"`
	Keys        *int64 `json:"keys,omitempty"`
	Files       *int64 `json:"files,omitempty"`
	Bytes       *int64 `json:"bytes,omitempty"`
	File        string `json:"file,omitempty"`
	Path        string `json:"path,omitempty"`
}

// RunResult is the result of a backup job. It is the result_json of the ack.
type RunResult struct {
	Snapshots  []TargetResult `json:"snapshots"`
	Manifest   Manifest       `json:"manifest"`
	DurationMS int64          `json:"duration_ms"`
}

func i64(v int64) *int64 { return &v }

// Run does a backup: the same data goes to every target. A target that fails does not stop the others.
func (r *Runner) Run(ctx context.Context, cmd *pb.RunBackup) (*RunResult, error) {
	start := time.Now()
	src := cmd.GetSource()
	if src == nil || len(cmd.Targets) == 0 {
		return nil, fail(CodeInternal, "the backup job has no source or no target")
	}
	host := hostOf(cmd.PolicyId)
	res := &RunResult{Snapshots: make([]TargetResult, len(cmd.Targets))}
	repos := make([]*repo, len(cmd.Targets))
	for i, t := range cmd.Targets {
		res.Snapshots[i] = TargetResult{TargetID: t.TargetId}
		rp, err := r.openRepo(t, cmd.RepoPassword)
		if err != nil {
			res.Snapshots[i].Error = r.scrub(err.Error())
			continue
		}
		defer rp.Close()
		if err := r.ensureRepo(ctx, rp); err != nil {
			res.Snapshots[i].Error = r.scrub(err.Error())
			continue
		}
		repos[i] = rp
	}
	if !anyRepo(repos) {
		return res, nil // every target failed: the server records the errors
	}

	var container string
	if src.Engine != "volume" {
		c, err := r.resolve(ctx, src.Container)
		if err != nil {
			return nil, err
		}
		container = c
		for _, h := range src.PreHook {
			if _, err := r.execOK(ctx, container, h); err != nil {
				return nil, fail(CodeHookFailed, "pre hook failed: %v", err)
			}
		}
	}
	var err error
	if src.Engine == "volume" {
		err = r.backupVolume(ctx, cmd, repos, res, host)
	} else {
		err = r.backupDump(ctx, cmd, container, repos, res, host)
	}
	if err != nil {
		return nil, err
	}
	if container != "" {
		for _, h := range src.PostHook {
			if _, err := r.execOK(ctx, container, h); err != nil {
				r.log().Warn("post hook failed", "policy", cmd.PolicyId, "error", err)
				for i := range res.Snapshots {
					if res.Snapshots[i].OK {
						res.Snapshots[i].Warning = "post hook failed: " + r.scrub(err.Error())
					}
				}
			}
		}
	}
	// Retention on every target that has the new snapshot (REQ-BKP-005).
	for i, rp := range repos {
		if rp == nil || !res.Snapshots[i].OK {
			continue
		}
		if err := r.forget(ctx, rp, host, cmd.KeepDaily, cmd.KeepWeekly, cmd.KeepMonthly); err != nil {
			res.Snapshots[i].Warning = "retention failed: " + r.scrub(err.Error())
		}
	}
	res.DurationMS = since(start)
	return res, nil
}

func anyRepo(repos []*repo) bool {
	for _, r := range repos {
		if r != nil {
			return true
		}
	}
	return false
}

// hostOf is the restic host of a policy. One host per policy keeps all its snapshots in one retention group.
func hostOf(policyID string) string { return "polipan-" + policyID }

func tagsOf(cmd *pb.RunBackup) []string {
	tags := []string{"polipan"}
	if cmd.Tag != "" {
		tags = append(tags, cmd.Tag)
	}
	return tags
}

func backupArgs(cmd *pb.RunBackup, host string) []string {
	args := []string{"backup", "--json", "--host", host}
	for _, t := range tagsOf(cmd) {
		args = append(args, "--tag", t)
	}
	if cmd.SnapshotTimeUnixMs > 0 {
		args = append(args, "--time", time.UnixMilli(cmd.SnapshotTimeUnixMs).UTC().Format("2006-01-02 15:04:05"))
	}
	return args
}

// failWriter is a writer that never fails: after an error it drops the data. It tells the first error.
type failWriter struct {
	w    io.WriteCloser
	mu   sync.Mutex
	err  error
	done bool
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil && !f.done {
		if _, err := f.w.Write(p); err != nil {
			f.err = err
		}
	}
	return len(p), nil
}

func (f *failWriter) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done = true
	if err := f.w.Close(); err != nil && f.err == nil {
		f.err = err
	}
	return f.err
}

func (f *failWriter) failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err != nil
}

// backupDump pipes the dump of the engine into restic, one restic per target (REQ-BKP-003).
func (r *Runner) backupDump(ctx context.Context, cmd *pb.RunBackup, container string, repos []*repo, res *RunResult, host string) error {
	eng, ok := engines[cmd.Source.Engine]
	if !ok {
		return fail(CodeInternal, "unknown engine %q", cmd.Source.Engine)
	}
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type proc struct {
		i      int
		done   chan error // the result of cmd.Wait
		in     *failWriter
		out    *bytes.Buffer
		stderr *limited
	}
	var procs []*proc
	var writers []io.Writer
	for i, rp := range repos {
		if rp == nil {
			continue
		}
		args := append(backupArgs(cmd, host), "--stdin", "--stdin-filename", eng.file)
		pr, pw := io.Pipe()
		c := r.command(dctx, rp, pr, nil, args...)
		out, errb := &bytes.Buffer{}, &limited{max: 8 << 10}
		c.Stdout, c.Stderr = out, errb
		if err := c.Start(); err != nil {
			res.Snapshots[i].Error = r.scrub(err.Error())
			_ = pw.Close()
			continue
		}
		p := &proc{i: i, done: make(chan error, 1), in: &failWriter{w: pw}, out: out, stderr: errb}
		procs = append(procs, p)
		writers = append(writers, p.in)
		go func(pr *io.PipeReader) {
			err := c.Wait()
			// When restic ends early, the writer must not block on its pipe.
			pr.CloseWithError(errors.New("restic ended"))
			p.done <- err
		}(pr)
	}
	if len(procs) == 0 {
		return nil
	}
	var dumpErr limited
	dumpErr.max = 8 << 10
	conn, err := r.Docker.Exec(ctx, container, docker.ExecOptions{Cmd: []string{"sh", "-c", eng.dump}, Stderr: &dumpErr})
	if err != nil {
		cancel()
		for _, p := range procs {
			<-p.done
		}
		return fail(CodeDumpFailed, "cannot start the dump: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.CloseWrite()
	n, copyErr := io.Copy(io.MultiWriter(writers...), conn)
	code, werr := conn.Wait(ctx)
	if copyErr != nil || werr != nil || code != 0 || n == 0 {
		cancel() // no snapshot is made from a broken dump
		for _, p := range procs {
			_ = p.in.w.Close()
			<-p.done
		}
		msg := firstLine(dumpErr.String())
		if copyErr != nil {
			msg = copyErr.Error()
		}
		return fail(CodeDumpFailed, "the dump of %s failed (exit code %d, %d bytes): %s", eng.name, code, n, r.scrub(msg))
	}
	for _, p := range procs {
		closeErr := p.in.close()
		waitErr := <-p.done
		tr := &res.Snapshots[p.i]
		switch {
		case p.in.failed() && waitErr != nil, waitErr != nil:
			tr.Error = r.scrub(firstLine(p.stderr.String() + " " + errString(closeErr, waitErr)))
		default:
			if s, ok := parseSummary(p.out.Bytes()); ok {
				tr.OK, tr.SnapshotID, tr.SizeBytes = true, s.SnapshotID, s.TotalBytesProcessed
			} else {
				tr.Error = "restic gave no snapshot"
			}
		}
	}
	m := Manifest{Engine: eng.name, File: eng.file, Bytes: i64(n)}
	r.fillManifest(ctx, container, eng, &m)
	res.Manifest = m
	return nil
}

func errString(errs ...error) string {
	for _, e := range errs {
		if e != nil {
			return e.Error()
		}
	}
	return ""
}

// fillManifest reads the numbers of the manifest from the source. A failure is not fatal: the check then
// has fewer numbers to compare.
func (r *Runner) fillManifest(ctx context.Context, container string, eng engineSpec, m *Manifest) {
	if eng.manifest == "" {
		return
	}
	out, err := r.execOK(ctx, container, eng.manifest)
	if err != nil {
		r.log().Warn("backup manifest not read", "engine", eng.name, "error", err)
		return
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if !ok || err != nil {
			continue
		}
		switch k {
		case "tables":
			m.Tables = &n
		case "rows":
			m.Rows = &n
		case "collections":
			m.Collections = &n
		case "keys":
			m.Keys = &n
		}
	}
}

// backupVolume backs up the files of a volume with restic (REQ-BKP-003). The container keeps running.
func (r *Runner) backupVolume(ctx context.Context, cmd *pb.RunBackup, repos []*repo, res *RunResult, host string) error {
	v, err := r.Docker.VolumeInspect(ctx, cmd.Source.VolumeName)
	if err != nil {
		return fail(CodeBackupFailed, "volume %s: %v", cmd.Source.VolumeName, err)
	}
	if _, err := os.Stat(v.Mountpoint); err != nil {
		return fail(CodeBackupFailed, "the folder of volume %s is not visible to the agent: %v", cmd.Source.VolumeName, err)
	}
	files, size, err := countFiles(v.Mountpoint)
	if err != nil {
		return fail(CodeBackupFailed, "cannot read volume %s: %v", cmd.Source.VolumeName, err)
	}
	res.Manifest = Manifest{Engine: "volume", Files: i64(files), Bytes: i64(size), Path: v.Mountpoint}
	for i, rp := range repos {
		if rp == nil {
			continue
		}
		var out bytes.Buffer
		if err := r.restic(ctx, rp, nil, &out, append(backupArgs(cmd, host), v.Mountpoint)...); err != nil {
			var re *resticError
			if !errors.As(err, &re) || re.Code != 3 { // 3: some files could not be read, the snapshot exists
				res.Snapshots[i].Error = r.scrub(err.Error())
				continue
			}
			res.Snapshots[i].Warning = "some files could not be read"
		}
		if s, ok := parseSummary(out.Bytes()); ok {
			res.Snapshots[i].OK, res.Snapshots[i].SnapshotID, res.Snapshots[i].SizeBytes = true, s.SnapshotID, s.TotalBytesProcessed
		} else {
			res.Snapshots[i].Error = "restic gave no snapshot"
		}
	}
	return nil
}

// countFiles counts the regular files and their size. Symlinks are not followed.
func countFiles(dir string) (files, size int64, err error) {
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				files++
				size += fi.Size()
			}
		}
		return nil
	})
	return files, size, err
}
