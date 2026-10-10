// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/store"
)

// Target is a backup storage target (REQ-BKP-002). The credentials are not part of it: they are a secret.
type Target struct {
	ID                  string
	OrgID               string
	Name                string
	Kind                string
	Config              map[string]any
	CredentialsSecretID string
	LastTestAt          *time.Time
	LastTestOK          *bool
}

// TargetInput is the data of a new or changed target.
type TargetInput struct {
	Name        string
	Kind        string
	Config      map[string]any
	Credentials map[string]string // write-only. nil on an update keeps the old ones.
}

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`)
	bucketRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,62}$`)
	pathRe   = regexp.MustCompile(`^[A-Za-z0-9._/@+=-]{0,200}$`)
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]{0,251}[A-Za-z0-9])?$`)
	userRe   = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`)
	regionRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,40}$`)
)

// credentialKeys are the keys of the credentials of each kind.
var credentialKeys = map[string][]string{
	"s3":    {"access_key_id", "secret_access_key"},
	"b2":    {"account_id", "account_key"},
	"sftp":  {"private_key"},
	"local": {},
	"azure": {"account_name", "account_key"},
	"gcs":   {"credentials_json"},
}

var configKeys = map[string][]string{
	"s3":    {"endpoint", "bucket", "path", "region"},
	"b2":    {"bucket", "path"},
	"sftp":  {"host", "port", "user", "path", "known_host"},
	"local": {"path"},
	"azure": {"container", "path"},
	"gcs":   {"bucket", "path", "project_id"},
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprint(int(v))
	case int:
		return fmt.Sprint(v)
	}
	return ""
}

// checkHostAllowed refuses the hosts that give out cloud credentials or reach the node itself by accident.
func checkHostAllowed(host string) error {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "metadata.google.internal" || h == "metadata" {
		return errors.New("this host is not allowed")
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		if ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("this address is not allowed")
		}
	}
	return nil
}

// validateTarget checks the config and the credentials of a target. Credentials are checked when given.
func (s *Service) validateTarget(kind string, cfg map[string]any, creds map[string]string, needCreds bool) (map[string]any, error) {
	allowed, ok := configKeys[kind]
	if !ok {
		return nil, invalid("kind", "kind must be s3, b2, sftp, local, azure or gcs")
	}
	clean := map[string]any{}
	for k := range cfg {
		found := false
		for _, a := range allowed {
			if a == k {
				found = true
			}
		}
		if !found {
			return nil, invalid("config."+k, "config.%s is not used by a %s target", k, kind)
		}
	}
	need := func(k string) (string, error) {
		v := str(cfg, k)
		if v == "" {
			return "", invalid("config."+k, "config.%s is required", k)
		}
		return v, nil
	}
	setPath := func(base bool) error {
		p := str(cfg, "path")
		if strings.Contains(p, "..") || !pathRe.MatchString(p) {
			return invalid("config.path", "config.path may have letters, digits and . _ / @ + = - only, and no ..")
		}
		clean["path"] = strings.Trim(p, "/")
		_ = base
		return nil
	}
	switch kind {
	case "s3":
		b, err := need("bucket")
		if err != nil {
			return nil, err
		}
		if !bucketRe.MatchString(b) {
			return nil, invalid("config.bucket", "config.bucket is not a valid bucket name")
		}
		clean["bucket"] = b
		if ep := str(cfg, "endpoint"); ep != "" {
			if !strings.Contains(ep, "://") {
				ep = "https://" + ep
			}
			u, err := url.Parse(ep)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || (u.Path != "" && u.Path != "/") {
				return nil, invalid("config.endpoint", "config.endpoint must be a host or an http(s) address without a path")
			}
			if err := checkHostAllowed(u.Hostname()); err != nil {
				return nil, invalid("config.endpoint", "config.endpoint: %v", err)
			}
			clean["endpoint"] = u.Scheme + "://" + u.Host
		}
		if r := str(cfg, "region"); r != "" {
			if !regionRe.MatchString(r) {
				return nil, invalid("config.region", "config.region is not valid")
			}
			clean["region"] = r
		}
		if err := setPath(false); err != nil {
			return nil, err
		}
	case "b2":
		b, err := need("bucket")
		if err != nil {
			return nil, err
		}
		if !bucketRe.MatchString(b) {
			return nil, invalid("config.bucket", "config.bucket is not a valid bucket name")
		}
		clean["bucket"] = b
		if err := setPath(false); err != nil {
			return nil, err
		}
	case "azure":
		c, err := need("container")
		if err != nil {
			return nil, err
		}
		if !bucketRe.MatchString(c) {
			return nil, invalid("config.container", "config.container is not a valid container name")
		}
		clean["container"] = c
		if err := setPath(false); err != nil {
			return nil, err
		}
	case "gcs":
		b, err := need("bucket")
		if err != nil {
			return nil, err
		}
		if !bucketRe.MatchString(b) {
			return nil, invalid("config.bucket", "config.bucket is not a valid bucket name")
		}
		clean["bucket"] = b
		if p := str(cfg, "project_id"); p != "" {
			if !regexp.MustCompile(`^[a-z][a-z0-9-]{4,60}$`).MatchString(p) {
				return nil, invalid("config.project_id", "config.project_id is not valid")
			}
			clean["project_id"] = p
		}
		if err := setPath(false); err != nil {
			return nil, err
		}
	case "sftp":
		host, err := need("host")
		if err != nil {
			return nil, err
		}
		if !hostRe.MatchString(host) {
			return nil, invalid("config.host", "config.host is not a valid host name")
		}
		if err := checkHostAllowed(host); err != nil {
			return nil, invalid("config.host", "config.host: %v", err)
		}
		user, err := need("user")
		if err != nil {
			return nil, err
		}
		if !userRe.MatchString(user) {
			return nil, invalid("config.user", "config.user is not valid")
		}
		clean["host"], clean["user"] = host, user
		port := 22
		if p := str(cfg, "port"); p != "" {
			if _, err := fmt.Sscanf(p, "%d", &port); err != nil || port < 1 || port > 65535 {
				return nil, invalid("config.port", "config.port must be from 1 to 65535")
			}
		}
		clean["port"] = port
		p := str(cfg, "path")
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "..") || !pathRe.MatchString(p) {
			return nil, invalid("config.path", "config.path must be an absolute path on the server")
		}
		clean["path"] = path.Clean(p)
		if kh := str(cfg, "known_host"); kh != "" {
			if strings.ContainsAny(kh, "\n\r") || len(kh) > 1000 {
				return nil, invalid("config.known_host", "config.known_host must be one known_hosts line")
			}
			clean["known_host"] = kh
		}
	case "local":
		p := str(cfg, "path")
		root := s.LocalRoot
		clean["path"] = path.Clean(p)
		if p == "" || !strings.HasPrefix(p, root+"/") || strings.Contains(p, "..") || !pathRe.MatchString(p) {
			return nil, invalid("config.path", "config.path must be a folder inside %s on the node", root)
		}
	}
	if creds != nil || needCreds {
		for _, k := range credentialKeys[kind] {
			if strings.TrimSpace(creds[k]) == "" {
				return nil, invalid("credentials."+k, "credentials.%s is required for a %s target", k, kind)
			}
		}
		for k := range creds {
			ok := false
			for _, a := range credentialKeys[kind] {
				ok = ok || a == k
			}
			if !ok {
				return nil, invalid("credentials."+k, "credentials.%s is not used by a %s target", k, kind)
			}
		}
	}
	return clean, nil
}

const targetCols = `id, org_id, name, kind, config, credentials_secret_id, last_test_at, last_test_ok`

func scanTarget(row pgx.Row) (*Target, error) {
	var t Target
	var raw []byte
	if err := row.Scan(&t.ID, &t.OrgID, &t.Name, &t.Kind, &raw, &t.CredentialsSecretID, &t.LastTestAt, &t.LastTestOK); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(raw, &t.Config)
	return &t, nil
}

// ListTargets lists the targets of the org.
func (s *Service) ListTargets(ctx context.Context, orgID string) ([]Target, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// GetTarget reads a target of the org.
func (s *Service) GetTarget(ctx context.Context, orgID, id string) (*Target, error) {
	return scanTarget(s.DB.QueryRow(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE id = $1 AND org_id = $2`, id, orgID))
}

