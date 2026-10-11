// SPDX-License-Identifier: Apache-2.0

// Package alert is the alert engine (docs/spec/12-observability.md section 5): rules, instances, channels and the
// delivery of messages. Events and metrics make instances. A job per channel sends the messages.
package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// Errors the API maps to answers.
var (
	ErrNotFound = errors.New("alert: not found")
	ErrInvalid  = errors.New("alert: invalid")
	ErrConflict = errors.New("alert: conflict")
)

type invalidError struct{ Field, Msg string }

func (e *invalidError) Error() string { return e.Msg }
func (e *invalidError) Unwrap() error { return ErrInvalid }

// InvalidField returns the field of an invalid-input error, or "".
func InvalidField(err error) string {
	var ie *invalidError
	if errors.As(err, &ie) {
		return ie.Field
	}
	return ""
}

func invalid(field, format string, a ...any) error {
	return &invalidError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

type dbq interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Rule types (REQ-ALERT-002, plus failover_blocked and log_match).
var RuleTypes = map[string]bool{
	"node_suspect": true, "node_down": true, "service_unhealthy": true, "deployment_failed": true,
	"deployment_rolled_back": true, "deployment_live": true, "cpu_high": true, "memory_high": true, "disk_high": true,
	"cert_expiring": true, "cert_failed": true, "backup_failed": true, "restore_check_failed": true,
	"restore_check_overdue": true, "backup_missing": true, "audit_chain_broken": true, "failover_started": true,
	"failover_blocked": true, "container_restart_loop": true, "log_match": true,
}

var thresholdTypes = map[string]bool{"cpu_high": true, "memory_high": true, "disk_high": true}

// Channel is a place where messages go. The secret is never part of it.
type Channel struct {
	ID        string
	OrgID     string
	Kind      string
	Name      string
	Config    map[string]any
	SecretID  *string
	Enabled   bool
	State     string
	LastError string
	LastOKAt  *time.Time
}

// ChannelInput is a create or update of a channel. Secret is the bot token (telegram), the webhook URL (slack),
// the SMTP password (email) or the HMAC secret (webhook). Nil keeps the old one.
type ChannelInput struct {
	Kind    string
	Name    string
	Config  map[string]any
	Secret  *string
	Enabled *bool
}

// Rule is an alert rule.
type Rule struct {
	ID         string
	OrgID      string
	Name       string
	Type       string
	Scope      map[string]string
	Params     map[string]any
	Severity   string
	ChannelIDs []string
	RepeatS    int
	MutedUntil *time.Time
	MuteReason string
	Enabled    bool
	Builtin    bool
}

// RuleInput is a create or update of a rule.
type RuleInput struct {
	Name       string
	Type       string
	Scope      map[string]string
	Params     map[string]any
	Severity   string
	ChannelIDs []string
	RepeatS    int
	Enabled    *bool
}

// Instance is a firing or resolved alert.
type Instance struct {
	ID             string
	RuleID         string
	RuleName       string
	RuleType       string
	Severity       string
	Fingerprint    string
	State          string
	FirstFiredAt   time.Time
	LastNotifiedAt *time.Time
	ResolvedAt     *time.Time
	Muted          bool
	Suppressed     bool
	Details        map[string]any
}

// Store reads and writes the alert tables.
type Store struct {
	DB      *pgxpool.Pool
	Secrets *secrets.Manager
	// MinRepeatS is the smallest repeat interval the API accepts (default 60). Tests lower it.
	MinRepeatS int
}

func (s *Store) minRepeat() int {
	if s.MinRepeatS > 0 {
		return s.MinRepeatS
	}
	return 60
}

// ---- secrets: they live in the hidden system project of the org, like the secrets of backup targets ----

const systemSlug = "polipan-system"

func systemProject(ctx context.Context, q dbq, orgID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM projects WHERE org_id = $1 AND slug = $2`, orgID, systemSlug).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = store.NewID("prj")
	if _, err := q.Exec(ctx, `INSERT INTO projects (id, org_id, name, slug, settings) VALUES ($1,$2,'Polipan system',$3,'{"system": true}')
		ON CONFLICT (org_id, slug) DO NOTHING`, id, orgID, systemSlug); err != nil {
		return "", err
	}
	err = q.QueryRow(ctx, `SELECT id FROM projects WHERE org_id = $1 AND slug = $2`, orgID, systemSlug).Scan(&id)
	return id, err
}

func (s *Store) putSecret(ctx context.Context, tx pgx.Tx, orgID, name, value string) (string, error) {
	if s.Secrets == nil {
		return "", errors.New("the secrets store is not set up")
	}
	project, err := systemProject(ctx, tx, orgID)
	if err != nil {
		return "", err
	}
	id := store.NewID("sec")
	if _, err := tx.Exec(ctx, `INSERT INTO secrets (id, project_id, name, current_version) VALUES ($1,$2,$3,1)`, id, project, name); err != nil {
		return "", err
	}
	if err := s.Secrets.Provider.Put(ctx, tx, project, id, 1, []byte(value), ""); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) newSecretVersion(ctx context.Context, tx pgx.Tx, secretID, value string) error {
	var project string
	var version int
	if err := tx.QueryRow(ctx, `SELECT project_id, current_version + 1 FROM secrets WHERE id = $1 FOR UPDATE`, secretID).Scan(&project, &version); err != nil {
		return notFound(err)
	}
	if err := s.Secrets.Provider.Put(ctx, tx, project, secretID, version, []byte(value), ""); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE secrets SET current_version = $2 WHERE id = $1`, secretID, version)
	return err
}

