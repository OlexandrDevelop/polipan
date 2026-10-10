// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"fmt"
	"sort"
	"strings"
)

// DesiredVar is a variable that the file sets.
type DesiredVar struct {
	Value     string
	Preview   bool
	BuildTime bool
}

// DesiredDomain is a domain that the file sets.
type DesiredDomain struct {
	Host         string
	PathPrefix   string
	Port         int
	HTTPS        bool
	RedirectWWW  string
	IngressNodes []string
	Options      map[string]any
}

// DesiredVolume is a volume that the file sets.
type DesiredVolume struct {
	Name       string
	Kind       string
	Path       string
	AccessMode string
	SizeLimit  int64
	Node       string
}

// DesiredService is one service in one environment after merge, unit conversion and defaults.
type DesiredService struct {
	Env        string
	Name       string
	Type       string
	SourceType string
	// Overlay holds only the config keys that the file sets. Defaults is added when the service is created.
	Overlay  map[string]any
	Defaults map[string]any
	// Managed lists the git managed fields (services.git_managed_fields).
	Managed       []string
	Failover      string
	AllowDataLoss *bool
	Vars          map[string]DesiredVar
	Secrets       []SecretRef
	Domains       []DesiredDomain
	Volumes       []DesiredVolume
	SetsDomains   bool
	SetsVolumes   bool
	DB            *DB
	Backup        *Backup
	DependsOn     []string
	Path          string // where the author edits it, for messages
}

// DesiredEnv is an environment of the file or a live one.
type DesiredEnv struct {
	Slug       string
	Kind       string
	Protected  *bool
	AutoDeploy *bool
	InFile     bool
	Vars       map[string]DesiredVar
	Secrets    []SecretRef
}

// Warning is a note in a plan. It does not stop an apply.
type Warning struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Desired is what the file asks for, per environment.
type Desired struct {
	EnvOrder    []string
	Envs        map[string]*DesiredEnv
	Services    map[string]*DesiredService // key: env/name
	ProjectVars map[string]DesiredVar
	Secrets     []SecretRef
	Previews    map[string]any
	Targets     []BackupTarget
	Alerts      *Alerts
	Warnings    []Warning
}

// ServiceKey is the key of a service in Desired.Services.
func ServiceKey(env, name string) string { return env + "/" + name }

// BuildContext is what the desired state needs from outside the file.
type BuildContext struct {
	// DefaultConnectionID is the Git connection of config_source. A git service without connection uses it.
	DefaultConnectionID string
}

var _ = sort.Strings

// Desire merges the file for every environment and converts it to the live shape (07 sections 3 and 4).
func (p *Parsed) Desire(live *Live, bc BuildContext) (*Desired, []Issue) {
	f := p.File
	d := &Desired{Envs: map[string]*DesiredEnv{}, Services: map[string]*DesiredService{}, ProjectVars: map[string]DesiredVar{},
		Secrets: f.Secrets, Targets: f.BackupTargets, Alerts: f.Alerts}
	var issues []Issue
	bad := func(path, msg string) {
		issues = append(issues, Issue{Line: p.lineOf(strings.Split(path, ".")), Path: path, Message: msg})
	}
	for k, v := range f.Variables {
		d.ProjectVars[k] = DesiredVar{Value: string(v)}
	}
	if f.Previews != nil {
		pv, err := previewSettings(f.Previews, live)
		if err != nil {
			bad("previews.ttl", err.Error())
		}
		d.Previews = pv
	}
	// The environments: the ones in the file, and the live ones (except previews).
	seen := map[string]bool{}
	for _, slug := range sortedKeys(f.Environments) {
		e := f.Environments[slug]
		d.Envs[slug] = &DesiredEnv{Slug: slug, Kind: e.Kind, Protected: e.Protected, AutoDeploy: e.AutoDeploy, InFile: true,
			Vars: map[string]DesiredVar{}, Secrets: e.Secrets}
		for k, v := range e.Variables {
			d.Envs[slug].Vars[k] = DesiredVar{Value: string(v)}
		}
		d.EnvOrder, seen[slug] = append(d.EnvOrder, slug), true
	}
	for _, slug := range sortedKeys(live.Envs) {
		if live.Envs[slug].Kind == "preview" || seen[slug] {
			continue
		}
		d.Envs[slug] = &DesiredEnv{Slug: slug, Vars: map[string]DesiredVar{}}
		d.EnvOrder = append(d.EnvOrder, slug)
	}
	domainAt := map[string]string{}
	for _, slug := range d.EnvOrder {
		for _, name := range sortedKeys(f.Services) {
			eff, _ := f.EffectiveService(slug, name)
			if eff.Disabled {
				continue
			}
			where := join("services", name)
			if e := f.Environments[slug]; e != nil && e.Services[name] != nil {
				where = join("environments", slug, "services", name)
			}
			ds, warns, err := p.desireService(eff, slug, where, live, bc)
			if err != nil {
				bad(where, err.Error())
				continue
			}
			d.Warnings = append(d.Warnings, warns...)
			d.Services[ServiceKey(slug, name)] = ds
			for _, dom := range ds.Domains {
				key := dom.Host + dom.PathPrefix
				if prev, dup := domainAt[key]; dup && prev != slug+"/"+name {
					bad(join("services", name, "domains"), fmt.Sprintf("the domain %s%s would be in two places (%s and %s/%s). Set it under environments.<env>.services.%s.domains",
						dom.Host, dom.PathPrefix, prev, slug, name, name))
				}
				domainAt[key] = slug + "/" + name
			}
		}
	}
	// A backup target that is neither in the file nor live is an error (07 section 6).
	targets := map[string]bool{}
	for _, t := range f.BackupTargets {
		targets[t.Name] = true
	}
	for name := range live.Targets {
		targets[name] = true
	}
	for _, key := range sortedKeys(d.Services) {
		ds := d.Services[key]
		if ds.Backup == nil {
			continue
		}
		for i, t := range ds.Backup.Targets {
			if !targets[t] {
				bad(join(ds.Path, "backup", "targets", i), fmt.Sprintf("the backup target %q is not in backup_targets and does not exist", t))
			}
		}
	}
	return d, dedupe(issues)
}

