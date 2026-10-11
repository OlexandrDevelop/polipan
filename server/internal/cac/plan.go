// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Actions of a change (REQ-CAC-004).
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionDelete    = "delete"
	ActionUnmanaged = "unmanaged"
)

// Resource kinds, in the order of apply (07 section 6).
const (
	ResPreviews     = "previews"
	ResEnvironment  = "environment"
	ResVariable     = "variable"
	ResSecret       = "secret"
	ResService      = "service"
	ResVolume       = "volume"
	ResDomain       = "domain"
	ResBackupTarget = "backup_target"
	ResBackupPolicy = "backup_policy"
	ResAlert        = "alert"
)

// FieldChange is one changed field.
type FieldChange struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// Change is one resource in the plan.
type Change struct {
	Action   string        `json:"action"`
	Resource string        `json:"resource"`
	Name     string        `json:"name"`
	Fields   []FieldChange `json:"fields,omitempty"`
	// Environment is the slug of the environment, empty for project and org items.
	Environment string `json:"environment,omitempty"`
	// ProtectedEnvironment is true when the change touches a protected environment (REQ-CAC-013).
	ProtectedEnvironment bool `json:"protected_environment,omitempty"`
	// RequiresAdmin is true for the changes of REQ-CAC-014.
	RequiresAdmin bool   `json:"requires_admin,omitempty"`
	Hint          string `json:"hint,omitempty"`

	op *op
}

// op is what apply needs to run a change.
type op struct {
	env     string
	service string
	ds      *DesiredService
	ls      *LiveService
	le      *LiveEnv
	de      *DesiredEnv
	// deploy tells that a Release and a Deployment follow (REQ-CAC-008).
	deploy bool
	// name of the item inside the service (variable, volume, domain)
	item string
	// data for the item
	dvar    *DesiredVar
	lvar    *LiveVar
	dvol    *DesiredVolume
	lvol    *LiveVolume
	ddom    *DesiredDomain
	ldom    *LiveDomain
	secret  *LiveSecret
	ref     *SecretRef
	target  *BackupTarget
	ltarget *LiveTarget
	policy  *LivePolicy
	preview map[string]any
	scope   string // project, environment or service (variables, secrets)
	// dataLoss is true when deleting the item deletes data (volumes and databases need prune_data).
	dataLoss bool
}

// Plan is the answer of the plan call (ConfigPlan in the OpenAPI file).
type Plan struct {
	PlanHash string    `json:"plan_hash"`
	Valid    bool      `json:"valid"`
	Errors   []Issue   `json:"errors"`
	Warnings []Warning `json:"warnings,omitempty"`
	Changes  []Change  `json:"changes"`

	parsed  *Parsed
	desired *Desired
	live    *Live
}

