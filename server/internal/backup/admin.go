// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/secrets"
)

// PasswordCommand is "polipan-server admin backup-password --policy <id>". It prints the restic repository
// password of a policy and the repository of each target, so the snapshots can be read with the restic binary alone
// (REQ-OSS-006, AC-OSS-05). The panel never shows this password. The read is written to the audit log.
func PasswordCommand(ctx context.Context, cfg config.Config, args []string, getenv func(string) string, out io.Writer) error {
	fset := flag.NewFlagSet("backup-password", flag.ContinueOnError)
	policy := fset.String("policy", "", "id of the backup policy")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *policy == "" {
		return errors.New("--policy is required")
	}
	kek, err := pki.LoadKEK(cfg.MasterKeyFile, getenv)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	sec, err := secrets.NewManager(pool, kek)
	if err != nil {
		return err
	}
	s := New(Deps{DB: pool, Secrets: sec})
	var orgID, secretID string
	var targets []string
	if err := pool.QueryRow(ctx, `SELECT org_id, repo_password_secret_id, target_ids FROM backup_policies WHERE id = $1`, *policy).Scan(&orgID, &secretID, &targets); err != nil {
		return fmt.Errorf("policy %q not found", *policy)
	}
	var project string
	var version int
	if err := pool.QueryRow(ctx, `SELECT project_id, current_version FROM secrets WHERE id = $1`, secretID).Scan(&project, &version); err != nil {
		return err
	}
	value, err := sec.Provider.Get(ctx, pool, project, secretID, version)
	if err != nil {
		return err
	}
	if err := secrets.WriteAudit(ctx, pool, secrets.AuditEvent{OrgID: orgID, ActorType: "system", Action: "backup.password_read", TargetType: "backup_policy",
		TargetID: *policy, ProjectID: project, Diff: map[string]any{"via": "polipan-server admin backup-password"}}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "repository password: %s\n", value)
	for _, id := range targets {
		t, err := s.GetTarget(ctx, orgID, id)
		if err != nil {
			continue
		}
		spec, err := buildSpec(t, map[string]string{}, *policy)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(out, "target %s (%s): restic -r %s snapshots\n", t.Name, t.Kind, spec.Repository)
	}
	_, err = fmt.Fprintln(out, "Set RESTIC_PASSWORD to the password. The credentials of a remote target are the ones you gave when you added it.")
	return err
}