func previewSettings(pv *Previews, live *Live) (map[string]any, error) {
	out := map[string]any{}
	if pv.Enabled != nil {
		out["enabled"] = *pv.Enabled
	}
	if pv.BaseEnvironment != "" {
		if le := live.Envs[pv.BaseEnvironment]; le != nil {
			out["base_environment_id"] = le.ID
		}
	}
	if pv.MaxActive != nil {
		out["max_active"] = *pv.MaxActive
	}
	if pv.TTL != "" {
		n, err := Seconds(pv.TTL)
		if err != nil {
			return nil, err
		}
		out["ttl_days"] = (n + 86399) / 86400
	}
	if pv.FromForks != nil {
		out["from_forks"] = *pv.FromForks
	}
	if pv.NodeSelector != nil {
		out["node_selector"] = pv.NodeSelector
	}
	if pv.Strategy != "" {
		out["strategy"] = pv.Strategy
	}
	if pv.SeedCommand != nil {
		out["seed_command"] = pv.SeedCommand
	}
	return out, nil
}

func (p *Parsed) desireService(eff *Effective, env, where string, live *Live, bc BuildContext) (*DesiredService, []Warning, error) {
	s := &eff.Service
	ds := &DesiredService{Env: env, Name: eff.Name, Type: s.Type, Overlay: map[string]any{}, Defaults: map[string]any{},
		Vars: map[string]DesiredVar{}, Secrets: s.Secrets, DB: s.DB, Backup: s.Backup, DependsOn: s.DependsOn, Path: where,
		Failover: s.Failover, AllowDataLoss: s.AllowDataLossFailover}
	if ds.Type == "" {
		ds.Type = "app"
	}
	var warns []Warning
	managed := map[string]bool{}
	set := func(key string, v any, field string) {
		ds.Overlay[key] = v
		managed[field] = true
	}
	extra := map[string]any{}
	setExtra := func(k string, v any) { extra[k] = v }
	has := func(k string) bool { _, ok := eff.Raw[k]; return ok }

	// source
	switch {
	case s.Source != nil && s.Source.Git != nil:
		g := s.Source.Git
		ds.SourceType = "git"
		git := map[string]any{"repo": g.Repo}
		if g.Branch != "" {
			git["branch"] = g.Branch
		}
		if g.Path != "" {
			git["path"] = g.Path
		}
		if g.Builder != "" {
			git["builder"] = g.Builder
		}
		if g.Dockerfile != "" {
			git["dockerfile"] = g.Dockerfile
		}
		conn := bc.DefaultConnectionID
		if g.Connection != "" {
			id, ok := live.Connections[g.Connection]
			if !ok {
				return nil, nil, fmt.Errorf("the Git connection %q does not exist", g.Connection)
			}
			conn = id
		}
		if conn != "" {
			git["connection_id"] = conn
		}
		if s.AutoDeploy != nil {
			git["auto_deploy"] = *s.AutoDeploy
		}
		set("git", git, "config.git")
	case s.Source != nil && s.Source.Image != nil:
		ds.SourceType = "image"
		set("image", s.Source.Image.Name, "config.image")
	default:
		ds.SourceType = "image"
	}
	// ports
	if len(s.Ports) > 0 {
		set("port", s.Ports[0].Container, "config.port")
		for _, pt := range s.Ports {
			if pt.Host != 0 {
				set("host_port", pt.Host, "config.host_port")
				break
			}
		}
		if len(s.Ports) > 1 {
			setExtra("ports", s.Ports)
		}
	}
	if s.Command != nil {
		set("command", s.Command, "config.command")
	}
	if s.Entrypoint != nil {
		set("entrypoint", s.Entrypoint, "config.entrypoint")
	}
	if s.RestartPolicy != "" {
		set("restart_policy", s.RestartPolicy, "config.restart_policy")
	}
	if s.StopGracePeriod != "" {
		n, err := Seconds(s.StopGracePeriod)
		if err != nil {
			return nil, nil, err
		}
		set("stop_grace_period", n, "config.stop_grace_period")
	}
	if s.Resources != nil {
		res := map[string]any{}
		if s.Resources.CPU != "" {
			n, err := CPUMillis(s.Resources.CPU)
			if err != nil {
				return nil, nil, err
			}
			res["cpu_millis"] = n
		}
		if s.Resources.Memory != "" {
			n, err := Bytes(s.Resources.Memory)
			if err != nil {
				return nil, nil, err
			}
			res["memory_bytes"] = n
		}
		set("resources", res, "config.resources")
		if s.Resources.MemoryReservation != "" {
			n, err := Bytes(s.Resources.MemoryReservation)
			if err != nil {
				return nil, nil, err
			}
			setExtra("memory_reservation_bytes", n)
		}
	}
	if s.Healthcheck != nil {
		h, err := healthConfig(s.Healthcheck, s.Ports)
		if err != nil {
			return nil, nil, err
		}
		set("health", h, "config.health")
	}
	if len(s.Files) > 0 || (has("files") && s.Files != nil) {
		files := make([]any, len(s.Files))
		for i, fm := range s.Files {
			files[i] = map[string]any{"path": fm.Path, "content": fm.Content}
		}
		set("files", files, "config.files")
	}
	var secretFiles []any
	for _, sr := range s.Secrets {
		if sr.File != "" {
			secretFiles = append(secretFiles, map[string]any{"path": sr.File, "secret": sr.Name})
		}
	}
	if len(secretFiles) > 0 {
		set("secret_files", secretFiles, "config.secret_files")
	}
	if dp := s.Deploy; dp != nil {
		if dp.Strategy != "" {
			strategy := dp.Strategy
			if strategy != "recreate" && (hasSingleWriter(s.Volumes) || ds.Type == "database") {
				warns = append(warns, Warning{Path: where + ".deploy.strategy",
					Message: fmt.Sprintf("the strategy %s does not fit a single-writer volume or a database. recreate is used", strategy)})
				strategy = "recreate"
			}
			set("strategy", strategy, "config.strategy")
		}
		opts := map[string]any{}
		if dp.DrainSeconds != nil {
			opts["drain_seconds"] = *dp.DrainSeconds
		}
		if dp.AllowOverlap != nil {
			opts["allow_overlap"] = *dp.AllowOverlap
		}
		if dp.AllowDowntimeOnLowCapacity != nil {
			opts["allow_downtime_on_low_capacity"] = *dp.AllowDowntimeOnLowCapacity
		}
		if len(opts) > 0 {
			set("strategy_options", opts, "config.strategy_options")
		}
		if dp.RetainStopped != nil {
			set("retain_stopped", *dp.RetainStopped, "config.retain_stopped")
		}
		if dp.PreDeploy != nil {
			set("pre_deploy", dp.PreDeploy, "config.pre_deploy")
		}
		if dp.PostDeploy != nil {
			set("post_deploy", dp.PostDeploy, "config.post_deploy")
		}
		if dp.Canary != nil {
			setExtra("canary", dp.Canary)
		}
	}
	if s.Workdir != "" {
		setExtra("workdir", s.Workdir)
	}
	if s.Replicas != nil {
		setExtra("replicas", *s.Replicas)
	}
	if s.Placement != nil {
		setExtra("placement", s.Placement)
	}
	if s.DependsOn != nil {
		setExtra("depends_on", s.DependsOn)
	}
	if s.Cron != nil {
		setExtra("cron", s.Cron)
	}
	if s.RequestBodyLimit != "" {
		n, err := Bytes(s.RequestBodyLimit)
		if err != nil {
			return nil, nil, err
		}
		setExtra("request_body_limit_bytes", n)
	}
	if len(extra) > 0 {
		set("extra", extra, "config.extra")
	}
	if ds.Type == "database" {
		if s.DB == nil {
			return nil, nil, fmt.Errorf("a database needs db.engine")
		}
		if s.DB.ExposeHostPort != 0 {
			set("host_port", s.DB.ExposeHostPort, "config.host_port")
		}
		// A database takes only these keys (the same list as the API). The others are left out with a warning.
		allowed := map[string]bool{"resources": true, "host_port": true, "strategy": true, "stop_grace_period": true, "image": true}
		for k := range ds.Overlay {
			if !allowed[k] {
				warns = append(warns, Warning{Path: where, Message: fmt.Sprintf("config.%s is not applied to a database", k)})
				delete(ds.Overlay, k)
				delete(managed, "config."+k)
			}
		}
		ds.Overlay["strategy"] = "recreate"
		managed["config.strategy"] = true
		ds.SourceType = "image"
	}
	// Defaults of a new image or git service.
	if ds.Type != "database" {
		ds.Defaults["restart_policy"] = "always"
		if port, ok := ds.Overlay["port"]; ok && ds.Overlay["health"] == nil {
			ds.Defaults["health"] = map[string]any{"type": "tcp", "port": port}
		}
	}
	if s.Failover != "" {
		managed["failover"] = true
	}
	if s.AllowDataLossFailover != nil {
		managed["allow_data_loss_failover"] = true
	}
	// Domains
	if has("domains") {
		ds.SetsDomains, managed["domains"] = true, true
		for i, dm := range s.Domains {
			host, err := NormalizeHost(dm.Host)
			if err != nil {
				return nil, nil, fmt.Errorf("domains.%d.host: %w", i, err)
			}
			dd := DesiredDomain{Host: host, PathPrefix: dm.PathPrefix, Port: dm.Port, HTTPS: dm.HTTPS == nil || *dm.HTTPS,
				RedirectWWW: dm.RedirectWWW, IngressNodes: dm.IngressNodes}
			if dd.PathPrefix == "" {
				dd.PathPrefix = "/"
			}
			if dd.Port == 0 && len(s.Ports) == 1 {
				dd.Port = s.Ports[0].Container
			}
			opts := map[string]any{}
			if dm.MaxBody != "" {
				n, err := Bytes(dm.MaxBody)
				if err != nil {
					return nil, nil, err
				}
				opts["max_body_bytes"] = n
			}
			if dm.IPAllow != nil {
				opts["ip_allow"] = dm.IPAllow
			}
			if dm.BasicAuthSecret != "" {
				opts["basic_auth_secret"] = dm.BasicAuthSecret
			}
			dd.Options = opts
			ds.Domains = append(ds.Domains, dd)
		}
	}
	// Volumes
	if has("volumes") {
		ds.SetsVolumes, managed["volumes"] = true, true
		for _, v := range s.Volumes {
			dv := DesiredVolume{Name: v.Name, Path: v.Path, Kind: v.Kind, AccessMode: v.AccessMode, Node: v.Node}
			if dv.Kind == "" {
				dv.Kind = "volume"
			}
			if dv.AccessMode == "" {
				dv.AccessMode = "single_writer"
			}
			if v.SizeLimit != "" {
				n, err := Bytes(v.SizeLimit)
				if err != nil {
					return nil, nil, err
				}
				dv.SizeLimit = n
			}
			ds.Volumes = append(ds.Volumes, dv)
		}
	}
	for k, v := range s.Env {
		ds.Vars[k] = DesiredVar{Value: v.Value, Preview: v.Preview, BuildTime: v.BuildTime}
	}
	for k := range managed {
		ds.Managed = append(ds.Managed, k)
	}
	sort.Strings(ds.Managed)
	return ds, warns, nil
}