// LiveVersion is the version of the live state: a hash of everything the plan compares with (REQ-CAC-102).
func LiveVersion(live *Live) string {
	raw, _ := json.Marshal(live)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Hash is the plan hash: the file content and the live state version (REQ-CAC-102).
func Hash(content []byte, liveVersion string) string {
	h := sha256.New()
	h.Write(content)
	h.Write([]byte{0})
	h.Write([]byte(liveVersion))
	return hex.EncodeToString(h.Sum(nil))
}

// NewPlan parses the file, computes the desired state and diffs it with the live state. It changes nothing.
// project is the slug the file must have. An invalid file gives a plan with valid=false and no changes.
func NewPlan(content []byte, live *Live, bc BuildContext) *Plan {
	pl := &Plan{Errors: []Issue{}, Changes: []Change{}, live: live}
	pl.PlanHash = Hash(content, LiveVersion(live))
	p, issues := Parse(content)
	if len(issues) == 0 && p.File.Project != live.Slug {
		issues = []Issue{{Line: p.lineOf([]string{"project"}), Path: "project",
			Message: fmt.Sprintf("the file is for the project %q, but this project is %q. One file belongs to one project", p.File.Project, live.Slug)}}
	}
	if len(issues) > 0 {
		pl.Errors = issues
		return pl
	}
	d, issues := p.Desire(live, bc)
	pl.parsed, pl.desired = p, d
	pl.Warnings = d.Warnings
	if len(issues) > 0 {
		pl.Errors = issues
		return pl
	}
	b := &builder{p: p, d: d, live: live, pl: pl}
	b.diff()
	sort.SliceStable(pl.Changes, func(i, j int) bool { return resOrder(pl.Changes[i].Resource) < resOrder(pl.Changes[j].Resource) })
	pl.Errors = append(pl.Errors, b.issues...)
	pl.Valid = len(pl.Errors) == 0
	return pl
}

func resOrder(r string) int {
	for i, x := range []string{ResPreviews, ResEnvironment, ResVariable, ResSecret, ResService, ResVolume, ResDomain, ResBackupTarget, ResBackupPolicy, ResAlert} {
		if x == r {
			return i
		}
	}
	return 99
}

// HasChanges tells if the plan would change anything (unmanaged items are only shown).
func (pl *Plan) HasChanges() bool {
	for _, c := range pl.Changes {
		if c.Action != ActionUnmanaged {
			return true
		}
	}
	return false
}

// MissingSecrets returns the names of the secrets that the file needs and that do not exist.
func (pl *Plan) MissingSecrets() []string {
	var out []string
	for _, e := range pl.Errors {
		if e.Code == "missing_secret" {
			out = append(out, strings.TrimPrefix(strings.SplitN(e.Message, "\"", 3)[1], ""))
		}
	}
	return out
}

type builder struct {
	p      *Parsed
	d      *Desired
	live   *Live
	pl     *Plan
	issues []Issue
}

func (b *builder) add(c Change) *Change {
	b.pl.Changes = append(b.pl.Changes, c)
	return &b.pl.Changes[len(b.pl.Changes)-1]
}

func (b *builder) issue(path, msg, code string) {
	b.issues = append(b.issues, Issue{Line: b.p.lineOf(strings.Split(path, ".")), Path: path, Message: msg, Code: code})
}

func norm(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

func same(a, b any) bool { return reflect.DeepEqual(norm(a), norm(b)) }

// restrict keeps in live only the keys that desired has, so that keys the file does not set are not a change.
func restrict(live, desired any) any {
	lm, lok := live.(map[string]any)
	dm, dok := desired.(map[string]any)
	if !lok || !dok {
		return live
	}
	out := map[string]any{}
	for k, dv := range dm {
		if lv, ok := lm[k]; ok {
			out[k] = restrict(lv, dv)
		}
	}
	return out
}

// mapKeys are the config keys that are maps that merge key by key. The other keys are replaced as a whole.
var mapKeys = map[string]bool{"git": true, "strategy_options": true, "extra": true}

// configDiff lists the changes of the config keys that the file sets.
func configDiff(live map[string]any, overlay map[string]any) []FieldChange {
	var out []FieldChange
	for _, k := range sortedKeys(overlay) {
		want := norm(overlay[k])
		if wm, ok := want.(map[string]any); ok && mapKeys[k] {
			lm, _ := norm(live[k]).(map[string]any)
			for _, sk := range sortedKeys(wm) {
				if lv, has := lm[sk]; !has || !same(lv, wm[sk]) {
					out = append(out, FieldChange{Path: "config." + k + "." + sk, Before: lv, After: wm[sk]})
				}
			}
			continue
		}
		if lv, has := live[k]; !has || !same(lv, want) {
			out = append(out, FieldChange{Path: "config." + k, Before: live[k], After: want})
		}
	}
	return out
}

func (b *builder) diff() {
	d, live := b.d, b.live
	b.diffPreviews()
	b.diffEnvs()
	b.diffProjectVars()
	for _, slug := range d.EnvOrder {
		b.diffEnvVars(slug)
	}
	b.diffSecrets()
	// Services of every environment.
	for _, slug := range d.EnvOrder {
		le := live.Envs[slug]
		var created, unmanaged []int
		for _, name := range sortedKeys(d.Services) {
			ds := d.Services[name]
			if ds.Env != slug {
				continue
			}
			var ls *LiveService
			if le != nil {
				ls = le.Services[ds.Name]
			}
			idx := b.diffService(ds, le, ls)
			if idx >= 0 && b.pl.Changes[idx].Action == ActionCreate {
				created = append(created, idx)
			}
		}
		if le != nil {
			for _, name := range sortedKeys(le.Services) {
				if _, ok := d.Services[ServiceKey(slug, name)]; ok {
					continue
				}
				ls := le.Services[name]
				c := b.add(Change{Action: ActionUnmanaged, Resource: ResService, Name: slug + "/" + name, Environment: slug})
				c.op = &op{env: slug, service: name, ls: ls, le: le, dataLoss: ls.Type == "database" || len(ls.Volumes) > 0}
				unmanaged = append(unmanaged, len(b.pl.Changes)-1)
			}
		}
		if len(created) > 0 && len(unmanaged) > 0 {
			for _, i := range append(created, unmanaged...) {
				b.pl.Changes[i].Hint = "a rename is not automatic: the new name is created and the old name is unmanaged (or deleted with prune)"
			}
		}
	}
	b.diffTargets()
	b.diffPolicies()
	if a := d.Alerts; a != nil && (len(a.Channels) > 0 || len(a.Rules) > 0) {
		b.pl.Warnings = append(b.pl.Warnings, Warning{Path: "alerts", Message: "alerts are not applied yet: the alert engine is not in this version"})
	}
	// Flags of the changes.
	for i := range b.pl.Changes {
		c := &b.pl.Changes[i]
		if c.Environment != "" {
			if le := live.Envs[c.Environment]; le != nil && le.Protected {
				c.ProtectedEnvironment = true
			} else if de := d.Envs[c.Environment]; le == nil && de != nil && de.Protected != nil && *de.Protected {
				c.ProtectedEnvironment = true
			}
		}
	}
}

func (b *builder) diffPreviews() {
	if b.d.Previews == nil {
		return
	}
	cur, _ := b.live.Settings["previews"].(map[string]any)
	var fields []FieldChange
	for _, k := range sortedKeys(b.d.Previews) {
		if lv, ok := cur[k]; !ok || !same(lv, b.d.Previews[k]) {
			fields = append(fields, FieldChange{Path: "previews." + k, Before: cur[k], After: b.d.Previews[k]})
		}
	}
	if len(fields) == 0 {
		return
	}
	c := b.add(Change{Action: ActionUpdate, Resource: ResPreviews, Name: "project", Fields: fields})
	c.op = &op{preview: b.d.Previews}
	for _, f := range fields {
		if f.Path == "previews.from_forks" { // REQ-CAC-014
			c.RequiresAdmin = true
		}
	}
}

func (b *builder) diffEnvs() {
	for _, slug := range b.d.EnvOrder {
		de, le := b.d.Envs[slug], b.live.Envs[slug]
		if !de.InFile {
			if le != nil && len(b.p.File.Environments) > 0 {
				c := b.add(Change{Action: ActionUnmanaged, Resource: ResEnvironment, Name: slug, Environment: slug})
				c.op = &op{env: slug, le: le}
			}
			continue
		}
		if le == nil {
			kind := de.Kind
			if kind == "" {
				kind = "custom"
			}
			protected := kind == "production"
			if de.Protected != nil {
				protected = *de.Protected
			}
			fields := []FieldChange{{Path: "kind", After: kind}, {Path: "protected", After: protected}}
			if de.AutoDeploy != nil {
				fields = append(fields, FieldChange{Path: "auto_deploy", After: *de.AutoDeploy})
			}
			c := b.add(Change{Action: ActionCreate, Resource: ResEnvironment, Name: slug, Fields: fields, Environment: slug})
			c.op = &op{env: slug, de: de}
			if protected && de.AutoDeploy != nil && *de.AutoDeploy {
				c.RequiresAdmin = true
			}
			continue
		}
		var fields []FieldChange
		admin := false
		if de.Kind != "" && de.Kind != le.Kind {
			b.issue(join("environments", slug, "kind"), fmt.Sprintf("the kind of the environment %q is %s and cannot change", slug, le.Kind), "")
		}
		if de.Protected != nil && *de.Protected != le.Protected {
			fields = append(fields, FieldChange{Path: "protected", Before: le.Protected, After: *de.Protected})
			if !*de.Protected { // REQ-CAC-014
				admin = true
			}
		}
		if de.AutoDeploy != nil {
			cur, _ := le.Settings["auto_deploy"].(bool)
			if _, has := le.Settings["auto_deploy"]; !has || cur != *de.AutoDeploy {
				fields = append(fields, FieldChange{Path: "auto_deploy", Before: le.Settings["auto_deploy"], After: *de.AutoDeploy})
				protected := le.Protected
				if de.Protected != nil {
					protected = protected || *de.Protected
				}
				if *de.AutoDeploy && protected {
					admin = true
				}
			}
		}
		if len(fields) > 0 {
			c := b.add(Change{Action: ActionUpdate, Resource: ResEnvironment, Name: slug, Fields: fields, Environment: slug, RequiresAdmin: admin})
			c.op = &op{env: slug, de: de, le: le}
		}
	}
}

func (b *builder) diffVars(scope, env, prefix string, want map[string]DesiredVar, have map[string]LiveVar, svc *LiveService) {
	for _, name := range sortedKeys(want) {
		w := want[name]
		h, ok := have[name]
		label := prefix + name
		w2, h2 := w, h
		switch {
		case !ok:
			c := b.add(Change{Action: ActionCreate, Resource: ResVariable, Name: label, Environment: env,
				Fields: []FieldChange{{Path: "value", After: w.Value}}})
			c.op = &op{scope: scope, env: env, item: name, dvar: &w2, ls: svc, deploy: true}
		default:
			var fields []FieldChange
			if h.Value != w.Value {
				fields = append(fields, FieldChange{Path: "value", Before: h.Value, After: w.Value})
			}
			if h.Preview != w.Preview {
				fields = append(fields, FieldChange{Path: "preview", Before: h.Preview, After: w.Preview})
			}
			if h.BuildTime != w.BuildTime {
				fields = append(fields, FieldChange{Path: "build_time", Before: h.BuildTime, After: w.BuildTime})
			}
			if !h.Managed {
				fields = append(fields, FieldChange{Path: "git_managed", Before: false, After: true})
			}
			if len(fields) > 0 {
				c := b.add(Change{Action: ActionUpdate, Resource: ResVariable, Name: label, Environment: env, Fields: fields})
				c.op = &op{scope: scope, env: env, item: name, dvar: &w2, lvar: &h2, ls: svc, deploy: true}
			}
		}
	}
	for _, name := range sortedKeys(have) {
		if _, ok := want[name]; !ok && have[name].Managed {
			h2 := have[name]
			c := b.add(Change{Action: ActionUnmanaged, Resource: ResVariable, Name: prefix + name, Environment: env})
			c.op = &op{scope: scope, env: env, item: name, lvar: &h2, ls: svc, deploy: true}
		}
	}
}

func (b *builder) diffProjectVars() {
	b.diffVars("project", "", "", b.d.ProjectVars, b.live.ProjectVars, nil)
}

func (b *builder) diffEnvVars(slug string) {
	le := b.live.Envs[slug]
	have := map[string]LiveVar{}
	if le != nil {
		have = le.Vars
	}
	if !b.d.Envs[slug].InFile && len(b.d.Envs[slug].Vars) == 0 {
		return
	}
	if le == nil {
		return // a new environment: its variables are made by the environment create
	}
	b.diffVars("environment", slug, slug+":", b.d.Envs[slug].Vars, have, nil)
}

func (b *builder) diffSecrets() {
	check := func(refs []SecretRef, path, env, envID, svcID string, projectOnly bool) {
		for i, r := range refs {
			var s *LiveSecret
			if projectOnly {
				for j := range b.live.Secrets {
					x := &b.live.Secrets[j]
					if x.Name == r.Name && x.EnvID == "" && x.ServiceID == "" {
						s = x
					}
				}
			} else {
				s = b.live.findSecret(r.Name, envID, svcID)
			}
			if s == nil {
				b.issue(join(path, i), fmt.Sprintf("the secret %q does not exist. Set it first with the UI, polipan secret set or Terraform", r.Name), "missing_secret")
				continue
			}
			var fields []FieldChange
			if r.Preview && !s.Preview {
				fields = append(fields, FieldChange{Path: "preview", Before: s.Preview, After: r.Preview})
			}
			if r.BuildTime && !s.BuildTime {
				fields = append(fields, FieldChange{Path: "build_time", Before: s.BuildTime, After: r.BuildTime})
			}
			if !s.Managed {
				fields = append(fields, FieldChange{Path: "git_managed", Before: false, After: true})
			}
			if len(fields) > 0 {
				r2, s2 := r, *s
				c := b.add(Change{Action: ActionUpdate, Resource: ResSecret, Name: r.Name, Environment: env, Fields: fields})
				c.op = &op{secret: &s2, ref: &r2}
			}
		}
	}
	check(b.d.Secrets, "secrets", "", "", "", true)
	for _, slug := range b.d.EnvOrder {
		de, le := b.d.Envs[slug], b.live.Envs[slug]
		envID := ""
		if le != nil {
			envID = le.ID
		}
		check(de.Secrets, join("environments", slug, "secrets"), slug, envID, "", false)
	}
	for _, key := range sortedKeys(b.d.Services) {
		ds := b.d.Services[key]
		envID, svcID := "", ""
		if le := b.live.Envs[ds.Env]; le != nil {
			envID = le.ID
			if ls := le.Services[ds.Name]; ls != nil {
				svcID = ls.ID
			}
		}
		check(ds.Secrets, join(ds.Path, "secrets"), ds.Env, envID, svcID, false)
	}
}

func (b *builder) diffService(ds *DesiredService, le *LiveEnv, ls *LiveService) int {
	name := ds.Env + "/" + ds.Name
	envProtected := le != nil && le.Protected
	if le == nil {
		if de := b.d.Envs[ds.Env]; de != nil && de.Protected != nil {
			envProtected = *de.Protected
		}
	}
	adminAuto := func() bool {
		g, _ := ds.Overlay["git"].(map[string]any)
		auto, _ := g["auto_deploy"].(bool)
		return envProtected && auto
	}
	if ls == nil {
		fields := []FieldChange{{Path: "type", After: ds.Type}}
		cfg := map[string]any{}
		for k, v := range ds.Defaults {
			cfg[k] = v
		}
		for k, v := range ds.Overlay {
			cfg[k] = v
		}
		for _, k := range sortedKeys(cfg) {
			fields = append(fields, FieldChange{Path: "config." + k, After: norm(cfg[k])})
		}
		if ds.Failover != "" {
			fields = append(fields, FieldChange{Path: "failover", After: ds.Failover})
		}
		for _, n := range sortedKeys(ds.Vars) {
			fields = append(fields, FieldChange{Path: "env." + n, After: ds.Vars[n].Value})
		}
		for _, v := range ds.Volumes {
			fields = append(fields, FieldChange{Path: "volumes." + v.Name, After: v.Path})
		}
		for _, dm := range ds.Domains {
			fields = append(fields, FieldChange{Path: "domains." + dm.Host + dm.PathPrefix, After: dm.Port})
		}
		c := b.add(Change{Action: ActionCreate, Resource: ResService, Name: name, Fields: fields, Environment: ds.Env, RequiresAdmin: adminAuto()})
		c.op = &op{env: ds.Env, service: ds.Name, ds: ds, le: le, deploy: true}
		return len(b.pl.Changes) - 1
	}
	var fields []FieldChange
	fields = append(fields, configDiff(ls.Config, ds.Overlay)...)
	if ds.Failover != "" && ds.Failover != ls.Failover {
		fields = append(fields, FieldChange{Path: "failover", Before: ls.Failover, After: ds.Failover})
	}
	if ds.AllowDataLoss != nil && *ds.AllowDataLoss != ls.AllowDataLoss {
		fields = append(fields, FieldChange{Path: "allow_data_loss_failover", Before: ls.AllowDataLoss, After: *ds.AllowDataLoss})
	}
	deploy := false
	for _, f := range fields {
		if strings.HasPrefix(f.Path, "config.") && f.Path != "config.extra.replicas" {
			deploy = true
		}
	}
	managedNow := append([]string{}, ls.Managed...)
	sort.Strings(managedNow)
	if !reflect.DeepEqual(managedNow, nonNil(ds.Managed)) {
		fields = append(fields, FieldChange{Path: "git_managed_fields", Before: managedNow, After: ds.Managed})
	}
	idx := -1
	if len(fields) > 0 {
		c := b.add(Change{Action: ActionUpdate, Resource: ResService, Name: name, Fields: fields, Environment: ds.Env})
		c.op = &op{env: ds.Env, service: ds.Name, ds: ds, ls: ls, le: le, deploy: deploy}
		c.RequiresAdmin = adminAuto() && hasField(fields, "config.git.auto_deploy")
		idx = len(b.pl.Changes) - 1
	}
	b.diffVars("service", ds.Env, name+":", ds.Vars, ls.Vars, ls)
	b.diffVolumes(ds, ls)
	b.diffDomains(ds, ls)
	return idx
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func hasField(fs []FieldChange, path string) bool {
	for _, f := range fs {
		if f.Path == path {
			return true
		}
	}
	return false
}

func (b *builder) diffVolumes(ds *DesiredService, ls *LiveService) {
	have := map[string]LiveVolume{}
	for _, v := range ls.Volumes {
		have[v.Name] = v
	}
	managed := containsStr(ls.Managed, "volumes")
	if !ds.SetsVolumes && !managed {
		return
	}
	prefix := ds.Env + "/" + ds.Name + ":"
	want := map[string]DesiredVolume{}
	for _, v := range ds.Volumes {
		want[v.Name] = v
		w := v
		h, ok := have[v.Name]
		if !ok {
			c := b.add(Change{Action: ActionCreate, Resource: ResVolume, Name: prefix + v.Name, Environment: ds.Env,
				Fields: []FieldChange{{Path: "path", After: v.Path}, {Path: "kind", After: v.Kind}}})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, dvol: &w, deploy: true}
			continue
		}
		var fields []FieldChange
		if h.Kind != v.Kind {
			b.issue(join(ds.Path, "volumes"), fmt.Sprintf("the kind of the volume %q cannot change", v.Name), "")
		}
		if h.Path != v.Path {
			fields = append(fields, FieldChange{Path: "path", Before: h.Path, After: v.Path})
		}
		if h.AccessMode != v.AccessMode {
			fields = append(fields, FieldChange{Path: "access_mode", Before: h.AccessMode, After: v.AccessMode})
		}
		if h.SizeLimit != v.SizeLimit {
			fields = append(fields, FieldChange{Path: "size_limit", Before: h.SizeLimit, After: v.SizeLimit})
		}
		if len(fields) > 0 {
			h2 := h
			c := b.add(Change{Action: ActionUpdate, Resource: ResVolume, Name: prefix + v.Name, Environment: ds.Env, Fields: fields})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, dvol: &w, lvol: &h2, deploy: true}
		}
	}
	for _, name := range sortedKeys(have) {
		if _, ok := want[name]; !ok {
			h := have[name]
			c := b.add(Change{Action: ActionUnmanaged, Resource: ResVolume, Name: prefix + name, Environment: ds.Env})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, lvol: &h, dataLoss: h.Kind == "volume", deploy: true}
		}
	}
}

