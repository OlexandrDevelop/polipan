// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// dumpTo writes one file of a snapshot to w ("restic dump").
func (r *Runner) dumpTo(ctx context.Context, rp *repo, snapshotID, file string, w io.Writer) error {
	return r.restic(ctx, rp, nil, w, "dump", snapshotID, "/"+file)
}

// feed loads a file of a snapshot into the stdin of a script that runs in a container.
func (r *Runner) feed(ctx context.Context, rp *repo, snapshotID, file, container, script string) (execResult, error) {
	pr, pw := io.Pipe()
	dumpErr := make(chan error, 1)
	go func() {
		err := r.dumpTo(ctx, rp, snapshotID, file, pw)
		_ = pw.CloseWithError(err) // nil means a normal end of the input
		dumpErr <- err
	}()
	res, err := r.execShell(ctx, container, script, pr)
	_ = pr.Close()
	if derr := <-dumpErr; derr != nil && err == nil {
		return res, fail(CodeRestoreFailed, "cannot read the snapshot: %v", derr)
	}
	return res, err
}

// Restore loads a snapshot into the service (REQ-BKP-007). Databases that work through a client (postgres, mysql,
// mariadb, mongo) must be running. Redis and volumes are files: the server stops the service before and starts it after.
func (r *Runner) Restore(ctx context.Context, cmd *pb.RestoreSnapshot) error {
	src := cmd.GetSource()
	if src == nil || cmd.Target == nil || cmd.SnapshotId == "" {
		return fail(CodeInternal, "the restore job is not complete")
	}
	rp, err := r.openRepo(cmd.Target, cmd.RepoPassword)
	if err != nil {
		return fail(CodeRestoreFailed, "%v", r.scrub(err.Error()))
	}
	defer rp.Close()
	switch src.Engine {
	case "volume":
		return r.restoreVolume(ctx, rp, cmd)
	case "redis":
		return r.restoreRedis(ctx, rp, cmd)
	}
	eng, ok := engines[src.Engine]
	if !ok || eng.restore == "" {
		return fail(CodeRestoreFailed, "a restore of %q is not supported by the agent", src.Engine)
	}
	container, err := r.resolve(ctx, src.Container)
	if err != nil {
		return err
	}
	if src.Engine == "postgres" { // drop the other connections, so the objects can be replaced
		_, _ = r.execOK(ctx, container, pgEnv+`psql `+pgConn+` -tA -c "select pg_terminate_backend(pid) from pg_stat_activity where datname = current_database() and pid <> pg_backend_pid()"`)
	}
	res, err := r.feed(ctx, rp, cmd.SnapshotId, eng.file, container, eng.restore)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fail(CodeRestoreFailed, "the restore into %s failed (exit code %d): %s", eng.name, res.Code, r.scrub(firstLine(res.Stderr)))
	}
	return nil
}

// restoreVolume replaces the files of a volume with the snapshot. The service is stopped.
func (r *Runner) restoreVolume(ctx context.Context, rp *repo, cmd *pb.RestoreSnapshot) error {
	v, err := r.Docker.VolumeInspect(ctx, cmd.Source.VolumeName)
	if err != nil && !cmd.StopContainer {
		// A move or a failover: the volume does not exist on this node yet. The files go into a new volume.
		v, err = r.Docker.VolumeCreate(ctx, cmd.Source.VolumeName, map[string]string{"polipan.managed": "true", "polipan.service": cmd.ServiceId})
	}
	if err != nil {
		return fail(CodeRestoreFailed, "volume %s: %v", cmd.Source.VolumeName, err)
	}
	list, err := r.snapshots(ctx, rp, cmd.SnapshotId)
	if err != nil || len(list) != 1 || len(list[0].Paths) == 0 {
		return fail(CodeRestoreFailed, "snapshot %s was not found in the repository", cmd.SnapshotId)
	}
	if err := clearDir(v.Mountpoint); err != nil {
		return fail(CodeRestoreFailed, "cannot clear the volume: %v", err)
	}
	if err := r.restic(ctx, rp, nil, io.Discard, "restore", cmd.SnapshotId+":"+list[0].Paths[0], "--target", v.Mountpoint); err != nil {
		return fail(CodeRestoreFailed, "%v", err)
	}
	return nil
}

