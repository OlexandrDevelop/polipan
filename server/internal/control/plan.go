// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"

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
	return o
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
	if requested == "canary" {
		return plan, orchestrator.ErrStrategyUnavailable
	}
	if requested != "" && requested != "recreate" && requested != "blue_green" {
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
	// A failover has no old container to drain: one replica is a recreate, more are blue-green (09 section 9).
	if p.Trigger == "failover" && strategy == "blue_green" && cfg.replicas() == 1 {
		strategy = "recreate"
	}
	if strategy == "recreate" {
		plan.ExpectedDowntime = "short"
	}
	if strategy == "blue_green" && len(cfg.PreDeploy) > 0 {
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