func (b *builder) diffDomains(ds *DesiredService, ls *LiveService) {
	managed := containsStr(ls.Managed, "domains")
	if !ds.SetsDomains && !managed {
		return
	}
	key := func(host, path string) string { return host + path }
	have := map[string]LiveDomain{}
	for _, d := range ls.Domains {
		have[key(d.Host, d.PathPrefix)] = d
	}
	prefix := ds.Env + "/" + ds.Name + ":"
	want := map[string]bool{}
	for _, dm := range ds.Domains {
		k := key(dm.Host, dm.PathPrefix)
		want[k] = true
		w := dm
		h, ok := have[k]
		if !ok {
			c := b.add(Change{Action: ActionCreate, Resource: ResDomain, Name: prefix + k, Environment: ds.Env,
				Fields: []FieldChange{{Path: "port", After: dm.Port}}})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, ddom: &w}
			continue
		}
		var fields []FieldChange
		if h.Port != dm.Port {
			fields = append(fields, FieldChange{Path: "port", Before: h.Port, After: dm.Port})
		}
		if h.HTTPS != dm.HTTPS {
			fields = append(fields, FieldChange{Path: "https", Before: h.HTTPS, After: dm.HTTPS})
		}
		if h.RedirectWWW != dm.RedirectWWW {
			fields = append(fields, FieldChange{Path: "redirect_www", Before: h.RedirectWWW, After: dm.RedirectWWW})
		}
		if !same(restrict(h.Options, dm.Options), dm.Options) {
			fields = append(fields, FieldChange{Path: "options", Before: h.Options, After: dm.Options})
		}
		if len(fields) > 0 {
			h2 := h
			c := b.add(Change{Action: ActionUpdate, Resource: ResDomain, Name: prefix + k, Environment: ds.Env, Fields: fields})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, ddom: &w, ldom: &h2}
		}
	}
	for _, k := range sortedKeys(have) {
		if !want[k] {
			h := have[k]
			c := b.add(Change{Action: ActionUnmanaged, Resource: ResDomain, Name: prefix + k, Environment: ds.Env})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, ldom: &h}
		}
	}
}

