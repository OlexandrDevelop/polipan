// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/audit"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/store"
)

// Limits of the host-admin provisioning commands (10-security, section 1).
const (
	// AdminInviteTTL is the life of an invite link made on the host. It is shorter than the 7 days of a web invite.
	AdminInviteTTL = time.Hour
	// AdminTokenMaxTTL is the longest life of a host-made token.
	AdminTokenMaxTTL = 90 * 24 * time.Hour
)

// splitArgs takes the one email from args. The email may come before or after the flags.
func splitArgs(fset *flag.FlagSet, args []string) (string, error) {
	fset.SetOutput(io.Discard)
	var rest []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		rest, args = args[:1], args[1:]
	}
	if err := fset.Parse(args); err != nil {
		return "", err
	}
	rest = append(rest, fset.Args()...)
	return oneEmail(rest)
}

// InviteUser is "polipan-server admin invite-user <email> [--role viewer|developer|admin]". It makes an invite
// link that works once and for one hour, and prints it. The new user sets an own password on the invite page.
// The owner role is not allowed here, and no existing user or password is changed.
func InviteUser(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	fset := flag.NewFlagSet("invite-user", flag.ContinueOnError)
	role := fset.String("role", auth.RoleViewer, "org role of the new user: viewer, developer or admin")
	email, err := splitArgs(fset, args)
	if err != nil {
		return err
	}
	if !validEmail(email) {
		return fmt.Errorf("%q is not a valid email", email)
	}
	switch *role {
	case auth.RoleViewer, auth.RoleDeveloper, auth.RoleAdmin:
	case auth.RoleOwner:
		return errors.New("the owner role cannot be given here; an owner can promote the user in the web app")
	default:
		return errors.New("role must be viewer, developer or admin")
	}
	st, closeFn, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	orgID, err := ownedOrg(ctx, st)
	if err != nil {
		return err
	}
	token, err := auth.NewOneTimeToken()
	if err != nil {
		return err
	}
	inv, err := st.CreateInvitationTTL(audit.WithRequest(ctx, systemRequest()), orgID, email, *role, "", auth.HashToken(token), time.Now(), AdminInviteTTL)
	if errors.Is(err, store.ErrAlreadyMember) {
		return fmt.Errorf("%s is already a member; use reset-password to give a new password link", email)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Invitation %s for %s (role %s). Open this link once, before %s:\n%s/invite?token=%s\n",
		inv.ID, email, *role, inv.ExpiresAt.UTC().Format(time.RFC3339), strings.TrimRight(cfg.ExternalURL, "/"), token)
	return err
}

