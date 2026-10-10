// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"encoding/json"
)

// AuditEvent is one audit record (REQ-AUDIT-002). Diff must never hold a secret value:
// use mask.Replacement ("***") for values.
//
// The hash-chained audit_events table comes with M5. Until then the records go to the
// events table with topic "audit", so the writes of variables, secrets and volumes,
// secret.use and the key rotations are kept.
type AuditEvent struct {
	OrgID      string
	ActorType  string // user, system
	ActorID    string
	Action     string // e.g. secret.write, secret.use, variable.update
	TargetType string
	TargetID   string
	ProjectID  string
	Diff       any
	IP         string
	RequestID  string
}

// WriteAudit stores an audit record in the transaction q.
func WriteAudit(ctx context.Context, q Querier, e AuditEvent) error {
	if e.ActorType == "" {
		e.ActorType = "user"
	}
	payload, err := json.Marshal(map[string]any{
		"actor_type": e.ActorType, "actor_id": e.ActorID, "action": e.Action,
		"target_type": e.TargetType, "target_id": e.TargetID, "project_id": e.ProjectID,
		"diff": e.Diff, "ip": e.IP, "request_id": e.RequestID,
	})
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO events (org_id, topic, object_id, payload) VALUES ($1, 'audit', $2, $3)`,
		e.OrgID, e.TargetID, payload)
	return err
}
