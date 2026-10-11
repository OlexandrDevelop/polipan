// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
)

var errNodeDown = errors.New("the node is not connected")

// plc is a placement of a run.
type plc struct{ id, node string }

// run is one deployment that runs now (08 sections 4 to 7).
type run struct {
	reserved []string // nodes reserved by the scheduler until the placements are saved
	c        *Control
	ad       *activeDep
	d        *deployment
	svc      *svcInfo
	cfg      serviceConfig // config of the new release

	node        string   // the node of the first replica (pre_deploy runs there)
	nodes       []string // the node of every new replica, in order
	keep        []plc    // live replicas that this deployment does not touch (move of one node, failover)
	dead        []plc    // replicas on a node that failed: removed at the switch (failover)
	exclude     []string // nodes the scheduler must not use
	dataCopy    bool     // move or failover of a service with a local volume: the data goes to the new node
	releaseID   string
	imageRef    string
	green, blue []plc
	fast        bool // the green containers are the kept stopped ones of an older release
	hadBlue     bool
	blueStopped bool // recreate: the old containers were stopped, so a failure must start them again
	committed   bool // the switch is in the database
	reverted    bool // the switch was undone
}

func ids(l []plc) []string {
	out := make([]string, len(l))
	for i, p := range l {
		out[i] = p.id
	}
	return out
}

func nodesOf(l ...[]plc) []string {
	var out []string
	for _, g := range l {
		for _, p := range g {
			out = append(out, p.node)
		}
	}
	return out
}

// runDeployment runs a claimed deployment to a terminal state. On a server stop it leaves the state as it is:
// the next start finishes it (recover.go).
func (c *Control) runDeployment(ctx context.Context, ad *activeDep, d *deployment, resume bool) {
	r := &run{c: c, ad: ad, d: d}
	defer r.unreserve()
	var derr *deployErr
	if resume {
		derr = r.resume(ctx)
	} else {
		derr = r.execute(ctx)
	}
	if derr == nil {
		return
	}
	cause := context.Cause(ctx)
	if ctx.Err() != nil && !errors.Is(cause, errUserCancel) {
		return // the server stops
	}
	if errors.Is(cause, errUserCancel) {
		derr = &deployErr{reason: ReasonCancelled, detail: "cancelled by the user", state: "cancelled"}
	}
	r.cleanup(derr)
	c.endDeployment(ctx, d.id, derr)
}

func (r *run) step(ctx context.Context, state, msg string, data map[string]any) *deployErr {
	if err := r.c.setState(ctx, r.d.id, state, msg, data); err != nil {
		if ctx.Err() != nil {
			return fail(ReasonInternal, "stopped: %v", ctx.Err())
		}
		return fail(ReasonInternal, "database: %v", err)
	}
	return nil
}

func (r *run) execute(ctx context.Context) *deployErr {
	if e := r.load(ctx); e != nil {
		return e
	}
	if e := r.prepare(ctx); e != nil {
		return e
	}
	if e := r.startContainers(ctx); e != nil {
		return e
	}
	if e := r.waitHealthy(ctx); e != nil {
		return e
	}
	switch r.d.strategy {
	case "recreate":
		return r.switchRecreate(ctx)
	case "canary":
		return r.canary(ctx, 0, false)
	}
	return r.switchBlueGreen(ctx)
}