func hasSingleWriter(vols []Volume) bool {
	for _, v := range vols {
		if v.Kind != "tmpfs" && v.AccessMode != "shared" {
			return true
		}
	}
	return false
}

func healthConfig(h *Healthcheck, ports []Port) (map[string]any, error) {
	out := map[string]any{"type": h.Type}
	port := h.Port
	if port == 0 && len(ports) > 0 && h.Type != "command" {
		port = ports[0].Container
	}
	if port != 0 {
		out["port"] = port
	}
	if h.Path != "" {
		out["path"] = h.Path
	}
	if h.HostHeader != "" {
		out["host"] = h.HostHeader
	}
	if h.Command != nil {
		out["command"] = h.Command
	}
	if h.ExpectedStatus != "" {
		r, err := StatusRange(h.ExpectedStatus)
		if err != nil {
			return nil, err
		}
		out["expected_status"] = r
	}
	for key, val := range map[string]string{"interval": h.Interval, "timeout": h.Timeout, "start_period": h.StartPeriod, "deploy_timeout": h.DeployTimeout} {
		if val == "" {
			continue
		}
		n, err := Seconds(val)
		if err != nil {
			return nil, err
		}
		out[key] = n
	}
	if h.HealthyThreshold != 0 {
		out["healthy_threshold"] = h.HealthyThreshold
	}
	if h.UnhealthyThreshold != 0 {
		out["unhealthy_threshold"] = h.UnhealthyThreshold
	}
	if h.RestartOnUnhealthy != nil {
		out["restart_on_unhealthy"] = *h.RestartOnUnhealthy
	}
	return out, nil
}