// SecretValue reads the current value of a channel secret.
func (s *Store) SecretValue(ctx context.Context, secretID string) (string, error) {
	if s.Secrets == nil {
		return "", errors.New("the secrets store is not set up")
	}
	var project string
	var version int
	if err := s.DB.QueryRow(ctx, `SELECT project_id, current_version FROM secrets WHERE id = $1 AND deleted_at IS NULL`, secretID).Scan(&project, &version); err != nil {
		return "", notFound(err)
	}
	v, err := s.Secrets.Provider.Get(ctx, s.DB, project, secretID, version)
	return string(v), err
}

// ---- channels ----

const channelCols = `id, org_id, kind, name, config, secret_id, enabled, state, COALESCE(last_error,''), last_ok_at`

func scanChannel(row pgx.Row) (*Channel, error) {
	var c Channel
	var cfg []byte
	if err := row.Scan(&c.ID, &c.OrgID, &c.Kind, &c.Name, &cfg, &c.SecretID, &c.Enabled, &c.State, &c.LastError, &c.LastOKAt); err != nil {
		return nil, err
	}
	c.Config = map[string]any{}
	_ = json.Unmarshal(cfg, &c.Config)
	return &c, nil
}

// ListChannels lists the channels of an org.
func (s *Store) ListChannels(ctx context.Context, orgID string) ([]*Channel, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+channelCols+` FROM alert_channels WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel reads one channel of an org.
func (s *Store) GetChannel(ctx context.Context, orgID, id string) (*Channel, error) {
	c, err := scanChannel(s.DB.QueryRow(ctx, `SELECT `+channelCols+` FROM alert_channels WHERE id = $1 AND org_id = $2`, id, orgID))
	return c, notFound(err)
}

var nameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,62}$`)

func str(cfg map[string]any, key string) string {
	switch v := cfg[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%d", int64(v))
	}
	return ""
}