func (b *builder) diffTargets() {
	for i := range b.d.Targets {
		t := b.d.Targets[i]
		lt := b.live.Targets[t.Name]
		if lt == nil {
			if t.CredentialsSecret != "" && !b.live.HasSecret(t.CredentialsSecret, "", "") {
				b.issue(join("backup_targets", i, "credentials_secret"),
					fmt.Sprintf("the secret %q does not exist. Set it first with the UI, polipan secret set or Terraform", t.CredentialsSecret), "missing_secret")
			}
			c := b.add(Change{Action: ActionCreate, Resource: ResBackupTarget, Name: t.Name, RequiresAdmin: true,
				Fields: []FieldChange{{Path: "kind", After: t.Kind}, {Path: "config", After: t.Config}}})
			c.op = &op{target: &t}
			continue
		}
		var fields []FieldChange
		if lt.Kind != t.Kind {
			b.issue(join("backup_targets", i, "kind"), fmt.Sprintf("the kind of the backup target %q cannot change", t.Name), "")
		}
		if !same(restrict(lt.Config, t.Config), t.Config) {
			fields = append(fields, FieldChange{Path: "config", Before: lt.Config, After: t.Config})
		}
		if len(fields) > 0 {
			lt2 := *lt
			c := b.add(Change{Action: ActionUpdate, Resource: ResBackupTarget, Name: t.Name, RequiresAdmin: true, Fields: fields})
			c.op = &op{target: &t, ltarget: &lt2}
		}
	}
}

