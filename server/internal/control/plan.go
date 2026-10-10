// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/server/internal/orchestrator"
)

// Warning codes (05-api section 2).
const (
	WarnZeroDowntime = "zero_downtime_not_available"
	WarnMigration    = "migration_compat"
	WarnLowCapacity  = "low_capacity_recreate"
)

// svcInfo is the part of a service the deploy engine reads.
type svcInfo struct {
	id, envID, orgID, projectID, name, typ, sourceType string
	protected                                          bool
	envStrategy                                        string // strategy of the environment settings (REQ-DEPLOY-005)
	raw                                                []byte
	cfg                                                serviceConfig
	liveRelease                                        *string
}

func (c *Control) loadService(ctx context.Context, q dbq, serviceID string) (*svcInfo, error) {
	var s svcInfo
	var deleted any
	err := q.QueryRow(ctx, `SELECT s.id, s.environment_id, p.org_id, p.id, s.name, s.type, s.source_type, e.protected, COALESCE(e.settings->>'strategy',''), s.config, s.live_release_id, s.deleted_at
		FROM services s JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id WHERE s.id = $1`, serviceID).
		Scan(&s.id, &s.envID, &s.orgID, &s.projectID, &s.name, &s.typ, &s.sourceType, &s.protected, &s.envStrategy, &s.raw, &s.liveRelease, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted != nil) {
		return nil, orchestrator.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(s.raw, &s.cfg); err != nil {
		return nil, err
	}
	return &s, nil
}

// deployOptions are the strategy options of one deployment (openapi StrategyOptions).
type deployOptions struct {
	DrainSeconds               int  `json:"drain_seconds"`
	AllowDowntimeOnLowCapacity bool `json:"allow_downtime_on_low_capacity"`
	AllowOverlap               bool `json:"allow_overlap"`
	// M7: where a move or a failover starts from, and how the data of a volume goes along.
	MoveFrom     string   `json:"move_from,omitempty"`
	FailoverFrom []string `json:"failover_from,omitempty"`
	VolumeCopy   string   `json:"volume_copy,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	// M8 canary: the steps in percent (the last is 100), the wait of a step, and the limits of the gate.
	CanarySteps               []int   `json:"canary_steps,omitempty"`
	CanaryStepIntervalSeconds int     `json:"canary_step_interval_seconds,omitempty"`
	CanaryMax5xxRate          float64 `json:"canary_max_5xx_rate,omitempty"`
	CanaryMaxP95Ratio         float64 `json:"canary_max_p95_ratio,omitempty"`
}

func resolveOptions(cfg serviceConfig, p orchestrator.DeployParams) deployOptions {
	o := deployOptions{DrainSeconds: cfg.drainSeconds(), AllowDowntimeOnLowCapacity: cfg.StrategyOptions.AllowDowntimeOnLowCapacity, AllowOverlap: cfg.allowOverlap()}
	if p.DrainSeconds != nil && *p.DrainSeconds >= 0 {
		o.DrainSeconds = *p.DrainSeconds
	}
	if p.AllowDowntimeOnLowCapacity != nil {
		o.AllowDowntimeOnLowCapacity = *p.AllowDowntimeOnLowCapacity
	}
	if p.AllowOverlap != nil {
		o.AllowOverlap = *p.AllowOverlap
	}
	o.MoveFrom, o.FailoverFrom, o.VolumeCopy, o.Cause = p.MoveFrom, p.FailoverFrom, p.VolumeCopy, p.Cause
	o.resolveCanary(cfg, p)
	return o
}

// Defaults of the canary (glossary).
const (
	defaultCanaryInterval = 300
	defaultCanaryMax5xx   = 0.02
	defaultCanaryP95Ratio = 1.5
)

// resolveCanary sets the canary options: the default, then the service config, then the request.
func (o *deployOptions) resolveCanary(cfg serviceConfig, p orchestrator.DeployParams) {
	steps := []int{10, 25, 50, 100}
	if len(cfg.Canary.Steps) > 0 {
		steps = cfg.Canary.Steps
	}
	if len(p.CanarySteps) > 0 {
		steps = p.CanarySteps
	}
	o.CanarySteps = cleanSteps(steps)
	o.CanaryStepIntervalSeconds = defaultCanaryInterval
	if d, err := time.ParseDuration(cfg.Canary.StepInterval); err == nil && d >= time.Second {
		o.CanaryStepIntervalSeconds = int(d.Seconds())
	}
	if p.CanaryStepIntervalSeconds != nil && *p.CanaryStepIntervalSeconds >= 1 {
		o.CanaryStepIntervalSeconds = *p.CanaryStepIntervalSeconds
	}
	o.CanaryMax5xxRate = defaultCanaryMax5xx
	if cfg.Canary.Max5xxRate != nil && *cfg.Canary.Max5xxRate >= 0 {
		o.CanaryMax5xxRate = *cfg.Canary.Max5xxRate
	}
	if p.CanaryMax5xxRate != nil && *p.CanaryMax5xxRate >= 0 {
		o.CanaryMax5xxRate = *p.CanaryMax5xxRate
	}
	o.CanaryMaxP95Ratio = defaultCanaryP95Ratio
	if cfg.Canary.MaxP95Ratio != nil && *cfg.Canary.MaxP95Ratio >= 1 {
		o.CanaryMaxP95Ratio = *cfg.Canary.MaxP95Ratio
	}
	if p.CanaryMaxP95Ratio != nil && *p.CanaryMaxP95Ratio >= 1 {
		o.CanaryMaxP95Ratio = *p.CanaryMaxP95Ratio
	}
}

// cleanSteps sorts the steps, drops the ones outside 1 to 100 and the doubles, and makes sure the last one is 100.
func cleanSteps(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, s := range in {
		if s >= 1 && s <= 100 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Ints(out)
	if len(out) == 0 || out[len(out)-1] != 100 {
		out = append(out, 100)
	}
	return out
}

// plan decides the strategy of a deployment (08 section 3). cfg is the config the new containers get
// (the release config for a rollback).
func (c *Control) plan(ctx context.Context, s *svcInfo, cfg serviceConfig, p orchestrator.DeployParams, opts deployOptions) (orchestrator.Plan, error) {
	plan := orchestrator.Plan{ExpectedDowntime: "none", Nodes: []string{}}
	requested := p.Strategy // the request wins, then the service, then the environment (REQ-DEPLOY-005)
	if requested == "" {
		requested = cfg.Strategy
	}
	if requested == "" {
		requested = s.envStrategy
	}
	if requested != "" && requested != "recreate" && requested != "blue_green" && requested != "canary" {
		return plan, orchestrator.ErrStrategyUnavailable
	}

	var hasDomain, singleWriter bool
	localVolume := false
	if p.Trigger == "move" || p.Trigger == "failover" {
		var err error
		if localVolume, err = c.hasLocalVolume(ctx, c.DB, s.id); err != nil {
			return plan, err
		}
	}
	if err := c.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE service_id = $1),
		EXISTS (SELECT 1 FROM volumes WHERE service_id = $1 AND kind <> 'tmpfs' AND access_mode = 'single_writer')`, s.id).Scan(&hasDomain, &singleWriter); err != nil {
		return plan, err
	}
	// Forced recreate (REQ-DEPLOY-104): the first reason is the one shown.
	var reason string
	switch {
	case localVolume:
		reason = "the data of the volume is copied to the other node while the service is stopped"
	case singleWriter:
		reason = "the service has a volume with one writer"
	case s.typ == "database":
		reason = "the service is a database"
	case !hasDomain && cfg.Port == 0 && (s.typ != "worker" || !opts.AllowOverlap):
		reason = "the service has no domain and no port"
	case cfg.HostPort != 0:
		reason = "the service publishes a host port"
	}
	strategy := requested
	switch {
	case reason != "":
		strategy = "recreate"
		plan.Forced = true
		plan.Warnings = append(plan.Warnings, orchestrator.Warning{Code: WarnZeroDowntime,
			Message: "Zero downtime is not available: " + reason + ". The old container stops before the new one starts."})
	case strategy == "":
		strategy = "blue_green"
	}
	// A move, a failover and a rollback use blue_green, unless the request asks for a canary (08 section 9).
	if strategy == "canary" && (p.Trigger == "move" || p.Trigger == "failover" || (p.Trigger == "rollback" && p.Strategy != "canary")) {
		strategy = "blue_green"
	}
	// The traffic split of a canary happens in Caddy, so the service needs a domain (08 section 3).
	if strategy == "canary" && !hasDomain {
		return plan, orchestrator.ErrStrategyUnavailable
	}
	// A failover has no old container to drain: one replica is a recreate, more are blue-green (09 section 9).
	if p.Trigger == "failover" && strategy == "blue_green" && cfg.replicas() == 1 {
		strategy = "recreate"
	}
	if strategy == "recreate" {
		plan.ExpectedDowntime = "short"
	}
	if strategy != "recreate" && len(cfg.PreDeploy) > 0 {
		plan.Warnings = append(plan.Warnings, orchestrator.Warning{Code: WarnMigration,
			Message: "pre_deploy runs while the old version still serves. A migration must work with the old and the new code."})
	}
	plan.Strategy = strategy
	if p.TargetNodeID != "" {
		plan.Nodes = []string{p.TargetNodeID}
	} else if n, err := c.pickNodeForService(ctx, s.id); err == nil {
		plan.Nodes = []string{n.id}
	}
	return plan, nil
}