// cleanConfig checks the config of a channel kind and returns only the keys that kind knows.
func cleanConfig(kind string, in map[string]any, hasSecret bool) (map[string]any, error) {
	out := map[string]any{}
	switch kind {
	case "telegram":
		chat := str(in, "chat_id")
		if chat == "" {
			return nil, invalid("config.chat_id", "a Telegram channel needs the chat id")
		}
		out["chat_id"] = chat
	case "slack":
		if ch := str(in, "channel"); ch != "" {
			out["channel"] = ch
		}
	case "email":
		host := str(in, "host")
		if host == "" {
			return nil, invalid("config.host", "an email channel needs the SMTP host")
		}
		port := str(in, "port")
		if port == "" {
			port = "587"
		}
		from := str(in, "from")
		if _, err := mail.ParseAddress(from); err != nil {
			return nil, invalid("config.from", "the from address is not valid")
		}
		var rcpt []string
		switch v := in["recipients"].(type) {
		case []any:
			for _, x := range v {
				if a, ok := x.(string); ok {
					rcpt = append(rcpt, strings.TrimSpace(a))
				}
			}
		case []string:
			for _, a := range v {
				rcpt = append(rcpt, strings.TrimSpace(a))
			}
		case string:
			for _, a := range strings.Split(v, ",") {
				rcpt = append(rcpt, strings.TrimSpace(a))
			}
		}
		if len(rcpt) == 0 || len(rcpt) > 20 {
			return nil, invalid("config.recipients", "give 1 to 20 recipients")
		}
		for _, a := range rcpt {
			if _, err := mail.ParseAddress(a); err != nil {
				return nil, invalid("config.recipients", "%q is not an email address", a)
			}
		}
		tlsMode := str(in, "tls")
		if tlsMode == "" {
			tlsMode = "starttls"
		}
		if tlsMode != "starttls" && tlsMode != "tls" && tlsMode != "none" {
			return nil, invalid("config.tls", "tls is starttls, tls or none")
		}
		out["host"], out["port"], out["from"], out["recipients"], out["tls"] = host, port, from, rcpt, tlsMode
		if u := str(in, "username"); u != "" {
			out["username"] = u
		}
	case "webhook":
		raw := str(in, "url")
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, invalid("config.url", "the webhook URL must start with http:// or https://")
		}
		out["url"] = raw
	default:
		return nil, invalid("kind", "the kind is telegram, slack, email or webhook")
	}
	return out, nil
}

func checkSecret(kind, secret string) error {
	switch kind {
	case "telegram":
		if secret == "" {
			return invalid("secret", "a Telegram channel needs the bot token")
		}
	case "slack":
		u, err := url.Parse(secret)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			return invalid("secret", "the Slack incoming webhook URL is not valid")
		}
	}
	return nil
}