// restoreRedis writes dump.rdb into the data volume of redis. Redis is stopped and loads it at the next start.
func (r *Runner) restoreRedis(ctx context.Context, rp *repo, cmd *pb.RestoreSnapshot) error {
	v, err := r.Docker.VolumeInspect(ctx, cmd.Source.VolumeName)
	if err != nil {
		return fail(CodeRestoreFailed, "volume %s: %v", cmd.Source.VolumeName, err)
	}
	var uid, gid = -1, -1
	if fi, err := os.Stat(v.Mountpoint); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
		}
	}
	if err := clearDir(v.Mountpoint); err != nil {
		return fail(CodeRestoreFailed, "cannot clear the volume: %v", err)
	}
	path := filepath.Join(v.Mountpoint, "dump.rdb")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o660)
	if err != nil {
		return fail(CodeRestoreFailed, "cannot write the dump: %v", err)
	}
	derr := r.dumpTo(ctx, rp, cmd.SnapshotId, engines["redis"].file, f)
	cerr := f.Close()
	if derr != nil || cerr != nil {
		return fail(CodeRestoreFailed, "cannot read the snapshot: %v", errors.Join(derr, cerr))
	}
	if uid >= 0 {
		_ = os.Chown(path, uid, gid)
	}
	return nil
}

// clearDir removes everything inside dir, but not dir itself.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSnapshots removes all snapshots of a policy from its targets (REQ delete_snapshots=true).
func (r *Runner) DeleteSnapshots(ctx context.Context, cmd *pb.DeleteSnapshots) error {
	var errs []error
	for _, t := range cmd.Targets {
		rp, err := r.openRepo(t, cmd.RepoPassword)
		if err != nil {
			errs = append(errs, fmt.Errorf("target %s: %w", t.TargetId, err))
			continue
		}
		list, err := r.snapshots(ctx, rp, "--host", hostOf(cmd.PolicyId))
		if err != nil {
			var re *resticError
			if errors.As(err, &re) && re.Code == 10 {
				rp.Close()
				continue // no repository: nothing to delete
			}
			errs = append(errs, fmt.Errorf("target %s: %w", t.TargetId, err))
			rp.Close()
			continue
		}
		if len(list) > 0 {
			args := []string{"forget", "--prune"}
			for _, s := range list {
				args = append(args, s.ID)
			}
			if err := r.restic(ctx, rp, nil, io.Discard, args...); err != nil {
				errs = append(errs, fmt.Errorf("target %s: %w", t.TargetId, err))
			}
		}
		rp.Close()
	}
	return errors.Join(errs...)
}

// TestTarget writes a small snapshot into a test repository on the target and reads it back (REQ-BKP-002).
// It returns the host key that ssh saw for an SFTP target without a pinned key, as one known_hosts line, so the
// server can save it and every later job checks it strictly (R16). Otherwise it returns "".
func (r *Runner) TestTarget(ctx context.Context, cmd *pb.TestBackupTarget) (string, error) {
	t := proto2test(cmd.Target)
	rp, err := r.openRepo(t, cmd.RepoPassword)
	if err != nil {
		return "", fail(CodeTargetFailed, "%v", r.scrub(err.Error()))
	}
	defer rp.Close()
	if err := r.ensureRepo(ctx, rp); err != nil {
		return "", fail(CodeTargetFailed, "%v", r.scrub(err.Error()))
	}
	payload := "polipan target test"
	var out limited
	out.max = 4 << 10
	if err := r.restic(ctx, rp, stringsReader(payload), &out, "backup", "--json", "--host", "polipan-test", "--tag", "test", "--stdin", "--stdin-filename", "test.txt"); err != nil {
		return "", fail(CodeTargetFailed, "cannot write to the target: %v", r.scrub(err.Error()))
	}
	s, ok := parseSummary([]byte(out.String()))
	if !ok {
		return "", fail(CodeTargetFailed, "restic gave no snapshot")
	}
	var got limited
	got.max = 4 << 10
	if err := r.dumpTo(ctx, rp, s.SnapshotID, "test.txt", &got); err != nil || got.String() != payload {
		return "", fail(CodeTargetFailed, "cannot read back from the target: %v", r.scrub(errString(err)))
	}
	_ = r.restic(ctx, rp, nil, io.Discard, "forget", "--prune", s.SnapshotID)
	return rp.firstHostKey(), nil
}
