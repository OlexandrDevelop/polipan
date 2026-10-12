// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/dbengine"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

type liveReplica struct{ placement, release, color, health, role string }

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

// ServiceName is the stable network alias <service>.<env> of a service. With the DNS zone it gives the documented
// name <service>.<env>.polipan.internal.
func ServiceName(service, envSlug string) string {
	return strings.ToLower(service + "." + envSlug)
}

// buildDesiredState makes the full document of a node from the database.
func (c *Control) buildDesiredState(ctx context.Context, q dbq, nodeID string, version int64) (*pb.DesiredState, error) {
	ds := &pb.DesiredState{Version: version, Ingress: &pb.IngressSpec{AcmeEmail: c.Config.ACMEEmail, AcmeCa: "letsencrypt"}, MeshCidr: c.Config.MeshCIDR}

	var orgID, nodeState string
	var nodeLabels []byte
	if err := q.QueryRow(ctx, `SELECT org_id, state, labels FROM nodes WHERE id = $1`, nodeID).Scan(&orgID, &nodeState, &nodeLabels); err != nil {
		return nil, err
	}
	var err error
	if ds.Peers, err = gateway.MeshPeers(ctx, q, orgID, nodeID, c.Config.WireguardPort); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx,
		`SELECT p.id, p.service_id, s.environment_id, p.release_id, p.color, p.role, p.health, s.desired_state,
		        r.image_ref, r.config_snapshot, COALESCE(host(p.ip), ''), pr.org_id, pr.id, p.replica_index,
		        s.name, en.slug, COALESCE(s.config->'depends_on', 'null'::jsonb)
		   FROM placements p
		   JOIN services s ON s.id = p.service_id AND s.deleted_at IS NULL
		   JOIN environments en ON en.id = s.environment_id
		   JOIN projects pr ON pr.id = en.project_id
		   JOIN releases r ON r.id = p.release_id
		  WHERE p.node_id = $1
		  ORDER BY p.created_at, p.id`, nodeID)
	if err != nil {
		return nil, err
	}
	deps := map[*pb.WorkloadSpec][]string{}      // depends_on of the service of each workload
	byService := map[string][]*pb.WorkloadSpec{} // environment id + service name -> its workloads on this node
	for rows.Next() {
		var pid, sid, eid, rid, color, role, health, svcState, imageRef, ip, orgID, projectID, svcName, envSlug string
		var replica int
		var raw, rawDeps []byte
		if err := rows.Scan(&pid, &sid, &eid, &rid, &color, &role, &health, &svcState, &imageRef, &raw, &ip, &orgID, &projectID, &replica, &svcName, &envSlug, &rawDeps); err != nil {
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
			NetworkAliases:   []string{name, dbengine.Alias(sid), ServiceName(svcName, envSlug)},
			Ip:               ip,
			Resources:        cfg.resourceSpec(),
			Labels: map[string]string{
				"polipan.service": sid, "polipan.placement": pid, "polipan.release": rid, "polipan.managed": "true",
				// The ids that tag metrics and logs (12-observability section 7).
				"polipan.org": orgID, "polipan.project": projectID, "polipan.environment": eid, "polipan.replica": strconv.Itoa(replica),
			},
		}
		if w.RestartPolicy == "" {
			w.RestartPolicy = "always"
		}
		if cfg.Port != 0 {
			w.Ports = []*pb.PortSpec{{ContainerPort: cfg.Port, Protocol: "tcp", HostPort: cfg.HostPort}}
		}
		ds.Workloads = append(ds.Workloads, w)
		byService[eid+"/"+svcName] = append(byService[eid+"/"+svcName], w)
		var names []string
		_ = json.Unmarshal(rawDeps, &names) // not a list of names: no dependency
		deps[w] = names
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	startAfter(ds.Workloads, deps, byService)
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
	ds.Telemetry = c.telemetrySpec()

	// The panel itself goes through Caddy of the node.
	if u, err := url.Parse(c.Config.ExternalURL); err == nil && u.Hostname() != "" {
		ds.Ingress.Routes = append(ds.Ingress.Routes, &pb.Route{
			RouteId: "panel", Hosts: []string{strings.ToLower(u.Hostname())}, PathPrefix: "/",
			Https:     u.Scheme == "https",
			Upstreams: []*pb.Upstream{{Address: c.PanelUpstream, Weight: 100}},
		})
	}

	if err := c.addIngressRoutes(ctx, q, nodeID, nodeState, nodeLabels, ds); err != nil {
		return nil, err
	}
	if err := c.addRemoteEndpoints(ctx, q, nodeID, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

// startAfter resolves config.depends_on to the placements on this node that the agent waits for before it starts a
// workload (08 section 7b): the live and candidate placements of the named services in the same environment. A
// dependency on another node is not a hard one; the config check rejects services pinned to different nodes.
func startAfter(ws []*pb.WorkloadSpec, deps map[*pb.WorkloadSpec][]string, byService map[string][]*pb.WorkloadSpec) {
	for _, w := range ws {
		if w.Role == "stopped" {
			continue
		}
		for _, name := range deps[w] {
			for _, d := range byService[w.EnvironmentId+"/"+name] {
				if d.ServiceId != w.ServiceId && (d.Role == "live" || d.Role == "candidate") {
					w.StartAfterPlacementIds = append(w.StartAfterPlacementIds, d.PlacementId)
				}
			}
		}
	}
}

// remoteLive is a live replica of a service, wherever it runs.
type remoteLive struct {
	liveReplica
	node, ip string
	canary   int // percent of traffic of the candidate replicas of a running canary (0: not a canary)
}

// servesIngress tells if a node in this state gets the routes of the domains (09 section 6: maintenance serves none).
func servesIngress(state string) bool {
	return state != "maintenance" && state != "pending"
}

// addIngressRoutes adds the routes of the domains this node serves. The upstreams are the live replicas of the
// service on any node: a replica on this node by its alias, any other by its mesh address (09 section 8).
// A replica on a node that is down is dropped (09 section 6).
func (c *Control) addIngressRoutes(ctx context.Context, q dbq, nodeID, nodeState string, nodeLabels []byte, ds *pb.DesiredState) error {
	if !servesIngress(nodeState) {
		return nil
	}
	var labels map[string]string
	_ = json.Unmarshal(nodeLabels, &labels)
	isIngress := labels["ingress"] == "true"
	type dom struct {
		id, host, prefix, redirect, sid string
		port                            int
		https                           bool
		cfg                             serviceConfig
	}
	drows, err := q.Query(ctx,
		`SELECT d.id, d.host, d.path_prefix, d.container_port, d.https, COALESCE(d.redirect_www,''), d.service_id, s.config
		   FROM domains d JOIN services s ON s.id = d.service_id AND s.deleted_at IS NULL
		  WHERE (d.ingress_node_ids IS NULL AND $2) OR $1 = ANY(d.ingress_node_ids)
		  ORDER BY d.created_at, d.id`, nodeID, isIngress)
	if err != nil {
		return err
	}
	var doms []dom
	svcIDs := map[string]bool{}
	for drows.Next() {
		var d dom
		var svcRaw []byte
		if err := drows.Scan(&d.id, &d.host, &d.prefix, &d.port, &d.https, &d.redirect, &d.sid, &svcRaw); err != nil {
			drows.Close()
			return err
		}
		_ = json.Unmarshal(svcRaw, &d.cfg)
		if orchestrator.IsPanelHost(c.Config.ExternalURL, d.host) {
			continue // old rows: a service never gets the host of the panel
		}
		doms = append(doms, d)
		svcIDs[d.sid] = true
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return err
	}
	if len(doms) == 0 {
		return nil
	}
	ids := make([]string, 0, len(svcIDs))
	for id := range svcIDs {
		ids = append(ids, id)
	}
	lrows, err := q.Query(ctx,
		`SELECT p.service_id, p.id, p.release_id, p.color, p.health, p.node_id, COALESCE(host(p.ip), ''), p.role, COALESCE(d.canary_step, 0)
		   FROM placements p JOIN nodes n ON n.id = p.node_id JOIN services s ON s.id = p.service_id
		   LEFT JOIN deployments d ON d.id = p.deployment_id AND d.state = 'shifting' AND d.canary_routing
		  WHERE p.service_id = ANY($1) AND (p.role = 'live' OR (p.role = 'candidate' AND d.id IS NOT NULL))
		    AND s.desired_state = 'running' AND n.state NOT IN ('down','pending','maintenance')
		  ORDER BY p.replica_index, p.id`, ids)
	if err != nil {
		return err
	}
	liveBySvc := map[string][]remoteLive{}
	for lrows.Next() {
		var sid string
		var l remoteLive
		if err := lrows.Scan(&sid, &l.placement, &l.release, &l.color, &l.health, &l.node, &l.ip, &l.role, &l.canary); err != nil {
			lrows.Close()
			return err
		}
		liveBySvc[sid] = append(liveBySvc[sid], l)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return err
	}
	for _, d := range doms {
		route := &pb.Route{
			RouteId: d.id, Hosts: []string{d.host}, PathPrefix: d.prefix, Https: d.https, RedirectWww: d.redirect, ServiceId: d.sid,
			Upstreams:               []*pb.Upstream{}, // empty: the agent answers 503 (AC-DOM-04)
			StreamCloseDelaySeconds: uint32(d.cfg.drainSeconds()),
		}
		all := liveBySvc[d.sid]
		var blue, green []remoteLive // green: the candidate replicas of a canary that gets traffic now
		for _, l := range all {
			if l.role == "candidate" {
				green = append(green, l)
			} else {
				blue = append(blue, l)
			}
		}
		flat := make([]liveReplica, len(blue))
		for i, l := range blue {
			flat[i] = l.liveReplica
		}
		keep := map[string]bool{}
		for _, l := range routable(flat) {
			keep[l.placement] = true
		}
		for _, l := range green { // a green replica that is not healthy gets no traffic, and it is not a fallback
			if l.health != "unhealthy" {
				keep[l.placement] = true
			}
		}
		weightBlue, weightGreen := canaryWeights(blue, green, keep)
		for _, l := range all {
			if !keep[l.placement] {
				continue
			}
			addr := fmt.Sprintf("%s:%d", ContainerName(l.placement), d.port)
			if l.node != nodeID {
				if l.ip == "" {
					continue // a replica on another node without a mesh address cannot be reached
				}
				addr = fmt.Sprintf("%s:%d", l.ip, d.port)
			}
			w := uint32(100)
			if l.role == "candidate" {
				w = weightGreen
			} else if weightBlue > 0 {
				w = weightBlue
			}
			route.Upstreams = append(route.Upstreams, &pb.Upstream{Address: addr, Weight: w, ReleaseId: l.release, Color: l.color})
		}
		ds.Ingress.Routes = append(ds.Ingress.Routes, route)
	}
	return nil
}

// addRemoteEndpoints adds the workloads of other nodes that are in the same environments as the workloads of this
// node: the names for the internal DNS and the addresses for the environment firewall (REQ-NODE-009, REQ-SEC-106).
// Only names, addresses and ids go out: no env, no secret (REQ-SEC-100).
func (c *Control) addRemoteEndpoints(ctx context.Context, q dbq, nodeID string, ds *pb.DesiredState) error {
	rows, err := q.Query(ctx,
		`SELECT p.id, p.service_id, s.environment_id, host(p.ip), s.name, en.slug
		   FROM placements p JOIN services s ON s.id = p.service_id AND s.deleted_at IS NULL JOIN nodes n ON n.id = p.node_id
		   JOIN environments en ON en.id = s.environment_id
		  WHERE p.node_id <> $1 AND p.ip IS NOT NULL AND p.role = 'live' AND n.state NOT IN ('down','pending','maintenance')
		    AND s.environment_id IN (SELECT s2.environment_id FROM placements p2 JOIN services s2 ON s2.id = p2.service_id WHERE p2.node_id = $1)
		  ORDER BY p.created_at, p.id`, nodeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, sid, eid, ip, svcName, envSlug string
		if err := rows.Scan(&pid, &sid, &eid, &ip, &svcName, &envSlug); err != nil {
			return err
		}
		ds.RemoteEndpoints = append(ds.RemoteEndpoints, &pb.Endpoint{
			Names: []string{ContainerName(pid), dbengine.Alias(sid), ServiceName(svcName, envSlug)}, Ip: ip, EnvironmentId: eid, ServiceId: sid,
		})
	}
	return rows.Err()
}

// telemetrySpec tells the agents where to send metrics and logs. It is nil when monitoring is not set up.
func (c *Control) telemetrySpec() *pb.TelemetrySpec {
	if c.Config.VictoriaMetricsURL == "" && c.Config.VictoriaLogsURL == "" {
		return nil
	}
	base := strings.TrimRight(c.Config.IngestURL, "/")
	if base == "" {
		host := "localhost"
		if len(c.Config.AgentHosts) > 0 {
			host = strings.TrimSpace(c.Config.AgentHosts[0])
		}
		port := "9444"
		if _, p, err := net.SplitHostPort(c.Config.IngestListen); err == nil && p != "" {
			port = p
		}
		base = "https://" + net.JoinHostPort(host, port)
	}
	return &pb.TelemetrySpec{
		MetricsWriteUrl: base + "/ingest/metrics", LogsUrl: base + "/ingest/logs",
		VmagentImage: c.Config.VMAgentImage, ScrapeIntervalSeconds: 15, BufferMib: 100,
	}
}

// canaryWeights gives the weight of one blue and of one green upstream, so that the green ones get the canary percent
// of the traffic together. The weights are whole numbers with no common divisor. Without a canary, or when no green
// replica is left in the route, blue is 0 and green is 100 (the caller then uses 100 for every upstream).
func canaryWeights(blue, green []remoteLive, keep map[string]bool) (b, g uint32) {
	nb, ng := 0, 0
	pct := 0
	for _, l := range blue {
		if keep[l.placement] {
			nb++
		}
	}
	for _, l := range green {
		if keep[l.placement] {
			ng++
			pct = l.canary
		}
	}
	if nb == 0 || ng == 0 || pct <= 0 || pct >= 100 {
		return 0, 100
	}
	gw, bw := pct*nb, (100-pct)*ng
	d := gcd(gw, bw)
	return uint32(bw / d), uint32(gw / d)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
