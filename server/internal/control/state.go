// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

type liveReplica struct{ placement, release, color, health string }

// routable keeps the live replicas that get traffic. An unhealthy replica is removed from the
// upstreams (REQ-HC-007). When all replicas are unhealthy, all stay: a bad answer is better than none (REQ-HC-008).
func routable(all []liveReplica) []liveReplica {
	var ok []liveReplica
	for _, l := range all {
		if l.health != "unhealthy" {
			ok = append(ok, l)
		}
	}
	if len(ok) == 0 {
		return all
	}
	return ok
}

// ContainerName is the container name and network alias of a placement.
func ContainerName(placementID string) string { return "pp-" + placementID }

// buildDesiredState makes the full document of a node from the database.
func (c *Control) buildDesiredState(ctx context.Context, q dbq, nodeID string, version int64) (*pb.DesiredState, error) {
	ds := &pb.DesiredState{Version: version, Ingress: &pb.IngressSpec{AcmeEmail: c.Config.ACMEEmail, AcmeCa: "letsencrypt"}}

	rows, err := q.Query(ctx,
		`SELECT p.id, p.service_id, s.environment_id, p.release_id, p.color, p.role, p.health, s.desired_state,
		        r.image_ref, r.config_snapshot
		   FROM placements p
		   JOIN services s ON s.id = p.service_id AND s.deleted_at IS NULL
		   JOIN releases r ON r.id = p.release_id
		  WHERE p.node_id = $1
		  ORDER BY p.created_at, p.id`, nodeID)
	if err != nil {
		return nil, err
	}
	type live = liveReplica
	liveBySvc := map[string][]live{}
	for rows.Next() {
		var pid, sid, eid, rid, color, role, health, svcState, imageRef string
		var raw []byte
		if err := rows.Scan(&pid, &sid, &eid, &rid, &color, &role, &health, &svcState, &imageRef, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var cfg serviceConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			rows.Close()
			return nil, fmt.Errorf("release config of %s: %w", rid, err)
		}
		name := ContainerName(pid)
		w := &pb.WorkloadSpec{
			PlacementId: pid, ServiceId: sid, EnvironmentId: eid, ReleaseId: rid, Color: color, Role: role,
			ContainerName: name, ImageRef: imageRef, RegistryAuthRef: c.registryAuthRef(imageRef),
			Command: cfg.Command, Entrypoint: cfg.Entrypoint,
			Health:           cfg.healthSpec(),
			StopGraceSeconds: uint32(cfg.stopGraceSeconds()),
			RestartPolicy:    cfg.RestartPolicy,
			NetworkAliases:   []string{name, dbengine.Alias(sid)},
			Resources:        cfg.resourceSpec(),
			Labels: map[string]string{
				"polipan.service": sid, "polipan.placement": pid, "polipan.release": rid, "polipan.managed": "true",
			},
		}
		if w.RestartPolicy == "" {
			w.RestartPolicy = "always"
		}
		if cfg.Port != 0 {
			w.Ports = []*pb.PortSpec{{ContainerPort: cfg.Port, Protocol: "tcp", HostPort: cfg.HostPort}}
		}
		ds.Workloads = append(ds.Workloads, w)
		if role == "live" && svcState == "running" {
			liveBySvc[sid] = append(liveBySvc[sid], live{pid, rid, color, health})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Env, secrets and mounts of each release (M2). This runs after rows is closed: one query at a time.
	// A kept stopped container needs no env: a secret of an old release may be destroyed (REQ-REL-008).
	for _, w := range ds.Workloads {
		if err := c.fillWorkload(ctx, q, nodeID, w); err != nil {
			if w.Role != "stopped" {
				return nil, err
			}
			c.Log.Warn("stopped workload without env", "placement", w.PlacementId, "err", err)
		}
	}

	c.addRegistryAuth(ctx, nodeID, ds) // pull credential of the built-in registry (build.go)

	// The panel itself goes through Caddy of the node.
	if u, err := url.Parse(c.Config.ExternalURL); err == nil && u.Hostname() != "" {
		ds.Ingress.Routes = append(ds.Ingress.Routes, &pb.Route{
			RouteId: "panel", Hosts: []string{strings.ToLower(u.Hostname())}, PathPrefix: "/",
			Https:     u.Scheme == "https",
			Upstreams: []*pb.Upstream{{Address: c.PanelUpstream, Weight: 100}},
		})
	}

	drows, err := q.Query(ctx,
		`SELECT d.id, d.host, d.path_prefix, d.container_port, d.https, COALESCE(d.redirect_www,''), d.service_id, s.config
		   FROM domains d JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
		  WHERE d.ingress_node_ids IS NULL OR $1 = ANY(d.ingress_node_ids)
		  ORDER BY d.created_at, d.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var id, host, prefix, redirect, sid string
		var port int
		var https bool
		var svcRaw []byte
		if err := drows.Scan(&id, &host, &prefix, &port, &https, &redirect, &sid, &svcRaw); err != nil {
			return nil, err
		}
		var svcCfg serviceConfig
		_ = json.Unmarshal(svcRaw, &svcCfg)
		if orchestrator.IsPanelHost(c.Config.ExternalURL, host) {
			continue // old rows: a service never gets the host of the panel
		}
		route := &pb.Route{
			RouteId: id, Hosts: []string{host}, PathPrefix: prefix, Https: https, RedirectWww: redirect, ServiceId: sid,
			Upstreams:               []*pb.Upstream{}, // empty: the agent answers 503 (AC-DOM-04)
			StreamCloseDelaySeconds: uint32(svcCfg.drainSeconds()),
		}
		for _, l := range routable(liveBySvc[sid]) {
			route.Upstreams = append(route.Upstreams, &pb.Upstream{
				Address: fmt.Sprintf("%s:%d", ContainerName(l.placement), port), Weight: 100, ReleaseId: l.release, Color: l.color,
			})
		}
		ds.Ingress.Routes = append(ds.Ingress.Routes, route)
	}
	return ds, drows.Err()
}