func credsJSON(c map[string]string) string {
	b, _ := json.Marshal(c)
	return string(b)
}

// CreateTarget adds a target. The credentials become a secret.
func (s *Service) CreateTarget(ctx context.Context, orgID string, in TargetInput) (*Target, error) {
	if !nameRe.MatchString(in.Name) {
		return nil, invalid("name", "name must be 1 to 63 characters: letters, digits, space . _ -")
	}
	cfg, err := s.validateTarget(in.Kind, in.Config, in.Credentials, true)
	if err != nil {
		return nil, err
	}
	creds := in.Credentials
	if creds == nil {
		creds = map[string]string{}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	id := store.NewID("bkp")
	secretID, err := s.putSecret(ctx, tx, orgID, "backup-target-"+id, credsJSON(creds))
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(cfg)
	if _, err := tx.Exec(ctx, `INSERT INTO backup_targets (id, org_id, name, kind, config, credentials_secret_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, orgID, in.Name, in.Kind, raw, secretID); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a target with this name exists", ErrConflict)
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetTarget(ctx, orgID, id)
}

// UpdateTarget changes a target. The kind cannot change. Credentials are replaced when given.
func (s *Service) UpdateTarget(ctx context.Context, orgID, id string, in TargetInput) (*Target, error) {
	cur, err := s.GetTarget(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if in.Kind != "" && in.Kind != cur.Kind {
		return nil, invalid("kind", "the kind of a target cannot change")
	}
	name := cur.Name
	if in.Name != "" {
		if !nameRe.MatchString(in.Name) {
			return nil, invalid("name", "name must be 1 to 63 characters: letters, digits, space . _ -")
		}
		name = in.Name
	}
	cfgIn := in.Config
	if cfgIn == nil {
		cfgIn = cur.Config
	}
	cfg, err := s.validateTarget(cur.Kind, cfgIn, in.Credentials, false)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if in.Credentials != nil {
		if err := s.newSecretVersion(ctx, tx, cur.CredentialsSecretID, credsJSON(in.Credentials)); err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(cfg)
	if _, err := tx.Exec(ctx, `UPDATE backup_targets SET name = $2, config = $3, last_test_at = NULL, last_test_ok = NULL WHERE id = $1`, id, name, raw); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a target with this name exists", ErrConflict)
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetTarget(ctx, orgID, id)
}

// DeleteTarget removes a target that no policy uses (409 otherwise).
func (s *Service) DeleteTarget(ctx context.Context, orgID, id string) error {
	t, err := s.GetTarget(ctx, orgID, id)
	if err != nil {
		return err
	}
	var used bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_policies WHERE $1 = ANY(target_ids)) OR EXISTS (SELECT 1 FROM restore_checks WHERE target_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrTargetInUse
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM backup_targets WHERE id = $1`, id); err != nil {
		return err
	}
	if err := s.deleteSecret(ctx, tx, t.CredentialsSecretID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// credentials reads the credentials of a target.
func (s *Service) credentials(ctx context.Context, t *Target) (map[string]string, error) {
	v, err := s.secretValue(ctx, t.CredentialsSecretID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// spec makes the target of an agent command: the restic repository of the policy, and the credentials as environment.
func (s *Service) spec(ctx context.Context, t *Target, policyID string) (*pb.BackupTargetSpec, error) {
	creds, err := s.credentials(ctx, t)
	if err != nil {
		return nil, err
	}
	return buildSpec(t, creds, policyID)
}

// buildSpec turns a target into the repository string. Every policy has its own repository inside the target.
func buildSpec(t *Target, creds map[string]string, policyID string) (*pb.BackupTargetSpec, error) {
	sp := &pb.BackupTargetSpec{TargetId: t.ID, Kind: t.Kind, Env: map[string]string{}}
	cfg := t.Config
	sub := func(base string) string {
		base = strings.Trim(base, "/")
		if base == "" {
			return policyID
		}
		return base + "/" + policyID
	}
	switch t.Kind {
	case "s3":
		ep := str(cfg, "endpoint")
		if ep == "" {
			ep = "s3.amazonaws.com"
		}
		ep = strings.TrimSuffix(ep, "/")
		sp.Repository = "s3:" + ep + "/" + str(cfg, "bucket") + "/" + sub(str(cfg, "path"))
		sp.Env["AWS_ACCESS_KEY_ID"] = creds["access_key_id"]
		sp.Env["AWS_SECRET_ACCESS_KEY"] = creds["secret_access_key"]
		if r := str(cfg, "region"); r != "" {
			sp.Env["AWS_DEFAULT_REGION"] = r
		}
	case "b2":
		sp.Repository = "b2:" + str(cfg, "bucket") + ":" + sub(str(cfg, "path"))
		sp.Env["B2_ACCOUNT_ID"] = creds["account_id"]
		sp.Env["B2_ACCOUNT_KEY"] = creds["account_key"]
	case "azure":
		sp.Repository = "azure:" + str(cfg, "container") + ":/" + sub(str(cfg, "path"))
		sp.Env["AZURE_ACCOUNT_NAME"] = creds["account_name"]
		sp.Env["AZURE_ACCOUNT_KEY"] = creds["account_key"]
	case "gcs":
		sp.Repository = "gs:" + str(cfg, "bucket") + ":/" + sub(str(cfg, "path"))
		sp.Env["GOOGLE_APPLICATION_CREDENTIALS_JSON"] = creds["credentials_json"]
		if p := str(cfg, "project_id"); p != "" {
			sp.Env["GOOGLE_PROJECT_ID"] = p
		}
	case "sftp":
		sp.Repository = fmt.Sprintf("sftp://%s@%s:%s%s/%s", str(cfg, "user"), str(cfg, "host"), str(cfg, "port"), str(cfg, "path"), policyID)
		sp.SshKey = []byte(creds["private_key"])
		sp.SshKnownHost = str(cfg, "known_host")
	case "local":
		sp.Repository = strings.TrimRight(str(cfg, "path"), "/") + "/" + policyID
	default:
		return nil, fmt.Errorf("unknown target kind %q", t.Kind)
	}
	return sp, nil
}

// TestTarget asks a node to write and read a small test object on the target (REQ-BKP-002).
func (s *Service) TestTarget(ctx context.Context, orgID, id string) (ok bool, message string, err error) {
	t, err := s.GetTarget(ctx, orgID, id)
	if err != nil {
		return false, "", err
	}
	node, err := s.anyNode(ctx, orgID, "")
	if err != nil {
		return false, "", err
	}
	sp, err := s.spec(ctx, t, "test")
	if err != nil {
		return false, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ack, err := s.Sender.Send(cctx, node, &pb.Command{CommandId: gateway.NewID("cmd"),
		Body: &pb.Command_TestBackupTarget{TestBackupTarget: &pb.TestBackupTarget{Target: sp, RepoPassword: "polipan-target-test"}}})
	okTest := err == nil && ack.GetOk()
	msg := ""
	switch {
	case err != nil:
		msg = "node unavailable: " + err.Error()
	case !ack.GetOk():
		msg = ack.GetErrorMessage()
	}
	now := s.now()
	if _, e := s.DB.Exec(ctx, `UPDATE backup_targets SET last_test_at = $2, last_test_ok = $3 WHERE id = $1`, id, now, okTest); e != nil {
		return false, "", e
	}
	return okTest, msg, nil
}

// anyNode returns a ready node with an open stream. A node in prefer comes first.
func (s *Service) anyNode(ctx context.Context, orgID, prefer string) (string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id FROM nodes WHERE org_id = $1 AND state = 'ready' ORDER BY (id = $2) DESC, created_at, id`, orgID, prefer)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		if s.Sender != nil && s.Sender.Connected(id) {
			return id, nil
		}
	}
	return "", ErrNoNode
}