func (b *builder) diffPolicies() {
	for _, key := range sortedKeys(b.d.Services) {
		ds := b.d.Services[key]
		if ds.Backup == nil {
			continue
		}
		var ls *LiveService
		if le := b.live.Envs[ds.Env]; le != nil {
			ls = le.Services[ds.Name]
		}
		bk := ds.Backup
		want := policyFields(bk)
		want["targets"] = nameList(bk.Targets)
		name := ds.Env + "/" + ds.Name
		if ls == nil || len(ls.Policies) == 0 {
			c := b.add(Change{Action: ActionCreate, Resource: ResBackupPolicy, Name: name, Environment: ds.Env,
				Fields: []FieldChange{{Path: "schedule", After: bk.Schedule}, {Path: "targets", After: bk.Targets}}})
			c.op = &op{env: ds.Env, ds: ds, ls: ls}
			continue
		}
		pol := ls.Policies[0]
		have := map[string]any{"schedule": pol.Schedule, "retention": pol.Retention, "restore_check_enabled": pol.CheckEnabled,
			"restore_check_schedule": pol.CheckSchedule, "restore_check_mode": pol.CheckMode}
		var names []string
		for _, id := range pol.TargetIDs {
			for n, t := range b.live.Targets {
				if t.ID == id {
					names = append(names, n)
				}
			}
		}
		sort.Strings(names)
		have["targets"] = nameList(names)
		var fields []FieldChange
		for _, k := range sortedKeys(want) {
			if hv, ok := have[k]; !ok || !same(hv, want[k]) {
				if k == "retention" {
					merged := map[string]int{}
					for rk, rv := range pol.Retention {
						merged[rk] = rv
					}
					for rk, rv := range bk.Retention {
						merged[rk] = rv
					}
					if same(merged, pol.Retention) {
						continue
					}
				}
				fields = append(fields, FieldChange{Path: k, Before: have[k], After: want[k]})
			}
		}
		if len(fields) > 0 {
			p2 := pol
			c := b.add(Change{Action: ActionUpdate, Resource: ResBackupPolicy, Name: name, Environment: ds.Env, Fields: fields})
			c.op = &op{env: ds.Env, ds: ds, ls: ls, policy: &p2}
		}
	}
}

func nameList(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// policyFields are the fields of the backup block that the file sets.
func policyFields(bk *Backup) map[string]any {
	out := map[string]any{}
	if bk.Schedule != "" {
		out["schedule"] = bk.Schedule
	}
	if bk.Retention != nil {
		out["retention"] = bk.Retention
	}
	if rc := bk.RestoreCheck; rc != nil {
		if rc.Enabled != nil {
			out["restore_check_enabled"] = *rc.Enabled
		}
		if rc.Schedule != "" {
			out["restore_check_schedule"] = rc.Schedule
		}
		if rc.Mode != "" {
			out["restore_check_mode"] = rc.Mode
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