// load reads the service and the placements that serve now (blue).
func (r *run) load(ctx context.Context) *deployErr {
	c := r.c
	svc, err := c.loadService(ctx, c.DB, r.d.serviceID)
	if err != nil {
		return fail(ReasonInvalidConfig, "service not found: %v", err)
	}
	r.svc, r.cfg = svc, svc.cfg
	r.d.prevReleaseID = svc.liveRelease
	if _, err := c.DB.Exec(ctx, `UPDATE deployments SET previous_release_id = $2, updated_at = now() WHERE id = $1`, r.d.id, svc.liveRelease); err != nil {
		return fail(ReasonInternal, "database: %v", err)
	}
	live, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1 AND role IN ('live','draining')
		AND deployment_id IS DISTINCT FROM $2 ORDER BY node_id, replica_index, id`, r.d.serviceID, r.d.id)
	if err != nil {
		return fail(ReasonInternal, "read placements: %v", err)
	}
	switch r.d.trigger {
	case "move":
		// Only the replicas on the node that is left move. The others stay live.
		for _, p := range live {
			if r.d.opts.MoveFrom == "" || p.node == r.d.opts.MoveFrom {
				r.blue = append(r.blue, p)
			} else {
				r.keep = append(r.keep, p)
			}
		}
		r.exclude = []string{}
		if r.d.opts.MoveFrom != "" {
			r.exclude = []string{r.d.opts.MoveFrom}
		}
	case "failover":
		// A replica on a failed node is replaced. There is nothing to drain on that node (09 section 9).
		failed := map[string]bool{}
		for _, n := range r.d.opts.FailoverFrom {
			failed[n] = true
		}
		r.exclude = r.d.opts.FailoverFrom
		for _, p := range live {
			if failed[p.node] {
				r.dead = append(r.dead, p)
			} else {
				r.keep = append(r.keep, p)
			}
		}
	default:
		r.blue = live
	}
	r.hadBlue = len(r.blue) > 0
	if r.d.trigger == "move" || r.d.trigger == "failover" {
		if r.dataCopy, err = c.hasLocalVolume(ctx, c.DB, r.d.serviceID); err != nil {
			return fail(ReasonInternal, "volumes: %v", err)
		}
	}
	if r.green, err = c.placementList(ctx, `SELECT id, node_id FROM placements WHERE deployment_id = $1 ORDER BY id`, r.d.id); err != nil {
		return fail(ReasonInternal, "read placements: %v", err)
	}
	return nil
}

func (c *Control) placementList(ctx context.Context, sql string, args ...any) ([]plc, error) {
	rows, err := c.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []plc
	for rows.Next() {
		var p plc
		if err := rows.Scan(&p.id, &p.node); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// prepare builds (git), schedules the replicas, checks the capacity, makes the Release and pulls the image.
func (r *run) prepare(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if usesStoredRelease(d.trigger, d.releaseID != nil) {
		return r.prepareRollback(ctx)
	}
	if d.trigger == "move" || d.trigger == "failover" {
		return r.prepareSameRelease(ctx)
	}
	built, berr := c.buildIfGit(ctx, d.id, d.serviceID)
	if berr != nil {
		return berr
	}
	if r.svc.sourceType == "git" {
		if e := r.step(ctx, "image_ready", "image built", nil); e != nil {
			return e
		}
	}
	if r.cfg.Image == "" && built == "" {
		return fail(ReasonInvalidConfig, "the service config needs an image")
	}
	if built != "" {
		r.cfg.Image = built
	} else if c.Build != nil && c.Build.Registry.IsOwnImage(r.cfg.Image) {
		// The node pull credential of the built-in registry is not limited to one repository.
		// Only a build of this service may use it: an image service must not pull built images of others.
		return fail(ReasonInvalidConfig, "an image service cannot use an image of the built-in registry")
	}
	if e := r.placeReplicas(ctx); e != nil {
		return e
	}
	// Variables, secret versions and volumes of the release (M2). A missing reference fails here, before the pull.
	snap, derr := c.resolveRelease(ctx, d.serviceID, r.node)
	if derr != nil {
		return derr
	}
	imageRef, digest, e := r.pullAll(ctx, r.cfg.Image)
	if e != nil {
		return e
	}
	r.releaseID, r.imageRef = gateway.NewID("rel"), imageRef
	if err := c.createRelease(ctx, d.id, d.serviceID, r.releaseID, d.actorID, imageRef, digest, snap, d.prevReleaseID); err != nil {
		return fail(ReasonInvalidConfig, "create release: %v", err)
	}
	d.releaseID = &r.releaseID
	return nil
}

// placeReplicas schedules the new replicas, checks that every node is connected and has room.
func (r *run) placeReplicas(ctx context.Context) *deployErr {
	c := r.c
	nodes, e := r.schedule(ctx)
	if e != nil {
		return e
	}
	r.nodes, r.node = nodes, nodes[0]
	for _, n := range distinct(nodes) {
		if c.Sender == nil || !c.Sender.Connected(n) {
			return fail(ReasonNodeUnavailable, "node %s is not connected", n)
		}
	}
	return r.checkCapacity(ctx)
}

func distinct(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// prepareSameRelease is the start of a move or a failover: the live Release starts again on the new nodes. No build
// runs and no Release is made (09 section 7).
func (r *run) prepareSameRelease(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if r.svc.liveRelease == nil {
		return fail(ReasonInvalidConfig, "the service has no live release")
	}
	if d.trigger == "failover" {
		if e := r.ensureNodeStillDown(ctx); e != nil {
			return e
		}
	}
	var raw []byte
	if err := c.DB.QueryRow(ctx, `SELECT image_ref, config_snapshot FROM releases WHERE id = $1`, *r.svc.liveRelease).Scan(&r.imageRef, &raw); err != nil {
		return fail(ReasonInvalidConfig, "release not found: %v", err)
	}
	if err := json.Unmarshal(raw, &r.cfg); err != nil {
		return fail(ReasonInvalidConfig, "release config: %v", err)
	}
	r.releaseID = *r.svc.liveRelease
	d.releaseID = &r.releaseID
	if _, err := c.DB.Exec(ctx, `UPDATE deployments SET release_id = $2, updated_at = now() WHERE id = $1`, d.id, r.releaseID); err != nil {
		return fail(ReasonInternal, "database: %v", err)
	}
	if e := r.placeReplicas(ctx); e != nil {
		return e
	}
	_, _, e := r.pullAll(ctx, r.imageRef)
	return e
}

// pullAll pulls the image on every node that gets a replica. The first answer gives the reference by digest.
func (r *run) pullAll(ctx context.Context, image string) (imageRef, digest string, e *deployErr) {
	for i, n := range distinct(r.nodes) {
		ref, dig, err := r.pull(ctx, n, image)
		if err != nil {
			return "", "", err
		}
		if i == 0 {
			imageRef, digest = ref, dig
		}
	}
	return imageRef, digest, nil
}

// pull pulls an image on the node and returns the reference by digest.
func (r *run) pull(ctx context.Context, node, image string) (imageRef, digest string, e *deployErr) {
	c := r.c
	ack, err := c.sendCmd(ctx, node, r.d.id, "pull-"+node+"-"+image, &pb.Command{Body: &pb.Command_PullImage{
		PullImage: &pb.PullImage{ImageRef: image, RegistryAuthRef: c.registryAuthRef(image)}}}, 10*time.Minute)
	if err != nil {
		return "", "", fail(ReasonNodeUnavailable, "pull command: %v", err)
	}
	if !ack.GetOk() {
		reason := ReasonImagePullFailed
		if ack.GetErrorCode() == "docker_unavailable" {
			reason = ReasonNodeUnavailable
		}
		return "", "", fail(reason, "%s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
	}
	var res struct {
		ImageRef    string `json:"image_ref"`
		Digest      string `json:"digest"`
		ImageDigest string `json:"image_digest"`
	}
	_ = json.Unmarshal(ack.GetResultJson(), &res)
	imageRef, digest = res.ImageRef, res.Digest
	if imageRef == "" {
		imageRef = res.ImageDigest // the agent may put the digest ref here
	}
	if i := strings.LastIndex(imageRef, "@"); i >= 0 && digest == "" {
		digest = imageRef[i+1:]
	}
	if digest == "" {
		digest = res.ImageDigest
		if i := strings.LastIndex(digest, "@"); i >= 0 {
			digest = digest[i+1:]
		}
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return "", "", fail(ReasonImagePullFailed, "the agent gave no image digest")
	}
	if !strings.Contains(imageRef, "@") {
		imageRef = imageRepo(image) + "@" + digest
	}
	return imageRef, digest, nil
}

// checkCapacity checks that every node has room for blue and green at the same time (REQ-DEPLOY-010).
func (r *run) checkCapacity(ctx context.Context) *deployErr {
	c := r.c
	if r.d.strategy == "recreate" {
		return nil
	}
	want := r.cfg.resourceSpec()
	if want == nil {
		return nil
	}
	per := map[string]int{}
	for _, n := range r.nodes {
		per[n]++
	}
	for _, node := range distinct(r.nodes) {
		var capRaw []byte
		if err := c.DB.QueryRow(ctx, `SELECT capacity FROM nodes WHERE id = $1`, node).Scan(&capRaw); err != nil || len(capRaw) == 0 {
			continue
		}
		var capa struct {
			CPU uint64 `json:"cpu_millis"`
			Mem uint64 `json:"memory_bytes"`
		}
		if json.Unmarshal(capRaw, &capa) != nil {
			continue
		}
		var usedCPU, usedMem float64
		if err := c.DB.QueryRow(ctx, `SELECT COALESCE(SUM((r.config_snapshot->'resources'->>'cpu_millis')::numeric),0),
			COALESCE(SUM((r.config_snapshot->'resources'->>'memory_bytes')::numeric),0)
			FROM placements p JOIN releases r ON r.id = p.release_id WHERE p.node_id = $1 AND p.role IN ('live','candidate','draining')`, node).Scan(&usedCPU, &usedMem); err != nil {
			continue
		}
		n := float64(per[node])
		tooMuch := (capa.Mem > 0 && usedMem+n*float64(want.MemoryLimitBytes) > float64(capa.Mem)) ||
			(capa.CPU > 0 && usedCPU+n*float64(want.CpuLimitMillis) > float64(capa.CPU))
		if !tooMuch {
			continue
		}
		if !r.d.opts.AllowDowntimeOnLowCapacity {
			return fail(ReasonNodeUnavailable, "node %s has no room for the old and the new container at the same time", node)
		}
		r.d.strategy = "recreate"
		warn, _ := json.Marshal([]map[string]string{{"code": WarnZeroDowntime,
			"message": "The node has room for one copy only, so the old container stops before the new one starts."}})
		_, _ = c.DB.Exec(ctx, `UPDATE deployments SET strategy = 'recreate', warnings = warnings || $2::jsonb WHERE id = $1`, r.d.id, warn)
		return nil
	}
	return nil
}

// startContainers is the starting state: pre_deploy, then the green containers.
func (r *run) startContainers(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if e := r.step(ctx, "starting", "starting the new containers", map[string]any{"release_id": r.releaseID, "strategy": d.strategy}); e != nil {
		return e
	}
	if len(r.cfg.PreDeploy) > 0 && d.trigger != "rollback" && d.trigger != "move" && d.trigger != "failover" { // the same release never runs the migration again
		if e := r.oneOff(ctx, "pre_deploy", r.cfg.PreDeploy, ReasonPreDeployFailed); e != nil {
			return e
		}
	}
	color := "blue"
	if len(r.blue) > 0 {
		color = "green"
		var cur string
		if c.DB.QueryRow(ctx, `SELECT color FROM placements WHERE id = $1`, r.blue[0].id).Scan(&cur) == nil && cur == "green" {
			color = "blue"
		}
	}
	role := "candidate"
	if d.strategy == "recreate" {
		role = "live"
		// Blue stops first (REQ-DEPLOY-007 in 08 section 7).
		for _, b := range r.blue {
			grace := time.Duration(r.cfg.stopGraceSeconds()+30) * time.Second
			ack, err := c.sendCmd(ctx, b.node, d.id, "stop-blue-"+b.id, &pb.Command{Body: &pb.Command_StopWorkload{
				StopWorkload: &pb.StopWorkload{PlacementId: b.id, GraceSeconds: uint32(r.cfg.stopGraceSeconds())}}}, grace)
			if err != nil || !ack.GetOk() {
				c.Log.Warn("recreate: stop of the old container", "placement", b.id, "err", err, "ack", ack.GetErrorMessage())
			}
		}
		r.blueStopped = len(r.blue) > 0
		if r.blueStopped {
			if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
				if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'stopped', updated_at = now() WHERE id = ANY($1)`, ids(r.blue)); err != nil {
					return nil, err
				}
				return nodesOf(r.blue), nil
			}); err != nil {
				return fail(ReasonContainerStartFailed, "stop the old containers: %v", err)
			}
		}
	}
	if r.dataCopy {
		if e := r.copyData(ctx); e != nil {
			return e
		}
	}
	// A kept stopped container of the release is started again (fast rollback), else a new placement is made.
	err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if r.fast {
			if _, err := tx.Exec(ctx, `UPDATE placements SET role = $2, color = $3, deployment_id = $4, health = 'unknown', health_info = NULL,
				expires_at = NULL, updated_at = now() WHERE id = ANY($1)`, ids(r.green), role, color, d.id); err != nil {
				return nil, err
			}
		} else {
			r.green = nil
			for i, node := range r.nodes {
				id := gateway.NewID("plc")
				ip, err := allocIP(ctx, tx, node)
				if err != nil {
					return nil, err
				}
				r.green = append(r.green, plc{id, node})
				if _, err := tx.Exec(ctx,
					`INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role, deployment_id, ip)
					 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::inet)`, id, d.serviceID, node, r.releaseID, i, color, role, d.id, ip); err != nil {
					return nil, err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE services SET desired_state = 'running', updated_at = now() WHERE id = $1`, d.serviceID); err != nil {
			return nil, err
		}
		return nodesOf(r.green, r.blue), nil
	})
	if err != nil {
		return fail(ReasonContainerStartFailed, "save placement: %v", err)
	}
	r.unreserve() // the rows are in the database now
	return nil
}

// oneOff runs the pre_deploy or post_deploy command in a one-off container from the new image.
func (r *run) oneOff(ctx context.Context, kind string, cmd []string, reason string) *deployErr {
	c, d := r.c, r.d
	w := &pb.WorkloadSpec{
		PlacementId: "oneoff-" + d.id + "-" + kind, ServiceId: d.serviceID, EnvironmentId: d.envID, ReleaseId: r.releaseID,
		ContainerName: "pp-" + kind + "-" + d.id, ImageRef: r.imageRef, Command: cmd, RestartPolicy: "no",
		RegistryAuthRef: c.registryAuthRef(r.imageRef), NetworkAliases: nil,
		Labels: map[string]string{"polipan.oneoff": kind, "polipan.service": d.serviceID},
	}
	if err := c.fillWorkload(ctx, c.DB, r.node, w); err != nil {
		return fail(reason, "%s: environment: %v", kind, err)
	}
	timeout := time.Duration(r.cfg.deployTimeout()) * time.Second
	ack, err := c.sendCmd(ctx, r.node, d.id, kind, &pb.Command{Body: &pb.Command_RunOneOff{
		RunOneOff: &pb.RunOneOff{Workload: w, TimeoutSeconds: uint32(timeout / time.Second)}}}, timeout+30*time.Second)
	if err != nil {
		return fail(reason, "%s: %v", kind, err)
	}
	var res struct {
		ExitCode int    `json:"exit_code"`
		Logs     string `json:"logs"`
	}
	_ = json.Unmarshal(ack.GetResultJson(), &res)
	for _, line := range strings.Split(strings.TrimRight(res.Logs, "\n"), "\n") {
		if line == "" {
			continue
		}
		line = c.maskStep(ctx, d.id, line)
		if err := addLog(ctx, c.DB, d.id, "pre_deploy", line); err == nil {
			c.event(ctx, d.id, map[string]any{"type": "log", "data": map[string]any{"stream": kind, "line": line}})
		}
	}
	if !ack.GetOk() {
		return fail(reason, "%s: %s: %s", kind, ack.GetErrorCode(), ack.GetErrorMessage())
	}
	if res.ExitCode != 0 {
		return fail(reason, "%s exited with code %d", kind, res.ExitCode)
	}
	return nil
}

// waitHealthy is the health_checking state: all green containers must be healthy together (REQ-HC-005).
func (r *run) waitHealthy(ctx context.Context) *deployErr {
	c, d := r.c, r.d
	if e := r.step(ctx, "health_checking", "waiting for the health check", nil); e != nil {
		return e
	}
	timeout := r.cfg.deployTimeout()
	hctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go r.watchNodes(hctx, cancel)
	go r.watchHealth(hctx)

	type result struct {
		p   plc
		ack *pb.CommandAck
		err error
	}
	ch := make(chan result, len(r.green))
	for _, g := range r.green {
		go func(g plc) {
			ack, err := c.sendCmd(hctx, g.node, d.id, "health-"+g.id, &pb.Command{Body: &pb.Command_CheckHealth{
				CheckHealth: &pb.CheckHealth{PlacementId: g.id, TimeoutSeconds: timeout}}}, time.Duration(timeout)*time.Second+30*time.Second)
			ch <- result{g, ack, err}
		}(g)
	}
	var first *deployErr
	for range r.green {
		res := <-ch
		var e *deployErr
		switch {
		case res.err != nil && errors.Is(context.Cause(hctx), errNodeDown):
			e = fail(ReasonNodeUnavailable, "node %s was not connected for %s", res.p.node, c.Config.NodeDownAfter)
		case res.err != nil && ctx.Err() == nil && errors.Is(res.err, context.DeadlineExceeded):
			e = fail(ReasonHealthTimeout, "the health check did not pass in %d s", timeout)
		case res.err != nil:
			e = fail(ReasonNodeUnavailable, "health command: %v", res.err)
		case !res.ack.GetOk() && res.ack.GetErrorCode() == "container_start_failed":
			e = fail(ReasonContainerStartFailed, "%s", res.ack.GetErrorMessage())
		case !res.ack.GetOk() && (res.ack.GetErrorCode() == "health_timeout" || res.ack.GetErrorCode() == "deadline_exceeded"):
			e = fail(ReasonHealthTimeout, "%s", res.ack.GetErrorMessage())
		case !res.ack.GetOk():
			e = fail(ReasonHealthFailed, "%s: %s", res.ack.GetErrorCode(), res.ack.GetErrorMessage())
		}
		if e != nil && first == nil {
			first = e
			cancel(errNodeDown) // stops the other waits; the first error is the one that counts
		}
	}
	if first != nil {
		c.event(ctx, d.id, map[string]any{"type": "health", "data": map[string]any{"healthy": false, "error": first.detail}})
		return first
	}
	if ctx.Err() != nil {
		return fail(ReasonInternal, "stopped: %v", ctx.Err())
	}
	_, _ = c.DB.Exec(ctx, `UPDATE placements SET health = 'healthy', updated_at = now() WHERE id = ANY($1)`, ids(r.green))
	c.event(ctx, d.id, map[string]any{"type": "health", "data": map[string]any{"healthy": true}})
	return nil
}

// watchNodes cancels the wait when a node of the green containers is not connected for node_down_after (08 section 11).
func (r *run) watchNodes(ctx context.Context, cancel context.CancelCauseFunc) {
	c := r.c
	down := map[string]time.Time{}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for _, g := range r.green {
				if c.Sender != nil && c.Sender.Connected(g.node) {
					delete(down, g.node)
					continue
				}
				if _, ok := down[g.node]; !ok {
					down[g.node] = now
				}
				if now.Sub(down[g.node]) >= c.Config.NodeDownAfter {
					cancel(errNodeDown)
					return
				}
			}
		}
	}
}

// watchHealth writes a health event of the deployment when the result of a green container changes.
func (r *run) watchHealth(ctx context.Context) {
	c := r.c
	last := map[string]string{}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rows, err := c.DB.Query(ctx, `SELECT id, health, COALESCE(health_info::text,'') FROM placements WHERE id = ANY($1)`, ids(r.green))
			if err != nil {
				continue
			}
			for rows.Next() {
				var id, h, info string
				if rows.Scan(&id, &h, &info) != nil {
					continue
				}
				key := h + info
				if last[id] == key {
					continue
				}
				last[id] = key
				var detail map[string]any
				_ = json.Unmarshal([]byte(info), &detail)
				c.event(ctx, r.d.id, map[string]any{"type": "health", "data": map[string]any{"placement_id": id, "health": h, "detail": detail}})
			}
			rows.Close()
		}
	}
}

// ---- cleanup ----

// cleanup removes or stops the green containers and starts blue again when a recreate stopped it.
// It uses its own context: ctx may be cancelled (a user cancel).
func (r *run) cleanup(derr *deployErr) {
	if r.reverted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r.discardGreen(ctx, derr.reason == ReasonCancelled)
	if r.blueStopped {
		r.restoreBlue(ctx)
		if derr.state == "" {
			derr.state = "rolled_back" // endDeployment uses failed when the state machine does not allow it
		}
	}
}

// discardGreen removes the new containers (after a cancel) or stops them and keeps them for failed_green_keep.
// The kept stopped containers of an older release (fast rollback) stay as they were.
func (r *run) discardGreen(ctx context.Context, removeNow bool) {
	c := r.c
	if len(r.green) == 0 {
		if g, err := c.placementList(ctx, `SELECT id, node_id FROM placements WHERE deployment_id = $1`, r.d.id); err == nil {
			r.green = g
		}
	}
	for _, g := range r.green {
		if r.fast {
			r.stopOne(ctx, g)
		} else if removeNow {
			c.removeContainer(ctx, g.node, g.id)
		} else {
			r.stopOne(ctx, g)
		}
	}
	err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		switch {
		case len(r.green) == 0:
		case r.fast:
			if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'stopped', updated_at = now() WHERE id = ANY($1)`, ids(r.green)); err != nil {
				return nil, err
			}
		case removeNow:
			if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE id = ANY($1)`, ids(r.green)); err != nil {
				return nil, err
			}
		default:
			if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'stopped', health = 'unhealthy', expires_at = $2, updated_at = now() WHERE id = ANY($1)`,
				ids(r.green), c.Clock.Now().Add(c.FailedGreenKeep)); err != nil {
				return nil, err
			}
		}
		return nodesOf(r.green), nil
	})
	if err != nil {
		c.Log.Warn("discard green", "deployment", r.d.id, "err", err)
	}
}

func (r *run) stopOne(ctx context.Context, p plc) {
	c := r.c
	if c.Sender == nil || !c.Sender.Connected(p.node) {
		return
	}
	if _, err := c.sendCmd(ctx, p.node, r.d.id, "stop-"+p.id, &pb.Command{Body: &pb.Command_StopWorkload{
		StopWorkload: &pb.StopWorkload{PlacementId: p.id, GraceSeconds: uint32(r.cfg.stopGraceSeconds())}}}, time.Duration(r.cfg.stopGraceSeconds()+30)*time.Second); err != nil {
		c.Log.Warn("stop container", "placement", p.id, "err", err)
	}
}

// restoreBlue makes the old containers live again after a failed recreate.
func (r *run) restoreBlue(ctx context.Context) {
	c := r.c
	if err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		if _, err := tx.Exec(ctx, `UPDATE placements SET role = 'live', updated_at = now() WHERE id = ANY($1)`, ids(r.blue)); err != nil {
			return nil, err
		}
		return nodesOf(r.blue, r.green), nil
	}); err != nil {
		c.Log.Error("restore the old containers", "deployment", r.d.id, "err", err)
	}
}
