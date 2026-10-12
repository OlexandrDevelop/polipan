// SPDX-License-Identifier: Apache-2.0

// Package admin has the "polipan-server admin" commands for users. They run on the control-plane host
// and write to the audit log as the system (REQ-AUTH-007).
package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/store"
)

// Help is the text of "polipan-server admin help".
const Help = `Usage: polipan-server [-config file] admin <command>

Commands run on the control-plane host only. They use the server config and the database, not the API.
The commands that change users or tokens write an audit event as the system ("polipan-server admin"). None of them changes the owner's password.

  rotate-kek                     make a new master key and re-wrap all data keys
  rotate-dek --project <id>      re-encrypt the secrets of a project
  backup-password --policy <id>  print the restic password and the repositories of a policy
  reset-password <email>         print a one-hour, one-use link to set a new password for that user
  disable-2fa <email>            turn two-factor login off for a user
  audit-verify                   check the audit hash chain
  invite-user <email> [--role viewer|developer|admin]
                                 print a one-hour, one-use invite link for a new user (default role viewer;
                                 owner is not allowed). The user sets an own password on the link.
  create-token <email> (--project <id or slug> | --all-projects) [--scope read|deploy|write]
               [--expires 24h|30d] [--name <name>]
                                 print a new API token of an existing user once (default scope deploy,
                                 default life 24h, at most 90d; admin scope is not allowed). Only its hash
                                 is stored. The token never has more rights than the user.
  revoke-token <token id>        revoke an API token
  remove-user <email>            remove a member who is not an owner; sessions and tokens end at once
`

func openStore(ctx context.Context, cfg config.Config) (*store.Store, func(), error) {
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	return store.New(pool), pool.Close, nil
}

func oneEmail(args []string) (string, error) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return "", errors.New("give one email address")
	}
	return store.NormalizeEmail(args[0]), nil
}

// ResetPassword is "polipan-server admin reset-password <email>". It makes a reset link that works for
// one hour and prints it. The link is the same as the one an email would carry.
func ResetPassword(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	email, err := oneEmail(args)
	if err != nil {
		return err
	}
	st, closeFn, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	user, err := st.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no user with the email %q", email)
	}
	if err != nil {
		return err
	}
	token, err := auth.NewOneTimeToken()
	if err != nil {
		return err
	}
	if err := st.CreatePasswordReset(ctx, user.ID, auth.HashToken(token), time.Now()); err != nil {
		return err
	}
	if err := writeEvent(ctx, st, user, "admin.reset_password"); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Open this link within one hour to set a new password:\n%s/reset-password?token=%s\n",
		strings.TrimRight(cfg.ExternalURL, "/"), token)
	return err
}

// DisableTOTP is "polipan-server admin disable-2fa <email>". It turns two-factor login off for a user who lost
// the device and the recovery codes.
func DisableTOTP(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	email, err := oneEmail(args)
	if err != nil {
		return err
	}
	st, closeFn, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	user, err := st.UserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no user with the email %q", email)
	}
	if err != nil {
		return err
	}
	if err := st.DisableTOTP(audit.WithRequest(ctx, systemRequest()), user.OrgID, user.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Two-factor login is off for %s. The user can set it up again after login.\n", user.Email)
	return err
}

// AuditVerify is "polipan-server admin audit-verify". It walks the hash chain of every org and prints the first
// broken sequence. It returns an error when a chain is broken, so scripts can check the exit code.
func AuditVerify(ctx context.Context, cfg config.Config, out io.Writer) error {
	st, closeFn, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	orgs, err := audit.OrgIDs(ctx, st.Pool)
	if err != nil {
		return err
	}
	broken := false
	for _, org := range orgs {
		res, err := audit.Verify(ctx, st.Pool, org)
		if err != nil {
			return err
		}
		if res.OK {
			_, _ = fmt.Fprintf(out, "org %s: ok, %d rows checked\n", org, res.CheckedRows)
			continue
		}
		broken = true
		_, _ = fmt.Fprintf(out, "org %s: BROKEN at seq %d\n", org, res.FirstBrokenSeq)
	}
	if broken {
		return errors.New("the audit chain is broken")
	}
	return nil
}

func systemRequest() *audit.Request {
	return &audit.Request{ActorType: audit.ActorSystem, ActorID: "polipan-server admin"}
}

func writeEvent(ctx context.Context, st *store.Store, user *store.User, action string) error {
	return audit.Write(audit.WithRequest(ctx, systemRequest()), st.Pool, audit.Event{OrgID: user.OrgID, Action: action, TargetType: "user", TargetID: user.ID})
}
