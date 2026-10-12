// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/obs"
	"github.com/polipan/polipan/server/internal/store"
)

// failureLogLines is the number of log lines in the failure context of a service.
const failureLogLines = 20

// lastFailure tells why a service failed (12-observability section 5.1): the reason of the last failed deployment,
// or "unhealthy" for an unhealthy container; the probable cause on the node when an incident links it; and the last
// log lines, with the secrets masked. Nil when the service has no failure. A part that cannot be read is left out.
func (h *Handlers) lastFailure(r *http.Request, s *store.Service) *gen.ServiceFailure {
	ctx := r.Context()
	log := logging.From(ctx)
	var f gen.ServiceFailure
	var depID, state, reason, detail string
	err := h.Store.Pool.QueryRow(ctx, `SELECT id, state, COALESCE(failure_reason,''), COALESCE(failure_detail,'') FROM deployments
		WHERE service_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, s.ID).Scan(&depID, &state, &reason, &detail)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		log.Warn("service failure: deployment", "service_id", s.ID, "error", err)
		return nil
	}
	unhealthy := false
	for _, p := range s.Placements {
		if p.Role == "live" && p.Health == "unhealthy" {
			unhealthy = true
		}
	}
	switch {
	case state == "failed":
		f.Reason, f.DeploymentId = ptr(reason), ptr(depID)
		if detail != "" {
			f.Detail = ptr(detail)
		}
	case unhealthy || s.Status == "failed" || s.Status == "degraded":
		f.Reason = ptr("unhealthy")
	default:
		return nil
	}
	masker, err := h.maskerFor(r, s.ID)
	if err != nil {
		return &f // no lines rather than lines with secrets
	}
	var lines []string
	if f.DeploymentId != nil {
		lines, err = deploymentTail(ctx, h.Store.Pool, depID, failureLogLines)
		f.LogSource = ptr(gen.ServiceFailureLogSourceDeployment)
	} else {
		lines, err = h.containerTail(ctx, s, failureLogLines)
		f.LogSource = ptr(gen.ServiceFailureLogSourceContainer)
	}
	if err != nil {
		log.Warn("service failure: logs", "service_id", s.ID, "error", err)
	}
	for i := range lines {
		lines[i] = masker.Mask(lines[i])
	}
	f.LogExcerpt = &lines
	if err := h.failureIncident(ctx, s, &f); err != nil {
		log.Warn("service failure: incident", "service_id", s.ID, "error", err)
	}
	return &f
}

// deploymentTail returns the last n lines of a deployment log, oldest first.
func deploymentTail(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, depID string, n int) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT line FROM deployment_logs WHERE deployment_id = $1 ORDER BY seq DESC LIMIT $2`, depID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append([]string{l}, out...)
	}
	return out, rows.Err()
}

// containerTail returns the last n lines of the containers of a service: from the log store, else from
// `docker logs` of the live container. It waits at most 3 seconds, so the service page stays fast.
func (h *Handlers) containerTail(ctx context.Context, s *store.Service, n int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if h.Obs.LogsEnabled() {
		got, err := h.Obs.Recent(ctx, h.Clock.Now(), obs.LogFilter{OrgID: s.OrgID, ServiceID: s.ID}, n)
		if err == nil {
			out := make([]string, 0, len(got))
			for _, l := range got {
				out = append(out, l.Message)
			}
			return out, nil
		}
	}
	if h.Orch == nil {
		return []string{}, nil
	}
	text, err := h.Orch.Logs(ctx, s.ID, n)
	if err != nil {
		return []string{}, err
	}
	out := []string{}
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// failureIncident adds the incident of the service: an open one, or one that ended in the last hour, that has an
// alert of the service or is open on a node of the service. The root cause is given only when it is not an alert
// of this service: then it is the correlated cause on the node.
func (h *Handlers) failureIncident(ctx context.Context, s *store.Service, f *gen.ServiceFailure) error {
	var incID, nodeID, nodeName, rootCause, rootType, rootService string
	err := h.Store.Pool.QueryRow(ctx, `SELECT inc.id, inc.node_id, COALESCE(n.name,''), COALESCE(inc.root_cause,''), COALESCE(inc.root_type,''),
		COALESCE(ri.details->>'service_id','')
		FROM incidents inc LEFT JOIN nodes n ON n.id = inc.node_id LEFT JOIN alert_instances ri ON ri.id = inc.root_instance_id
		WHERE inc.org_id = $1 AND (inc.state = 'open' OR inc.resolved_at > $3)
		  AND (EXISTS (SELECT 1 FROM alert_instances i WHERE i.incident_id = inc.id AND i.details->>'service_id' = $2)
		       OR (inc.state = 'open' AND inc.node_id IN (SELECT node_id FROM placements WHERE service_id = $2)))
		ORDER BY (inc.state = 'open') DESC, inc.opened_at DESC LIMIT 1`, s.OrgID, s.ID, h.Clock.Now().Add(-time.Hour)).
		Scan(&incID, &nodeID, &nodeName, &rootCause, &rootType, &rootService)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	f.IncidentId, f.NodeId = ptr(incID), ptr(nodeID)
	if nodeName != "" {
		f.NodeName = ptr(nodeName)
	}
	if rootCause != "" && rootService != s.ID {
		f.RootCause, f.RootType = ptr(rootCause), ptr(rootType)
	}
	return nil
}
