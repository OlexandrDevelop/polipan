// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/backup"
	"github.com/polipan/polipan/proto/agentv1"
)

// backupSecrets are the secrets of a backup command. They are removed from every error text.
func backupSecrets(targets []*agentv1.BackupTargetSpec, password string) []string {
	var out []string
	add := func(v string) {
		if len(v) >= 4 {
			out = append(out, v)
		}
	}
	add(password)
	for _, t := range targets {
		if t == nil {
			continue
		}
		for _, v := range t.Env {
			add(v)
		}
		add(string(t.SshKey))
	}
	return out
}

// scrubResult makes the JSON of a run or check result. The text fields hold no secret: the known values
// of the job and of the services are replaced in the whole text, as a last net (R11).
func (e *Engine) scrubResult(res any, secrets []string) []byte {
	return []byte(scrubWith(secrets, e.scrub(string(jsonBytes(res)))))
}

func scrubWith(secrets []string, s string) string {
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// backupFailed turns an error of a backup job into the code and the message of an ack.
func (e *Engine) backupFailed(err error, secrets []string) ([]byte, string, string) {
	var be *backup.Error
	code := "internal_error"
	if errors.As(err, &be) {
		code = be.Code
	} else {
		code = codeFor(err, code)
	}
	return nil, code, scrubWith(secrets, e.scrub(err.Error()))
}

func (e *Engine) runBackup(ctx context.Context, rb *agentv1.RunBackup) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	secrets := backupSecrets(rb.Targets, rb.RepoPassword)
	res, err := e.Backup.Run(ctx, rb)
	if err != nil {
		return e.backupFailed(err, secrets)
	}
	return e.scrubResult(res, secrets), "", ""
}

func (e *Engine) runRestoreCheck(ctx context.Context, rc *agentv1.RunRestoreCheck) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	secrets := backupSecrets([]*agentv1.BackupTargetSpec{rc.Target}, rc.RepoPassword)
	res, err := e.Backup.RunCheck(ctx, rc)
	if err != nil {
		return e.backupFailed(err, secrets)
	}
	return e.scrubResult(res, secrets), "", ""
}

func (e *Engine) restoreSnapshot(ctx context.Context, rs *agentv1.RestoreSnapshot) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	secrets := backupSecrets([]*agentv1.BackupTargetSpec{rs.Target}, rs.RepoPassword)
	// Files (redis, volumes): the container is stopped during the restore, and the reconcile loop must not start it.
	var restart func() error
	if rs.StopContainer && rs.GetSource().GetContainer() != "" {
		c, err := e.Docker.Inspect(ctx, rs.Source.Container)
		if err != nil {
			return e.backupFailed(fmt.Errorf("container %s: %w", rs.Source.Container, err), secrets)
		}
		placement := c.Labels[LabelPlacement]
		if c.State == "running" {
			e.suppress(placement)
			if err := e.Docker.Stop(ctx, c.ID, 30); err != nil {
				e.unsuppress(placement)
				return e.backupFailed(fmt.Errorf("cannot stop %s: %w", rs.Source.Container, err), secrets)
			}
			restart = func() error {
				sctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				e.unsuppress(placement)
				// The volume guard (08 section 7a): another container may hold the data now.
				e.startMu.Lock()
				defer e.startMu.Unlock()
				// The actual mounts of the container count: the desired state may have changed during the restore.
				err := e.guardContainer(sctx, c.ID, e.workload(placement))
				if err == nil {
					err = e.Docker.Start(sctx, c.ID)
				}
				if err != nil {
					e.Log.Error("cannot start the container after a restore", "container", c.ID, "error", err)
				}
				return err
			}
		}
	}
	err := e.Backup.Restore(ctx, rs)
	if restart != nil {
		if rerr := restart(); rerr != nil && err == nil {
			err = rerr // the data is restored, but the container did not start again (volume_in_use or Docker)
		}
	}
	if err != nil {
		return e.backupFailed(err, secrets)
	}
	return []byte("{}"), "", ""
}

func (e *Engine) testBackupTarget(ctx context.Context, t *agentv1.TestBackupTarget) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	knownHost, err := e.Backup.TestTarget(ctx, t)
	if err != nil {
		return e.backupFailed(err, backupSecrets([]*agentv1.BackupTargetSpec{t.Target}, t.RepoPassword))
	}
	// The host key of an SFTP server that was seen for the first time. The server keeps it (R16).
	return jsonBytes(map[string]string{"known_host": knownHost}), "", ""
}

func (e *Engine) deleteSnapshots(ctx context.Context, d *agentv1.DeleteSnapshots) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	if err := e.Backup.DeleteSnapshots(ctx, d); err != nil {
		return e.backupFailed(err, backupSecrets(d.Targets, d.RepoPassword))
	}
	return []byte("{}"), "", ""
}

func (e *Engine) unsuppress(placement string) {
	e.mu.Lock()
	delete(e.suppressed, placement)
	e.mu.Unlock()
}