// CreateChannel adds a channel. The first channel of an org is also set on the default rules that have none.
func (s *Store) CreateChannel(ctx context.Context, orgID string, in ChannelInput) (*Channel, error) {
	if !nameRe.MatchString(in.Name) {
		return nil, invalid("name", "name must be 1 to 63 characters: letters, digits, space . _ -")
	}
	secret := ""
	if in.Secret != nil {
		secret = *in.Secret
	}
	cfg, err := cleanConfig(in.Kind, in.Config, secret != "")
	if err != nil {
		return nil, err
	}
	if err := checkSecret(in.Kind, secret); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var secretID *string
	if secret != "" {
		id, err := s.putSecret(ctx, tx, orgID, "alert-channel-"+in.Name+"-"+store.NewID("x"), secret)
		if err != nil {
			return nil, err
		}
		secretID = &id
	}
	id := store.NewID("chn")
	enabled := in.Enabled == nil || *in.Enabled
	raw, _ := json.Marshal(cfg)
	if _, err := tx.Exec(ctx, `INSERT INTO alert_channels (id, org_id, kind, name, config, secret_id, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, orgID, in.Kind, in.Name, raw, secretID, enabled); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a channel with this name exists", ErrConflict)
		}
		return nil, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_channels WHERE org_id = $1`, orgID).Scan(&count); err != nil {
		return nil, err
	}
	if count == 1 { // the first admin picked a channel: the default rules use it (REQ-ALERT-005)
		if _, err := tx.Exec(ctx, `UPDATE alert_rules SET channel_ids = ARRAY[$2]::text[] WHERE org_id = $1 AND builtin AND cardinality(channel_ids) = 0`, orgID, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, orgID, id)
}

// UpdateChannel changes a channel. The kind cannot change.
func (s *Store) UpdateChannel(ctx context.Context, orgID, id string, in ChannelInput) (*Channel, error) {
	cur, err := s.GetChannel(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if in.Kind != "" && in.Kind != cur.Kind {
		return nil, invalid("kind", "the kind of a channel cannot change")
	}
	name := cur.Name
	if in.Name != "" {
		if !nameRe.MatchString(in.Name) {
			return nil, invalid("name", "name must be 1 to 63 characters: letters, digits, space . _ -")
		}
		name = in.Name
	}
	cfg := cur.Config
	if in.Config != nil {
		if cfg, err = cleanConfig(cur.Kind, in.Config, true); err != nil {
			return nil, err
		}
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	secretID := cur.SecretID
	if in.Secret != nil && *in.Secret != "" {
		if err := checkSecret(cur.Kind, *in.Secret); err != nil {
			return nil, err
		}
		if secretID == nil {
			sid, err := s.putSecret(ctx, tx, orgID, "alert-channel-"+name+"-"+store.NewID("x"), *in.Secret)
			if err != nil {
				return nil, err
			}
			secretID = &sid
		} else if err := s.newSecretVersion(ctx, tx, *secretID, *in.Secret); err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(cfg)
	if _, err := tx.Exec(ctx, `UPDATE alert_channels SET name = $3, config = $4, secret_id = $5, enabled = $6 WHERE id = $1 AND org_id = $2`,
		id, orgID, name, raw, secretID, enabled); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a channel with this name exists", ErrConflict)
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetChannel(ctx, orgID, id)
}

// DeleteChannel removes a channel from the org and from the rules that use it.
func (s *Store) DeleteChannel(ctx context.Context, orgID, id string) error {
	c, err := s.GetChannel(ctx, orgID, id)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE alert_rules SET channel_ids = array_remove(channel_ids, $2) WHERE org_id = $1`, orgID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alert_channels WHERE id = $1 AND org_id = $2`, id, orgID); err != nil {
		return err
	}
	if c.SecretID != nil {
		if _, err := tx.Exec(ctx, `UPDATE secrets SET deleted_at = now() WHERE id = $1`, *c.SecretID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- rules ----

const ruleCols = `id, org_id, name, type, scope, params, severity, channel_ids, repeat_interval_s, muted_until, COALESCE(mute_reason,''), enabled, builtin`

func scanRule(row pgx.Row) (*Rule, error) {
	var r Rule
	var scope, params []byte
	if err := row.Scan(&r.ID, &r.OrgID, &r.Name, &r.Type, &scope, &params, &r.Severity, &r.ChannelIDs, &r.RepeatS, &r.MutedUntil, &r.MuteReason, &r.Enabled, &r.Builtin); err != nil {
		return nil, err
	}
	r.Scope, r.Params = map[string]string{}, map[string]any{}
	_ = json.Unmarshal(scope, &r.Scope)
	_ = json.Unmarshal(params, &r.Params)
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	return &r, nil
}

// ListRules lists the rules of an org.
func (s *Store) ListRules(ctx context.Context, orgID string) ([]*Rule, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE org_id = $1 ORDER BY builtin DESC, name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRule reads one rule of an org.
func (s *Store) GetRule(ctx context.Context, orgID, id string) (*Rule, error) {
	r, err := scanRule(s.DB.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id = $1 AND org_id = $2`, id, orgID))
	return r, notFound(err)
}

// cleanScope checks that the ids of a scope exist in the org and fit together.
func (s *Store) cleanScope(ctx context.Context, orgID string, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range []string{"project_id", "environment_id", "service_id", "node_id"} {
		v := strings.TrimSpace(in[k])
		if v == "" {
			continue
		}
		var q string
		switch k {
		case "project_id":
			q = `SELECT count(*) FROM projects WHERE id = $1 AND org_id = $2`
		case "environment_id":
			q = `SELECT count(*) FROM environments e JOIN projects p ON p.id = e.project_id WHERE e.id = $1 AND p.org_id = $2`
		case "service_id":
			q = `SELECT count(*) FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE s.id = $1 AND p.org_id = $2`
		case "node_id":
			q = `SELECT count(*) FROM nodes WHERE id = $1 AND org_id = $2`
		}
		var n int
		if err := s.DB.QueryRow(ctx, q, v, orgID).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, invalid("scope."+k, "%s does not exist in this org", k)
		}
		out[k] = v
	}
	for k := range in {
		if k != "project_id" && k != "environment_id" && k != "service_id" && k != "node_id" {
			return nil, invalid("scope."+k, "unknown scope field")
		}
	}
	return out, nil
}

func (s *Store) cleanChannels(ctx context.Context, orgID string, ids []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var n int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM alert_channels WHERE id = $1 AND org_id = $2`, id, orgID).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, invalid("channel_ids", "channel %s does not exist in this org", id)
		}
		out = append(out, id)
	}
	return out, nil
}

func cleanParams(typ string, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	num := func(k string) (float64, bool, error) {
		v, ok := in[k]
		if !ok || v == nil {
			return 0, false, nil
		}
		f, ok := v.(float64)
		if !ok {
			return 0, false, invalid("params."+k, "%s must be a number", k)
		}
		return f, true, nil
	}
	switch {
	case thresholdTypes[typ]:
		t, ok, err := num("threshold")
		if err != nil {
			return nil, err
		}
		if !ok {
			t = 90
		}
		if t <= 0 || t > 100 {
			return nil, invalid("params.threshold", "threshold is a percent from 1 to 100")
		}
		out["threshold"] = t
		f, ok, err := num("for_seconds")
		if err != nil {
			return nil, err
		}
		if !ok {
			f = 300
		}
		if f < 30 || f > 86400 {
			return nil, invalid("params.for_seconds", "for_seconds is from 30 to 86400")
		}
		out["for_seconds"] = f
	case typ == "cert_expiring":
		d, ok, err := num("days")
		if err != nil {
			return nil, err
		}
		if !ok {
			d = 14
		}
		if d < 1 || d > 90 {
			return nil, invalid("params.days", "days is from 1 to 90")
		}
		out["days"] = d
	case typ == "log_match":
		q, _ := in["query"].(string)
		if strings.TrimSpace(q) == "" || len(q) > 500 {
			return nil, invalid("params.query", "a log_match rule needs a search text of up to 500 characters")
		}
		out["query"] = q
		c, ok, err := num("count")
		if err != nil {
			return nil, err
		}
		if !ok {
			c = 1
		}
		if c < 1 {
			return nil, invalid("params.count", "count must be at least 1")
		}
		out["count"] = c
		f, ok, err := num("for_seconds")
		if err != nil {
			return nil, err
		}
		if !ok {
			f = 300
		}
		if f < 30 || f > 86400 {
			return nil, invalid("params.for_seconds", "for_seconds is from 30 to 86400")
		}
		out["for_seconds"] = f
	}
	return out, nil
}

func (s *Store) checkRule(ctx context.Context, orgID string, in *RuleInput) (map[string]string, []string, map[string]any, error) {
	if !nameRe.MatchString(in.Name) {
		return nil, nil, nil, invalid("name", "name must be 1 to 63 characters: letters, digits, space . _ -")
	}
	if !RuleTypes[in.Type] {
		return nil, nil, nil, invalid("type", "unknown rule type %q", in.Type)
	}
	if in.Severity != "info" && in.Severity != "warning" && in.Severity != "critical" {
		return nil, nil, nil, invalid("severity", "severity is info, warning or critical")
	}
	if in.RepeatS != 0 && in.RepeatS < s.minRepeat() {
		return nil, nil, nil, invalid("repeat_interval_seconds", "the repeat interval is at least %d seconds", s.minRepeat())
	}
	scope, err := s.cleanScope(ctx, orgID, in.Scope)
	if err != nil {
		return nil, nil, nil, err
	}
	chans, err := s.cleanChannels(ctx, orgID, in.ChannelIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	params, err := cleanParams(in.Type, in.Params)
	if err != nil {
		return nil, nil, nil, err
	}
	return scope, chans, params, nil
}

// CreateRule adds a rule.
func (s *Store) CreateRule(ctx context.Context, orgID string, in RuleInput, defaultRepeatS int) (*Rule, error) {
	scope, chans, params, err := s.checkRule(ctx, orgID, &in)
	if err != nil {
		return nil, err
	}
	if in.RepeatS == 0 {
		in.RepeatS = defaultRepeatS
	}
	id := store.NewID("alr")
	sc, _ := json.Marshal(scope)
	pr, _ := json.Marshal(params)
	enabled := in.Enabled == nil || *in.Enabled
	if _, err := s.DB.Exec(ctx, `INSERT INTO alert_rules (id, org_id, name, type, scope, params, severity, channel_ids, repeat_interval_s, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, orgID, in.Name, in.Type, sc, pr, in.Severity, chans, in.RepeatS, enabled); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a rule with this name exists", ErrConflict)
		}
		return nil, err
	}
	return s.GetRule(ctx, orgID, id)
}

// UpdateRule changes a rule. The type cannot change.
func (s *Store) UpdateRule(ctx context.Context, orgID, id string, in RuleInput) (*Rule, error) {
	cur, err := s.GetRule(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if in.Type != "" && in.Type != cur.Type {
		return nil, invalid("type", "the type of a rule cannot change")
	}
	in.Type = cur.Type
	if in.Name == "" {
		in.Name = cur.Name
	}
	if in.Severity == "" {
		in.Severity = cur.Severity
	}
	if in.Scope == nil {
		in.Scope = cur.Scope
	}
	if in.Params == nil {
		in.Params = cur.Params
	}
	if in.ChannelIDs == nil {
		in.ChannelIDs = cur.ChannelIDs
	}
	if in.RepeatS == 0 {
		in.RepeatS = cur.RepeatS
	}
	scope, chans, params, err := s.checkRule(ctx, orgID, &in)
	if err != nil {
		return nil, err
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	sc, _ := json.Marshal(scope)
	pr, _ := json.Marshal(params)
	if _, err := s.DB.Exec(ctx, `UPDATE alert_rules SET name = $3, scope = $4, params = $5, severity = $6, channel_ids = $7, repeat_interval_s = $8, enabled = $9
		WHERE id = $1 AND org_id = $2`, id, orgID, in.Name, sc, pr, in.Severity, chans, in.RepeatS, enabled); err != nil {
		if u, _ := store.IsUnique(err); u {
			return nil, fmt.Errorf("%w: a rule with this name exists", ErrConflict)
		}
		return nil, err
	}
	return s.GetRule(ctx, orgID, id)
}

// DeleteRule removes a rule and its instances.
func (s *Store) DeleteRule(ctx context.Context, orgID, id string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MuteRule mutes a rule until a time. A time that is not in the future clears the mute.
func (s *Store) MuteRule(ctx context.Context, orgID, id string, until time.Time, reason string, now time.Time) (*Rule, error) {
	if until.After(now.Add(366 * 24 * time.Hour)) {
		return nil, invalid("until", "a mute is at most one year")
	}
	if until.After(now) && strings.TrimSpace(reason) == "" {
		return nil, invalid("reason", "a mute needs a reason")
	}
	var mu *time.Time
	if until.After(now) {
		mu = &until
	} else {
		reason = ""
	}
	tag, err := s.DB.Exec(ctx, `UPDATE alert_rules SET muted_until = $3, mute_reason = NULLIF($4,'') WHERE id = $1 AND org_id = $2`, id, orgID, mu, reason)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetRule(ctx, orgID, id)
}

// ---- instances ----

// ListInstances lists the alerts of an org, newest first. state is "", firing or resolved. Cursor is the id of the
// last item of the page before.
func (s *Store) ListInstances(ctx context.Context, orgID, state string, limit int, cursor string, now time.Time) ([]*Instance, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{orgID, now, limit + 1}
	q := `SELECT i.id, i.rule_id, r.name, r.type, r.severity, i.fingerprint, i.state, i.first_fired_at, i.last_notified_at, i.resolved_at,
		(r.muted_until IS NOT NULL AND r.muted_until > $2), i.suppressed_by IS NOT NULL, COALESCE(i.details,'{}'::jsonb), i.updated_at, i.id
		FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE r.org_id = $1`
	if state != "" {
		args = append(args, state)
		q += fmt.Sprintf(` AND i.state = $%d`, len(args))
	}
	if cursor != "" {
		var at time.Time
		if err := s.DB.QueryRow(ctx, `SELECT i.updated_at FROM alert_instances i JOIN alert_rules r ON r.id = i.rule_id WHERE i.id = $1 AND r.org_id = $2`, cursor, orgID).Scan(&at); err == nil {
			args = append(args, at, cursor)
			q += fmt.Sprintf(` AND (i.updated_at, i.id) < ($%d, $%d)`, len(args)-1, len(args))
		}
	}
	q += ` ORDER BY i.updated_at DESC, i.id DESC LIMIT $3`
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		var in Instance
		var det []byte
		var upd time.Time
		var dup string
		if err := rows.Scan(&in.ID, &in.RuleID, &in.RuleName, &in.RuleType, &in.Severity, &in.Fingerprint, &in.State, &in.FirstFiredAt,
			&in.LastNotifiedAt, &in.ResolvedAt, &in.Muted, &in.Suppressed, &det, &upd, &dup); err != nil {
			return nil, "", err
		}
		in.Details = map[string]any{}
		_ = json.Unmarshal(det, &in.Details)
		out = append(out, &in)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