// CreateToken is "polipan-server admin create-token <email> (--project <id or slug> | --all-projects)
// [--scope read|deploy|write] [--expires 24h|7d] [--name <name>]". It makes an API token of an existing user
// and prints it once. Only the hash is stored. The token never has more rights than the user.
func CreateToken(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	fset := flag.NewFlagSet("create-token", flag.ContinueOnError)
	project := fset.String("project", "", "id or slug of the one project the token may use")
	allProjects := fset.Bool("all-projects", false, "let the token use every project the user can use")
	scope := fset.String("scope", auth.ScopeDeploy, "token scope: read, deploy or write")
	expires := fset.String("expires", "24h", "token life, for example 1h, 24h or 30d (at most 90d)")
	name := fset.String("name", "", "token name (default host-admin-<date>)")
	email, err := splitArgs(fset, args)
	if err != nil {
		return err
	}
	switch *scope {
	case auth.ScopeRead, auth.ScopeDeploy, auth.ScopeWrite:
	case auth.ScopeAdmin:
		return errors.New("the admin scope cannot be given here; make it in the web app")
	default:
		return errors.New("scope must be read, deploy or write")
	}
	if (*project == "") == !*allProjects {
		return errors.New("give --project <id or slug>, or --all-projects")
	}
	ttl, err := parseTTL(*expires)
	if err != nil {
		return err
	}
	tokenName := strings.TrimSpace(*name)
	if tokenName == "" {
		tokenName = "host-admin-" + time.Now().UTC().Format("20060102-1504")
	}
	if len(tokenName) > 100 {
		return errors.New("name must have 1 to 100 characters")
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
	if user.Disabled {
		return fmt.Errorf("the user %s is disabled", email)
	}
	projectID := ""
	if *project != "" {
		err := st.Pool.QueryRow(ctx, `SELECT id FROM projects WHERE (id = $1 OR slug = $1) AND org_id = $2 AND deleted_at IS NULL
			ORDER BY created_at LIMIT 1`, *project, user.OrgID).Scan(&projectID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no project %q", *project)
		}
		if err != nil {
			return err
		}
	}
	value, err := auth.NewAPIToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(ttl)
	t, err := st.CreateToken(audit.WithRequest(ctx, systemRequest()), user.OrgID, user.ID, tokenName, auth.HashToken(value), value[:8],
		[]string{*scope}, projectID, &expiresAt)
	if err != nil {
		return err
	}
	limit := "all projects of the user"
	if projectID != "" {
		limit = "project " + projectID
	}
	_, err = fmt.Fprintf(out, "Token %s for %s: scope %s, %s, expires %s.\nIt is shown only now. Store it in a safe place:\n%s\nRevoke it with: polipan-server admin revoke-token %s\n",
		t.ID, user.Email, *scope, limit, expiresAt.UTC().Format(time.RFC3339), value, t.ID)
	return err
}

// RevokeToken is "polipan-server admin revoke-token <token id>". It ends one API token of any user.
func RevokeToken(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("give one token id")
	}
	st, closeFn, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeFn()
	var orgID string
	err = st.Pool.QueryRow(ctx, `SELECT org_id FROM api_tokens WHERE id = $1`, args[0]).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no token %q", args[0])
	}
	if err != nil {
		return err
	}
	if err := st.RevokeToken(audit.WithRequest(ctx, systemRequest()), orgID, "", args[0]); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Token %s is revoked.\n", args[0])
	return err
}

// RemoveUser is "polipan-server admin remove-user <email>". It removes a member who is not an owner: sessions
// and tokens end at once. The user row stays (soft deleted) for the audit log.
func RemoveUser(ctx context.Context, cfg config.Config, args []string, out io.Writer) error {
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
	// The command acts as an admin, so the store refuses to remove an owner.
	err = st.RemoveMember(audit.WithRequest(ctx, systemRequest()), user.OrgID, auth.RoleAdmin, user.ID)
	if errors.Is(err, store.ErrOwnerProtect) {
		return errors.New("an owner cannot be removed here")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "User %s is removed. Sessions and tokens are ended.\n", user.Email)
	return err
}

// ownedOrg returns the single org, and fails when the first owner is not made yet (then setup-url is the way).
func ownedOrg(ctx context.Context, st *store.Store) (string, error) {
	orgID, err := st.OnlyOrgID(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("there is no org yet; make the first owner with polipan-server setup-url")
	}
	if err != nil {
		return "", err
	}
	var owners int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.role = 'owner' AND u.deleted_at IS NULL`, orgID).Scan(&owners); err != nil {
		return "", err
	}
	if owners == 0 {
		return "", errors.New("there is no owner yet; make the first owner with polipan-server setup-url")
	}
	return orgID, nil
}

// parseTTL reads a Go duration, or a number of days like "30d". It must be from one minute to 90 days.
func parseTTL(s string) (time.Duration, error) {
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 0 || days > 90 {
			return 0, fmt.Errorf("bad --expires %q", s)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("bad --expires %q", s)
		}
	}
	if d < time.Minute || d > AdminTokenMaxTTL {
		return 0, errors.New("--expires must be from 1m to 90d")
	}
	return d, nil
}

func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at == strings.LastIndexByte(s, '@') && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n<>,;") && len(s) <= 254
}
