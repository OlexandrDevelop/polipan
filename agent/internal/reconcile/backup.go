// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"strings"

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
	res, err := e.Backup.Run(ctx, rb)
	if err != nil {
		return e.backupFailed(err, backupSecrets(rb.Targets, rb.RepoPassword))
	}
	return jsonBytes(res), "", ""
}

func (e *Engine) runRestoreCheck(ctx context.Context, rc *agentv1.RunRestoreCheck) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	res, err := e.Backup.RunCheck(ctx, rc)
	if err != nil {
		return e.backupFailed(err, backupSecrets([]*agentv1.BackupTargetSpec{rc.Target}, rc.RepoPassword))
	}
	return jsonBytes(res), "", ""
}

func (e *Engine) restoreSnapshot(ctx context.Context, rs *agentv1.RestoreSnapshot) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	if err := e.Backup.Restore(ctx, rs); err != nil {
		return e.backupFailed(err, backupSecrets([]*agentv1.BackupTargetSpec{rs.Target}, rs.RepoPassword))
	}
	return []byte("{}"), "", ""
}

func (e *Engine) testBackupTarget(ctx context.Context, t *agentv1.TestBackupTarget) ([]byte, string, string) {
	if e.Backup == nil {
		return nil, "unsupported_version", "this agent has no backup runner"
	}
	if err := e.Backup.TestTarget(ctx, t); err != nil {
		return e.backupFailed(err, backupSecrets([]*agentv1.BackupTargetSpec{t.Target}, t.RepoPassword))
	}
	return []byte("{}"), "", ""
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
