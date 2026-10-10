// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/polipan/polipan/api/gen"
	"github.com/polipan/polipan/server/internal/store"
)

func nodeJSON(n *store.Node) gen.Node {
	state := gen.NodeState(n.State)
	out := gen.Node{Id: &n.ID, Name: &n.Name, State: &state, PublicIp: n.PublicIP, MeshIp: n.MeshIP, Os: n.OS, Arch: n.Arch,
		DockerVersion: n.DockerVersion, AgentVersion: n.AgentVersion, LastHeartbeatAt: n.LastHeartbeatAt, WorkloadCount: &n.WorkloadCount}
	var labels map[string]string
	if json.Unmarshal(n.Labels, &labels) == nil && labels != nil {
		out.Labels = &labels
	}
	if len(n.Capacity) > 0 {
		var c struct {
			CPUMillis   *int `json:"cpu_millis"`
			MemoryBytes *int `json:"memory_bytes"`
			DiskBytes   *int `json:"disk_bytes"`
		}
		if json.Unmarshal(n.Capacity, &c) == nil {
			out.Capacity = &struct {
				CpuMillis   *int `json:"cpu_millis,omitempty"`
				DiskBytes   *int `json:"disk_bytes,omitempty"`
				MemoryBytes *int `json:"memory_bytes,omitempty"`
			}{CpuMillis: c.CPUMillis, DiskBytes: c.DiskBytes, MemoryBytes: c.MemoryBytes}
		}
	}
	return out
}

// ListNodes lists the nodes of the org.
func (h *Handlers) ListNodes(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.ListNodes(r.Context(), h.orgID(r))
	if fail(w, r, err) {
		return
	}
	out := make([]gen.Node, len(list))
	for i := range list {
		out[i] = nodeJSON(&list[i])
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetNode returns a node.
func (h *Handlers) GetNode(w http.ResponseWriter, r *http.Request, nodeID gen.NodeId) {
	n, err := h.Store.GetNode(r.Context(), h.orgID(r), nodeID)
	if fail(w, r, err) {
		return
	}
	WriteJSON(w, http.StatusOK, nodeJSON(n))
}

var safeHostRe = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$`)

// installCommand builds the command line for the new node. The host comes from external_url.
// Without it, the Host header is used, but only when it has safe characters.
func (h *Handlers) installCommand(r *http.Request, token, fingerprint string) (string, bool) {
	scheme, hostport := "https", r.Host
	if h.Settings.ExternalURL != "" {
		u, err := url.Parse(h.Settings.ExternalURL)
		if err != nil || u.Host == "" {
			return "", false
		}
		scheme, hostport = u.Scheme, u.Host
	}
	if !safeHostRe.MatchString(hostport) {
		return "", false
	}
	agentPort := "9443"
	if _, p, err := net.SplitHostPort(h.Settings.AgentListen); err == nil && p != "" {
		agentPort = p
	}
	host := hostOnly(hostport)
	return "curl -fsSL " + scheme + "://" + hostport + "/install-agent.sh | sh -s -- --server " + host + ":" + agentPort +
		" --token " + token + " --ca-fingerprint " + fingerprint, true
}

// CreateNodeJoinToken makes a join token. The token is in the answer once, with the install command.
func (h *Handlers) CreateNodeJoinToken(w http.ResponseWriter, r *http.Request) {
	var body gen.CreateNodeJoinTokenJSONBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if !nodeNameRe.MatchString(name) {
		invalid(w, r, "name", "name must use lower case letters, digits and dashes (1 to 63 characters)")
		return
	}
	labels := map[string]string{}
	if body.Labels != nil {
		labels = *body.Labels
	}
	if len(labels) > 20 {
		invalid(w, r, "labels", "a node can have at most 20 labels")
		return
	}
	p := PrincipalFrom(r.Context())
	used, err := h.Store.NodeNameUsed(r.Context(), p.OrgID, name)
	if fail(w, r, err) {
		return
	}
	if used {
		WriteError(w, r, http.StatusConflict, CodeNameConflict, "a node with this name exists", map[string]any{"field": "name"})
		return
	}
	jt, err := h.Orch.CreateJoinToken(r.Context(), p.OrgID, name, labels, p.User.ID)
	if h.orchestratorFailed(w, r, err) {
		return
	}
	cmd, ok := h.installCommand(r, jt.Token, jt.CAFingerprint)
	if !ok {
		internalError(w, r, errBadExternalURL)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"token": jt.Token, "install_command": cmd, "expires_at": jt.ExpiresAt,
	})
}
