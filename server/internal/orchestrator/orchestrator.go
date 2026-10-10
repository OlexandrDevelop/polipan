// SPDX-License-Identifier: Apache-2.0

// Package orchestrator is the contract between the HTTP API and the deploy engine.
// The API calls Orchestrator. The control package implements it (nodes, desired state, deployments).
package orchestrator

import (
	"context"
	"errors"
	"time"
)

// Errors the API maps to HTTP answers.
var (
	ErrNotFound      = errors.New("orchestrator: not found")
	ErrNoNode        = errors.New("orchestrator: no ready node")           // 409 conflict, code node_unavailable in details
	ErrDeployRunning = errors.New("orchestrator: a deployment is running") // 409 conflict
)

// JoinToken is a new node join token. The plain token is shown once.
type JoinToken struct {
	Token         string
	ExpiresAt     time.Time
	CAFingerprint string // sha256 of the CA certificate, hex
}

// Orchestrator is what the API needs from the control side.
type Orchestrator interface {
	// Deploy creates a Release from the service config and starts a recreate Deployment
	// in the background. It returns the deployment id at once (state queued).
	Deploy(ctx context.Context, serviceID, actorID, trigger string) (deploymentID string, err error)
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
