// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/store"
)

var labelKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]{0,61}[A-Za-z0-9])?$`)

// nodes returns the node actions of the orchestrator. Without them the operation answers 500.
func (h *Handlers) nodes(w http.ResponseWriter, r *http.Request) (orchestrator.NodeManager, bool) {
	nm, ok := h.Orch.(orchestrator.NodeManager)
	if !ok {
		internalError(w, r, errors.New("the orchestrator has no node actions"))
	}
	return nm, ok
}

// nodeAction runs an action and answers with the node.
func (h *Handlers) nodeAction(w http.ResponseWriter, r *http.Request, nodeID string, status int, do func(nm orchestrator.NodeManager, orgID, actorID string) error) {
	nm, ok := h.nodes(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID); fail(w, r, err) {
		return
	}
	if h.orchestratorFailed(w, r, do(nm, h.orgID(r), PrincipalFrom(r.Context()).User.ID)) {
		return
	}
	n, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, status, nodeJSON(n))
}

// CordonNode stops scheduling on the node.
func (h *Handlers) CordonNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	h.nodeAction(w, r, nodeID, http.StatusOK, func(nm orchestrator.NodeManager, org, actor string) error {
		return nm.CordonNode(r.Context(), org, nodeID, actor)
	})
}

// UncordonNode allows scheduling on the node again.
func (h *Handlers) UncordonNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	h.nodeAction(w, r, nodeID, http.StatusOK, func(nm orchestrator.NodeManager, org, actor string) error {
		return nm.UncordonNode(r.Context(), org, nodeID, actor)
	})
}

// DrainNode moves all workloads away. The answer is 202: the node is draining and the moves go on.
func (h *Handlers) DrainNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	h.nodeAction(w, r, nodeID, http.StatusAccepted, func(nm orchestrator.NodeManager, org, actor string) error {
		return nm.DrainNode(r.Context(), org, nodeID, actor)
	})
}

// SetNodeMaintenance puts a drained node into maintenance, or takes it out.
func (h *Handlers) SetNodeMaintenance(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	var body gen.SetNodeMaintenanceJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	h.nodeAction(w, r, nodeID, http.StatusOK, func(nm orchestrator.NodeManager, org, actor string) error {
		return nm.SetMaintenance(r.Context(), org, nodeID, body.Enabled, actor)
	})
}

// DeleteNode deletes a node. A node with workloads needs force=true (409 node_not_empty).
func (h *Handlers) DeleteNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId, params gen.DeleteNodeParams) {
	nm, ok := h.nodes(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID); fail(w, r, err) {
		return
	}
	force := params.Force != nil && *params.Force
	if h.orchestratorFailed(w, r, nm.DeleteNode(r.Context(), h.orgID(r), nodeID, force, PrincipalFrom(r.Context()).User.ID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateNode changes the name or the labels. The labels decide which nodes serve ingress and which can run a service.
func (h *Handlers) UpdateNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	var body gen.UpdateNodeJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	nm, ok := h.nodes(w, r)
	if !ok {
		return
	}
	var name *string
	if body.Name != nil {
		v := strings.TrimSpace(*body.Name)
		if !nodeNameRe.MatchString(v) {
			invalid(w, r, "name", "name must use lower case letters, digits and dashes (1 to 63 characters)")
			return
		}
		name = &v
	}
	var labels map[string]string
	if body.Labels != nil {
		labels = *body.Labels
		if len(labels) > 20 {
			invalid(w, r, "labels", "a node can have at most 20 labels")
			return
		}
		for k, v := range labels {
			if !labelKeyRe.MatchString(k) || len(v) > 63 {
				invalid(w, r, "labels", "a label key has letters, digits, dot, dash, underscore or slash, and a value has at most 63 characters")
				return
			}
		}
	}
	n, err := h.Store.UpdateNode(r.Context(), h.orgID(r), nodeID, name, labels)
	if unique, _ := store.IsUnique(err); unique {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a node with this name exists", map[string]any{"field": "name"})
		return
	}
	if fail(w, r, err) {
		return
	}
	if err := nm.NodesChanged(r.Context()); err != nil {
		internalError(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, nodeJSON(n))
}

// RunNodeSelfCheck asks the agent to test Docker, Caddy and WireGuard.
func (h *Handlers) RunNodeSelfCheck(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	nm, ok := h.nodes(w, r)
	if !ok {
		return
	}
	res, err := nm.SelfCheck(r.Context(), h.orgID(r), nodeID)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// MoveService moves a service to another node (REQ-NODE-006).
func (h *Handlers) MoveService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.MoveServiceParams) {
	id, replayed := h.startIdempotent(w, r, params.IdempotencyKey)
	if replayed {
		return
	}
	var body gen.MoveServiceJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	nm, ok := h.nodes(w, r)
	if !ok {
		return
	}
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:move", s.EnvProtected) {
		return
	}
	if _, err := h.Store.GetNode(r.Context(), h.orgID(r), body.TargetNodeId); err != nil {
		invalid(w, r, "target_node_id", "the target node does not exist")
		return
	}
	copyMode := ""
	if body.VolumeCopy != nil {
		copyMode = string(*body.VolumeCopy)
	}
	switch copyMode {
	case "", "backup":
	case "direct":
		invalid(w, r, "volume_copy", "direct copy between nodes is not available in this version: use backup")
		return
	default:
		invalid(w, r, "volume_copy", "volume_copy must be backup or direct")
		return
	}
	depID, _, err := nm.MoveService(r.Context(), serviceID, body.TargetNodeId, copyMode, PrincipalFrom(r.Context()).User.ID)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	d, err := h.Store.GetDeployment(r.Context(), h.orgID(r), depID)
	if fail(w, r, err) {
		return
	}
	out := deploymentJSON(d)
	h.saveIdempotent(r, id, http.StatusAccepted, out)
	w.Header().Set("Location", BasePath+"/deployments/"+depID)
	WriteJSON(w, http.StatusAccepted, out)
}

// nodeDetail adds the workloads of the node to the answer of getNode.
func (h *Handlers) nodeDetail(r *http.Request, n *store.Node) gen.Node {
	out := nodeJSON(n)
	list, err := h.Store.NodeWorkloads(r.Context(), n.ID)
	if err != nil {
		return out
	}
	ws := make([]gen.NodeWorkload, len(list))
	for i, w := range list {
		role, health := gen.NodeWorkloadRole(w.Role), gen.NodeWorkloadHealth(w.Health)
		ws[i] = gen.NodeWorkload{PlacementId: ptr(w.PlacementID), ServiceId: ptr(w.ServiceID), ServiceName: ptr(w.ServiceName),
			EnvironmentId: ptr(w.EnvironmentID), Role: &role, Health: &health}
	}
	out.Workloads = &ws
	return out
}

// nodeUsage reads the usage numbers of the heartbeat.
func nodeUsage(raw json.RawMessage) *struct {
	CpuMillis   *int `json:"cpu_millis,omitempty"`
	DiskBytes   *int `json:"disk_bytes,omitempty"`
	MemoryBytes *int `json:"memory_bytes,omitempty"`
} {
	if len(raw) == 0 {
		return nil
	}
	var u struct {
		CPU  *int `json:"cpu_millis"`
		Mem  *int `json:"memory_bytes"`
		Disk *int `json:"disk_bytes"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	return &struct {
		CpuMillis   *int `json:"cpu_millis,omitempty"`
		DiskBytes   *int `json:"disk_bytes,omitempty"`
		MemoryBytes *int `json:"memory_bytes,omitempty"`
	}{CpuMillis: u.CPU, DiskBytes: u.Disk, MemoryBytes: u.Mem}
}

// failoverStatus is the failover part of the service answer, and the time of the last backup.
func (h *Handlers) failoverStatus(ctx context.Context, out gen.Service, serviceID string) gen.Service {
	f, err := h.Store.GetFailoverInfo(ctx, serviceID)
	if err != nil {
		return out
	}
	st := gen.FailoverStatusState(f.State)
	fs := &gen.FailoverStatus{State: &st, DataLostSince: f.DataLostSince, At: f.At}
	if f.Reason != "" {
		fs.Reason = &f.Reason
	}
	out.FailoverStatus = fs
	if f.HasVolume {
		out.LastBackupAt = f.LastBackupAt
	}
	return out
}
