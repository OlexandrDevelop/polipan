// SPDX-License-Identifier: Apache-2.0

// Package orchestrator is the contract between the HTTP API and the deploy engine.
// The API calls Orchestrator. The control package implements it (nodes, desired state, deployments).
package orchestrator

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Errors the API maps to HTTP answers.
var (
	ErrNotFound      = errors.New("orchestrator: not found")
	ErrNoNode        = errors.New("orchestrator: no ready node")           // 409 conflict, code node_unavailable in details
	ErrDeployRunning = errors.New("orchestrator: a deployment is running") // 409 conflict

	// M3 errors. The API gives each one the error code of the same name (05-api section 2).
	ErrAlreadyLive         = errors.New("orchestrator: the release is live")                                   // 409 already_live
	ErrImagePruned         = errors.New("orchestrator: the image of the release was pruned")                   // 409 release_image_pruned
	ErrSecretDestroyed     = errors.New("orchestrator: a secret version of the release was destroyed")         // 409 secret_version_destroyed
	ErrNotCancellable      = errors.New("orchestrator: the deployment cannot be cancelled now")                // 409 deployment_not_cancellable
	ErrDomainPortMismatch  = errors.New("orchestrator: a domain points to a port the release does not expose") // 409 domain_port_mismatch
	ErrStrategyUnavailable = errors.New("orchestrator: the strategy is not available")                         // 422
	ErrNoLiveRelease       = errors.New("orchestrator: the service has no live container")                     // 409
)

// Warning is a note in a plan or a deployment (zero_downtime_not_available, migration_compat, domain_port_mismatch).
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DeployParams is a request to deploy or to roll back.
type DeployParams struct {
	ServiceID string
	ActorID   string
	// Trigger is manual, git_push, api, rollback ... Empty means manual (rollback when RollbackReleaseID is set).
	Trigger string
	// Strategy is recreate or blue_green. Empty means the service setting, then the default.
	Strategy string
	// DrainSeconds, AllowDowntimeOnLowCapacity and AllowOverlap override the service config for this deployment. Nil = no override.
	DrainSeconds               *int
	AllowDowntimeOnLowCapacity *bool
	AllowOverlap               *bool
	// RollbackReleaseID is the older release to go back to. No build runs.
	RollbackReleaseID string
	// Force accepts the warning domain_port_mismatch of a rollback.
	Force bool
}

// Plan is what a deployment would do (REQ-DEPLOY-007).
type Plan struct {
	Strategy         string
	Forced           bool
	ExpectedDowntime string // none | short
	Nodes            []string
	Warnings         []Warning
}

// HealthResult is the result of one run of the health check (POST health-check:test).
type HealthResult struct {
	Healthy     bool   `json:"healthy"`
	Status      int    `json:"status"`
	BodyExcerpt string `json:"body_excerpt"`
	DurationMS  int    `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}

// JoinToken is a new node join token. The plain token is shown once.
type JoinToken struct {
	Token         string
	ExpiresAt     time.Time
	CAFingerprint string // sha256 of the CA certificate, hex
}

// Orchestrator is what the API needs from the control side.
type Orchestrator interface {
	// Deploy queues a manual or triggered deployment with the service settings. It returns the deployment id at once.
	Deploy(ctx context.Context, serviceID, actorID, trigger string) (deploymentID string, err error)
	// DeployWith queues a deployment or a rollback. A newer queued deployment replaces older queued ones (08 section 10).
	DeployWith(ctx context.Context, p DeployParams) (deploymentID string, warnings []Warning, err error)
	// Plan answers what DeployWith would do, without doing it (dry_run).
	Plan(ctx context.Context, p DeployParams) (Plan, error)
	// Cancel cancels a deployment up to health_checking. In switching and draining it gives ErrNotCancellable.
	Cancel(ctx context.Context, deploymentID, actorID string) error
	// Restart restarts the containers of the live release. No new release is made.
	Restart(ctx context.Context, serviceID string) error
	// TestHealth runs the health check of a live container once.
	TestHealth(ctx context.Context, serviceID string) (HealthResult, error)
	// SetRunning stops (false) or starts (true) the service containers. It changes services.desired_state,
	// the placement role and the desired state of the node. No new Release is made (AC-APP-05).
	SetRunning(ctx context.Context, serviceID string, running bool) error
	// DomainsChanged recomputes the ingress of the nodes after a domain was added, changed or removed.
	// It restarts no container and makes no Release (AC-DOM-03).
	DomainsChanged(ctx context.Context, serviceID string) error
	// ServiceRemoved stops and removes the containers of a deleted service and updates the nodes.
	ServiceRemoved(ctx context.Context, serviceID string) error
	// Logs returns the last tail lines of the live container (docker logs tail, until M8).
	Logs(ctx context.Context, serviceID string, tail int) (string, error)
	// CreateJoinToken makes a single-use token (join_token_ttl) and stores only its hash.
	CreateJoinToken(ctx context.Context, orgID, nodeName string, labels map[string]string, actorID string) (JoinToken, error)
}

// IsPanelHost reports whether host could serve the same name as the panel. Both names are
// expanded like the agent does for redirect_www (the name, "www." + name, and the name without
// one "www."). If the two sets share a name, the host is refused. Service domains must not
// use these hosts: the panel route is not a domain row, and a service route on the same host
// could take the logins of the panel.
func IsPanelHost(externalURL, host string) bool {
	u, err := url.Parse(externalURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	variants := func(h string) []string {
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		return []string{h, "www." + h, strings.TrimPrefix(h, "www.")}
	}
	panel := map[string]bool{}
	for _, v := range variants(u.Hostname()) {
		panel[v] = true
	}
	for _, v := range variants(host) {
		if panel[v] {
			return true
		}
	}
	return false
}

// DataRemover is an optional part of the Orchestrator: it removes the data volumes of a service on its node
// (delete_volumes=true, REQ-DB-009, REQ-TPL-008). The Control implements it.
type DataRemover interface {
	RemoveServiceData(ctx context.Context, serviceID string) error
}
