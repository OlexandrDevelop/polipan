// SPDX-License-Identifier: Apache-2.0

// Package promote plans and runs the promotion of Releases from one environment to another (F04, REQ-PROMO-001
// to REQ-PROMO-010). It makes a new Release in the target service: the same image digest and the same config,
// except the fields that belong to the target environment. The variables and secrets are resolved in the scope of
// the target. No build runs. The deploy engine runs the new Release with the strategy of the target.
package promote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/vars"
)

// Errors of the planning. The API maps them to answers.
var (
	ErrNotFound           = errors.New("promote: not found")
	ErrSameEnvironment    = errors.New("promote: the source and the target are the same environment")
	ErrOtherProject       = errors.New("promote: the target is in another project") // REQ-PROMO-009
	ErrPreviewTarget      = errors.New("promote: a preview environment cannot be a promotion target")
	ErrDomainPortMismatch = errors.New("promote: a domain of the target points to a port the release does not expose")
)

// NotPromotableError is 409 release_not_promotable (REQ-PROMO-010).
type NotPromotableError struct{ Reason string }

func (e *NotPromotableError) Error() string { return "the release cannot be promoted: " + e.Reason }

// MissingError is 422 missing_variables: the names the source Release used that the target does not have (REQ-PROMO-003).
type MissingError struct{ Names []string }

func (e *MissingError) Error() string { return "missing variables: " + strings.Join(e.Names, ", ") }

// Options of one promotion.
type Options struct {
	TargetEnvID      string
	AllowMissing     []string
	CreateMissing    bool
	IncludeDatabases bool
	// Strategy asks for a strategy. Empty means the one of the target.
	Strategy string
	// AllowPreview lets a preview environment be the target. Only the preview service uses it, to run the live Release
	// of a base service in a preview (REQ-PREV-003). The API never sets it (REQ-PROMO-009).
	AllowPreview bool
	// KeepMissing is for a preview: a name that the base environment uses and the preview does not have is not an
	// error. A reference that does not resolve still fails the deployment, with the names (REQ-PREV-010).
	KeepMissing bool
}

// Change is one field that differs between the target now and the new Release.
type Change struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Item is the plan for one service.
type Item struct {
	Service             string
	SourceServiceID     string
	SourceReleaseID     string
	SourceReleaseNumber int
	TargetServiceID     string // empty when the service is created
	TargetReleaseID     string // the live Release of the target now
	ImageDigest         string
	Changes             []Change
	// Missing lists the names that are not in the target and not allowed. Allowed lists the missing names that were allowed.
	Missing, Allowed []string
	// VariablesAdded and VariablesRemoved are names only (also for secrets).
	VariablesAdded, VariablesRemoved []string
	SkippedReason                    string
	Create                           bool
	Warnings                         []orchestrator.Warning
	Prepared                         *orchestrator.PreparedRelease

	hash any
}

// Plan is the answer of a dry run.
type Plan struct {
	Items    []Item
	Warnings []orchestrator.Warning
	Hash     string
}

