// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// serviceConfig is the image service config (services.config and releases.config_snapshot).
type serviceConfig struct {
	Image         string   `json:"image"`
	Port          uint32   `json:"port"`
	Command       []string `json:"command"`
	Entrypoint    []string `json:"entrypoint"`
	RestartPolicy string   `json:"restart_policy"`
	Health        *struct {
		Type          string `json:"type"`
		Port          uint32 `json:"port"`
		Path          string `json:"path"`
		DeployTimeout int    `json:"deploy_timeout"` // seconds
	} `json:"health"`
	Strategy string `json:"strategy"`
}

// healthSpec returns the health check of the config. With a port and no health block, it is tcp on the port.
func (c serviceConfig) healthSpec() *pb.HealthCheckSpec {
	typ, port, path := "", c.Port, ""
	if c.Health != nil {
		typ, path = c.Health.Type, c.Health.Path
		if c.Health.Port != 0 {
			port = c.Health.Port
		}
	}
	if typ == "" {
		if port == 0 {
			return nil
		}
		typ = "tcp"
	}
	return &pb.HealthCheckSpec{
		Type: typ, Path: path, Port: port,
		ExpectedStatusMin: 200, ExpectedStatusMax: 399,
		IntervalSeconds: 5, TimeoutSeconds: 3, HealthyThreshold: 1, UnhealthyThreshold: 3,
	}
}

// deployTimeout is health.deploy_timeout in seconds (default 300).
func (c serviceConfig) deployTimeout() uint32 {
	if c.Health != nil && c.Health.DeployTimeout > 0 {
		return uint32(c.Health.DeployTimeout)
	}
	return 300
}

// ContainerName is the container name and network alias of a placement.
func ContainerName(placementID string) string { return "pp-" + placementID }

// buildDesiredState makes the full document of a node from the database.
func (c *Control) buildDesiredState(ctx context.Context, q dbq, nodeID string, version int64) (*pb.DesiredState, error) {
	ds := &pb.DesiredState{Version: version, Ingress: &pb.IngressSpec{AcmeEmail: c.Config.ACMEEmail, AcmeCa: "letsencrypt"}}

	rows, err := q.Query(ctx,
		`SELECT p.id, p.service_id, s.environment_id, p.release_id, p.color, p.role, s.desired_state,
		        r.image_ref, r.config_snapshot
		   FROM placements p
		   JOIN services s ON s.id = p.service_id AND s.deleted_at IS NULL
		   JOIN releases r ON r.id = p.release_id
		  WHERE p.node_id = $1
		  ORDER BY p.created_at, p.id`, nodeID)
	if err != nil {
		return nil, err
	}
	type live struct{ placement, release, color string }
	liveBySvc := map[string][]live{}
	for rows.Next() {
		var pid, sid, eid, rid, color, role, svcState, imageRef string
		var raw []byte
		if err := rows.Scan(&pid, &sid, &eid, &rid, &color, &role, &svcState, &imageRef, &raw); err != nil {
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
			ContainerName: name, ImageRef: imageRef,
			Command: cfg.Command, Entrypoint: cfg.Entrypoint,
			Health:           cfg.healthSpec(),
			StopGraceSeconds: 10,
			RestartPolicy:    cfg.RestartPolicy,
			NetworkAliases:   []string{name},
			Labels: map[string]string{
				"polipan.service": sid, "polipan.placement": pid, "polipan.release": rid, "polipan.managed": "true",
			},
		}
		if w.RestartPolicy == "" {
			w.RestartPolicy = "always"
		}
		if cfg.Port != 0 {
			w.Ports = []*pb.PortSpec{{ContainerPort: cfg.Port, Protocol: "tcp"}}
		}
		ds.Workloads = append(ds.Workloads, w)
		if role == "live" && svcState == "running" {
			liveBySvc[sid] = append(liveBySvc[sid], live{pid, rid, color})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The panel itself goes through Caddy of the node.
	if u, err := url.Parse(c.Config.ExternalURL); err == nil && u.Hostname() != "" {
		ds.Ingress.Routes = append(ds.Ingress.Routes, &pb.Route{
			RouteId: "panel", Hosts: []string{strings.ToLower(u.Hostname())}, PathPrefix: "/",
			Https:     u.Scheme == "https",
			Upstreams: []*pb.Upstream{{Address: c.PanelUpstream, Weight: 100}},
		})
	}

	drows, err := q.Query(ctx,
		`SELECT d.id, d.host, d.path_prefix, d.container_port, d.https, COALESCE(d.redirect_www,''), d.service_id
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
		if err := drows.Scan(&id, &host, &prefix, &port, &https, &redirect, &sid); err != nil {
			return nil, err
		}
		if orchestrator.IsPanelHost(c.Config.ExternalURL, host) {
			continue // old rows: a service never gets the host of the panel
		}
		route := &pb.Route{
			RouteId: id, Hosts: []string{host}, PathPrefix: prefix, Https: https, RedirectWww: redirect, ServiceId: sid,
			Upstreams: []*pb.Upstream{}, // empty: the agent answers 503 (AC-DOM-04)
		}
		for _, l := range liveBySvc[sid] {
			route.Upstreams = append(route.Upstreams, &pb.Upstream{
				Address: fmt.Sprintf("%s:%d", ContainerName(l.placement), port), Weight: 100, ReleaseId: l.release, Color: l.color,
			})
		}
		ds.Ingress.Routes = append(ds.Ingress.Routes, route)
	}
	return ds, drows.Err()
}