// MissingNames lists the missing names of all items that stop the promotion.
func (p *Plan) MissingNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range p.Items {
		for _, n := range it.Missing {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Planner reads the database.
type Planner struct {
	DB vars.Querier
}

type svcRow struct {
	ID, Name, Type, SourceType string
	EnvID                      string
	Config                     map[string]any
	LiveRelease                *string
}

type relRow struct {
	ID, ImageRef, Digest string
	Number               int
	Config               map[string]any
	Variables            map[string]string
	SecretVersions       map[string]int
	Source               map[string]any
	Pruned               bool
	WasLive              bool
	ServiceID            string
}

// EnvInfo is an environment of a promotion.
type EnvInfo struct {
	ID, ProjectID, Kind, Slug string
	Protected                 bool
}

// Env reads an environment.
func (pl *Planner) Env(ctx context.Context, id string) (*EnvInfo, error) {
	var e EnvInfo
	err := pl.DB.QueryRow(ctx, `SELECT id, project_id, kind, slug, protected FROM environments WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&e.ID, &e.ProjectID, &e.Kind, &e.Slug, &e.Protected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

func (pl *Planner) service(ctx context.Context, where string, args ...any) (*svcRow, error) {
	var s svcRow
	var raw []byte
	err := pl.DB.QueryRow(ctx, `SELECT id, name, type, source_type, environment_id, config, live_release_id FROM services WHERE deleted_at IS NULL AND `+where, args...).
		Scan(&s.ID, &s.Name, &s.Type, &s.SourceType, &s.EnvID, &raw, &s.LiveRelease)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Config = map[string]any{}
	_ = json.Unmarshal(raw, &s.Config)
	return &s, nil
}

func (pl *Planner) release(ctx context.Context, id string) (*relRow, error) {
	var r relRow
	var cfg, vr, sv, src []byte
	var pruned any
	err := pl.DB.QueryRow(ctx, `SELECT id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source, pruned_at,
		EXISTS (SELECT 1 FROM deployment_steps st JOIN deployments d ON d.id = st.deployment_id WHERE d.release_id = releases.id AND st.state = 'live')
		FROM releases WHERE id = $1`, id).Scan(&r.ID, &r.ServiceID, &r.Number, &r.ImageRef, &r.Digest, &cfg, &vr, &sv, &src, &pruned, &r.WasLive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Pruned = pruned != nil
	r.Config, r.Variables, r.SecretVersions, r.Source = map[string]any{}, map[string]string{}, map[string]int{}, map[string]any{}
	_ = json.Unmarshal(cfg, &r.Config)
	_ = json.Unmarshal(vr, &r.Variables)
	_ = json.Unmarshal(sv, &r.SecretVersions)
	_ = json.Unmarshal(src, &r.Source)
	return &r, nil
}

// checkTarget applies REQ-PROMO-009: the same project, and no preview as target.
func checkTarget(srcEnv, tgtEnv *EnvInfo, allowPreview bool) error {
	switch {
	case srcEnv.ID == tgtEnv.ID:
		return ErrSameEnvironment
	case srcEnv.ProjectID != tgtEnv.ProjectID:
		return ErrOtherProject
	case tgtEnv.Kind == "preview" && !allowPreview:
		return ErrPreviewTarget
	}
	return nil
}

// ForRelease plans the promotion of one Release to the service of the same name in the target environment.
func (pl *Planner) ForRelease(ctx context.Context, releaseID string, o Options) (*Plan, *EnvInfo, error) {
	rel, err := pl.release(ctx, releaseID)
	if err != nil {
		return nil, nil, err
	}
	src, err := pl.service(ctx, `id = $1`, rel.ServiceID)
	if err != nil {
		return nil, nil, err
	}
	if src == nil {
		return nil, nil, ErrNotFound
	}
	srcEnv, err := pl.Env(ctx, src.EnvID)
	if err != nil {
		return nil, nil, err
	}
	tgtEnv, err := pl.Env(ctx, o.TargetEnvID)
	if err != nil {
		return nil, nil, err
	}
	if err := checkTarget(srcEnv, tgtEnv, o.AllowPreview); err != nil {
		return nil, tgtEnv, err
	}
	it, err := pl.item(ctx, src, rel, tgtEnv, o)
	if err != nil {
		return nil, tgtEnv, err
	}
	plan := &Plan{Items: []Item{*it}}
	plan.finish(o)
	return plan, tgtEnv, nil
}

// ForEnvironment plans the promotion of every service of the source environment (REQ-PROMO-008).
// The services are in the order of the dependencies: databases first, then the others by depends_on.
func (pl *Planner) ForEnvironment(ctx context.Context, srcEnvID string, o Options) (*Plan, *EnvInfo, error) {
	srcEnv, err := pl.Env(ctx, srcEnvID)
	if err != nil {
		return nil, nil, err
	}
	tgtEnv, err := pl.Env(ctx, o.TargetEnvID)
	if err != nil {
		return nil, nil, err
	}
	if err := checkTarget(srcEnv, tgtEnv, o.AllowPreview); err != nil {
		return nil, tgtEnv, err
	}
	rows, err := pl.DB.Query(ctx, `SELECT id FROM services WHERE environment_id = $1 AND deleted_at IS NULL ORDER BY name`, srcEnvID)
	if err != nil {
		return nil, tgtEnv, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, tgtEnv, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var services []*svcRow
	for _, id := range ids {
		s, err := pl.service(ctx, `id = $1`, id)
		if err != nil {
			return nil, tgtEnv, err
		}
		if s != nil {
			services = append(services, s)
		}
	}
	plan := &Plan{}
	for _, s := range order(services) {
		if s.Type == "database" && !o.IncludeDatabases {
			plan.Items = append(plan.Items, Item{Service: s.Name, SourceServiceID: s.ID, SkippedReason: "database_skipped"})
			continue
		}
		if s.LiveRelease == nil {
			plan.Items = append(plan.Items, Item{Service: s.Name, SourceServiceID: s.ID, SkippedReason: "no_live_release"})
			continue
		}
		rel, err := pl.release(ctx, *s.LiveRelease)
		if err != nil {
			return nil, tgtEnv, err
		}
		it, err := pl.item(ctx, s, rel, tgtEnv, o)
		if err != nil {
			return nil, tgtEnv, err
		}
		plan.Items = append(plan.Items, *it)
	}
	plan.finish(o)
	return plan, tgtEnv, nil
}

// order puts databases first, then every service after the services it depends on. A cycle is cut by name order.
func order(list []*svcRow) []*svcRow {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	byName := map[string]*svcRow{}
	for _, s := range list {
		byName[s.Name] = s
	}
	var out []*svcRow
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(s *svcRow)
	visit = func(s *svcRow) {
		if state[s.Name] != 0 {
			return
		}
		state[s.Name] = 1
		var deps []string
		if raw, ok := s.Config["depends_on"].([]any); ok {
			for _, d := range raw {
				if n, ok := d.(string); ok {
					deps = append(deps, n)
				}
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			if dep := byName[d]; dep != nil {
				visit(dep)
			}
		}
		state[s.Name] = 2
		out = append(out, s)
	}
	for _, s := range list {
		if s.Type == "database" {
			visit(s)
		}
	}
	for _, s := range list {
		visit(s)
	}
	return out
}

// envSpecific are the config keys that belong to the target environment and are never copied (REQ-PROMO-002).
// resources is only replaced when the target sets it.
var envSpecific = []string{"replicas", "placement", "failover", "strategy", "strategy_options", "canary", "deploy", "host_port",
	"domains", "backup", "resources", vars.KeyVolumes, vars.KeySecretRefs, vars.KeyEnvTemplates}

// item makes the plan of one service.
func (pl *Planner) item(ctx context.Context, src *svcRow, rel *relRow, tgtEnv *EnvInfo, o Options) (*Item, error) {
	it := &Item{Service: src.Name, SourceServiceID: src.ID, SourceReleaseID: rel.ID, SourceReleaseNumber: rel.Number, ImageDigest: rel.Digest}
	if rel.Pruned {
		return nil, &NotPromotableError{Reason: "the image of the release was pruned"}
	}
	if !rel.WasLive {
		return nil, &NotPromotableError{Reason: "the release was never live in its environment"}
	}
	tgt, err := pl.service(ctx, `environment_id = $1 AND name = $2`, tgtEnv.ID, src.Name)
	if err != nil {
		return nil, err
	}
	if tgt == nil && (!o.CreateMissing || src.Type == "database") {
		// A database is never created by a promotion: its data is not copied (F04 edge cases).
		it.SkippedReason = "missing_in_target"
		return it, nil
	}
	var tgtCfg map[string]any
	var snap *vars.Snapshot
	var baseline *relRow
	if tgt != nil {
		it.TargetServiceID = tgt.ID
		if snap, err = vars.Build(ctx, pl.DB, tgt.ID); err != nil {
			return nil, err
		}
		tgtCfg = configOf(snap)
		if tgt.LiveRelease != nil {
			if baseline, err = pl.release(ctx, *tgt.LiveRelease); err != nil {
				return nil, err
			}
			it.TargetReleaseID = baseline.ID
		}
	} else {
		it.Create = true
		if snap, err = pl.envSnapshot(ctx, src, tgtEnv); err != nil {
			return nil, err
		}
		tgtCfg = configOf(snap)
		tgtCfg["replicas"] = float64(1) // a new service has one replica and no domain (REQ-PROMO-006)
	}

	// New config: the source config, with the env-specific fields of the target.
	newCfg := map[string]any{}
	for k, v := range rel.Config {
		newCfg[k] = v
	}
	for _, k := range envSpecific {
		tv, has := tgtCfg[k]
		if k == "resources" && !has {
			continue // only a target that sets resources overrides them
		}
		delete(newCfg, k)
		if has {
			newCfg[k] = tv
		}
	}

	// Variables and secrets resolve in the scope of the target (REQ-PROMO-003).
	allowed := map[string]bool{}
	for _, n := range o.AllowMissing {
		allowed[n] = true
	}
	have := map[string]bool{}
	for n := range snap.Variables {
		have[n] = true
	}
	for n := range snap.SecretVersions {
		have[n] = true
	}
	if m, ok := tgtCfg[vars.KeyEnvTemplates].(map[string]any); ok {
		for n := range m {
			have[n] = true
		}
	}
	used := map[string]bool{}
	for n := range rel.Variables {
		used[n] = true
	}
	for n := range rel.SecretVersions {
		if !strings.Contains(n, ".") { // the secret of a referenced database is only used in templates
			used[n] = true
		}
	}
	if m, ok := rel.Config[vars.KeyEnvTemplates].(map[string]any); ok {
		for n := range m {
			used[n] = true
		}
	}
	var files []vars.SecretFile
	if raw, err := json.Marshal(rel.Config["secret_files"]); err == nil {
		_ = json.Unmarshal(raw, &files)
	}
	for _, f := range files {
		if _, ok := snap.Secrets[f.Secret]; !ok {
			used[f.Secret] = true
			delete(have, f.Secret)
		}
	}
	if !o.KeepMissing {
		for n := range used {
			if have[n] {
				continue
			}
			if allowed[n] {
				it.Allowed = append(it.Allowed, n)
			} else {
				it.Missing = append(it.Missing, n)
			}
		}
		sort.Strings(it.Missing)
		sort.Strings(it.Allowed)
	}

	// A domain of the target must point to a port that the release exposes (edge case of F04).
	if p, _ := numberOf(newCfg["port"]); p != 0 && it.TargetServiceID != "" {
		var mismatch bool
		if err := pl.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE service_id = $1 AND container_port <> $2)`, it.TargetServiceID, p).Scan(&mismatch); err != nil {
			return nil, err
		}
		if mismatch {
			return nil, ErrDomainPortMismatch
		}
		if cur, _ := numberOf(tgt.Config["port"]); cur != 0 && cur != p {
			it.Warnings = append(it.Warnings, orchestrator.Warning{Code: "port_changed",
				Message: fmt.Sprintf("the port of %s changes from %d to %d", it.Service, cur, p)})
		}
	}

	cfgJSON, err := json.Marshal(newCfg)
	if err != nil {
		return nil, err
	}
	source := map[string]any{}
	for k, v := range rel.Source {
		if k != "rollback_of_release_id" && k != "actor_id" {
			source[k] = v
		}
	}
	source["promoted_from_release_id"] = rel.ID
	pr := &orchestrator.PreparedRelease{ImageRef: rel.ImageRef, Digest: rel.Digest, Config: cfgJSON, Variables: snap.Variables,
		SecretVersions: snap.SecretVersions, Source: source}
	for name, e := range snap.Secrets {
		pr.SecretUses = append(pr.SecretUses, orchestrator.SecretUse{ID: e.ID, Name: name, Version: e.Version})
	}
	sort.Slice(pr.SecretUses, func(i, j int) bool { return pr.SecretUses[i].Name < pr.SecretUses[j].Name })
	it.Prepared = pr

	// The diff against what the target runs now.
	var before map[string]any
	var beforeVars map[string]string
	if baseline != nil {
		before, beforeVars = baseline.Config, baseline.Variables
		if baseline.Digest != rel.Digest {
			it.Changes = append(it.Changes, Change{Path: "image_digest", Before: baseline.Digest, After: rel.Digest})
		}
	} else {
		before, beforeVars = map[string]any{}, map[string]string{}
		it.Changes = append(it.Changes, Change{Path: "image_digest", After: rel.Digest})
	}
	it.Changes = append(it.Changes, diffMaps("", before, newCfg)...)
	for _, n := range sortedKeys(snap.Variables) {
		if old, ok := beforeVars[n]; !ok {
			it.VariablesAdded = append(it.VariablesAdded, n)
		} else if old != snap.Variables[n] {
			it.Changes = append(it.Changes, Change{Path: "variables." + n, Before: old, After: snap.Variables[n]})
		}
	}
	for _, n := range sortedKeys(beforeVars) {
		if _, ok := snap.Variables[n]; !ok {
			it.VariablesRemoved = append(it.VariablesRemoved, n)
		}
	}
	var beforeSecrets map[string]int
	if baseline != nil {
		beforeSecrets = baseline.SecretVersions
	}
	for _, n := range sortedKeys(snap.SecretVersions) {
		if _, ok := beforeSecrets[n]; !ok {
			it.VariablesAdded = append(it.VariablesAdded, n)
		}
	}
	for _, n := range sortedKeys(beforeSecrets) {
		if _, ok := snap.SecretVersions[n]; !ok {
			it.VariablesRemoved = append(it.VariablesRemoved, n)
		}
	}

	it.hash = map[string]any{"release": rel.ID, "service": it.TargetServiceID, "create": it.Create, "live": it.TargetReleaseID,
		"config": json.RawMessage(cfgJSON), "variables": snap.Variables, "secrets": snap.SecretVersions, "missing": it.Missing, "allowed": it.Allowed}
	return it, nil
}

// envSnapshot resolves the variables of a service that does not exist yet in the target: only the project and the
// environment scope apply.
func (pl *Planner) envSnapshot(ctx context.Context, src *svcRow, tgtEnv *EnvInfo) (*vars.Snapshot, error) {
	entries, err := vars.LoadEntries(ctx, pl.DB, tgtEnv.ProjectID)
	if err != nil {
		return nil, err
	}
	dbEntries, err := vars.DatabaseEntries(ctx, pl.DB, tgtEnv.ID)
	if err != nil {
		return nil, err
	}
	view := vars.Effective(entries, tgtEnv.ID, "", tgtEnv.Kind == "preview")
	for _, e := range dbEntries {
		view[e.Name] = e
	}
	res, err := vars.Resolve(view)
	if err != nil {
		return nil, err
	}
	snap := &vars.Snapshot{Variables: res.Plain, SecretVersions: map[string]int{}, Secrets: res.Secrets}
	refs := map[string]string{}
	for name, e := range res.Secrets {
		snap.SecretVersions[name] = e.Version
		refs[name] = e.ID
	}
	cfg := map[string]any{vars.KeyVolumes: []vars.Volume{}, vars.KeySecretRefs: refs, vars.KeyEnvTemplates: res.Templates}
	snap.Config, _ = json.Marshal(cfg)
	return snap, nil
}

func numberOf(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffMaps lists the leaf fields that differ. Arrays are compared as a whole.
func diffMaps(prefix string, before, after map[string]any) []Change {
	var out []Change
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		b, hb := before[k]
		a, ha := after[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		bm, bok := b.(map[string]any)
		am, aok := a.(map[string]any)
		switch {
		case hb && ha && bok && aok:
			out = append(out, diffMaps(path, bm, am)...)
		case !hb:
			out = append(out, Change{Path: path, After: a})
		case !ha:
			out = append(out, Change{Path: path, Before: b})
		case !reflect.DeepEqual(b, a):
			out = append(out, Change{Path: path, Before: b, After: a})
		}
	}
	return out
}

// finish sets the hash of the plan (REQ-PROMO-004). It covers the source, the target state that the plan read,
// and the options. A change of the target makes a new hash, so a confirm with the old hash gives 409 plan_changed.
func (p *Plan) finish(o Options) {
	parts := make([]any, 0, len(p.Items))
	for _, it := range p.Items {
		parts = append(parts, map[string]any{"service": it.Service, "skipped": it.SkippedReason, "item": it.hash})
	}
	allow := append([]string(nil), o.AllowMissing...)
	sort.Strings(allow)
	raw, _ := json.Marshal(map[string]any{"target": o.TargetEnvID, "create_missing": o.CreateMissing, "include_databases": o.IncludeDatabases,
		"allow_missing": allow, "strategy": o.Strategy, "items": parts})
	sum := sha256.Sum256(raw)
	p.Hash = "sha256:" + hex.EncodeToString(sum[:])
	for _, it := range p.Items {
		p.Warnings = append(p.Warnings, it.Warnings...)
	}
}

// configOf reads the config of a snapshot into a map.
func configOf(snap *vars.Snapshot) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(snap.Config, &m)
	return m
}

// NewServiceConfig makes the config of a service that a promotion creates (REQ-PROMO-006): the config of the source
// without the fields of its environment, with one replica, and a git source that does not deploy by itself.
func NewServiceConfig(source json.RawMessage) (json.RawMessage, error) {
	cfg := map[string]any{}
	if err := json.Unmarshal(source, &cfg); err != nil {
		return nil, err
	}
	for _, k := range envSpecific {
		delete(cfg, k)
	}
	cfg["replicas"] = 1
	if g, ok := cfg["git"].(map[string]any); ok {
		g["auto_deploy"] = false
	}
	return json.Marshal(cfg)
}
