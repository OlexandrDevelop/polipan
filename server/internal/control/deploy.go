// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/orchestrator"
	"github.com/polipan/polipan/server/internal/vars"
)

// Failure reasons of a deployment (08-deploy-engine).
const (
	ReasonImagePullFailed      = "image_pull_failed"
	ReasonContainerStartFailed = "container_start_failed"
	ReasonHealthCheckFailed    = "health_check_failed"
	ReasonNodeUnavailable      = "node_unavailable"
	ReasonInvalidConfig        = "invalid_config"
	ReasonServerRestarted      = "server_restarted"
)

type deployErr struct{ reason, detail string }

func (e *deployErr) Error() string { return e.reason + ": " + e.detail }

// Deploy makes a deployment (state queued) and runs it in the background.
func (c *Control) Deploy(ctx context.Context, serviceID, actorID, trigger string) (string, error) {
	if trigger == "" {
		trigger = "manual"
	}
	var envID string
	var deleted *time.Time
	err := c.DB.QueryRow(ctx, `SELECT environment_id, deleted_at FROM services WHERE id = $1`, serviceID).Scan(&envID, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted != nil) {
		return "", orchestrator.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := c.pickNodeForService(ctx, serviceID); err != nil {
		return "", orchestrator.ErrNoNode
	}

	id := gateway.NewID("dep")
	c.track(id, true)
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		c.track(id, false)
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One deployment at a time for a service. 'queued' is not in the unique index, so we check it here.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, serviceID); err != nil {
		c.track(id, false)
		return "", err
	}
	var busy bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM deployments WHERE service_id = $1
		   AND state IN ('queued','building','image_ready','starting','health_checking','shifting','switching','draining'))`,
		serviceID).Scan(&busy); err != nil {
		c.track(id, false)
		return "", err
	}
	if busy {
		c.track(id, false)
		return "", orchestrator.ErrDeployRunning
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, state, actor_id)
		 VALUES ($1,$2,$3,$4,'recreate','queued',$5)`, id, serviceID, envID, trigger, actor)
	if err == nil {
		err = saveBuildRequest(ctx, tx, id) // commit and no_cache of a git deploy (build.go)
	}
	if err == nil {
		err = c.addStep(ctx, tx, id, "queued", "deployment created", nil)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		c.track(id, false)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return "", orchestrator.ErrDeployRunning
		}
		return "", err
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer c.track(id, false)
		c.runDeployment(c.runCtx, id, serviceID, actorID)
	}()
	return id, nil
}

func (c *Control) track(id string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.active[id] = true
	} else {
		delete(c.active, id)
	}
}

// recoverDeployments marks deployments that a former server run left in an active state as failed.
func (c *Control) recoverDeployments(ctx context.Context) error {
	rows, err := c.DB.Query(ctx,
		`SELECT id FROM deployments WHERE state IN ('queued','building','image_ready','starting','health_checking','shifting','switching','draining')`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		c.mu.Lock()
		mine := c.active[id]
		c.mu.Unlock()
		if mine {
			continue
		}
		c.markFailed(ctx, id, &deployErr{ReasonServerRestarted, "the server restarted while the deployment was running"})
	}
	return nil
}

type readyNode struct{ id string }

// pickNode returns the first ready node (oldest first). Scheduling by capacity comes later (09 section 7).
func (c *Control) pickNode(ctx context.Context) (readyNode, error) {
	var n readyNode
	err := c.DB.QueryRow(ctx, `SELECT id FROM nodes WHERE state = 'ready' ORDER BY created_at, id LIMIT 1`).Scan(&n.id)
	return n, err
}

func (c *Control) runDeployment(ctx context.Context, depID, serviceID, actorID string) {
	derr := c.execute(ctx, depID, serviceID, actorID)
	if derr == nil {
		return
	}
	if ctx.Err() != nil {
		return // shutdown: the next start marks it failed (server_restarted)
	}
	c.markFailed(ctx, depID, derr)
}

func (c *Control) execute(ctx context.Context, depID, serviceID, actorID string) *deployErr {
	fail := func(reason, format string, a ...any) *deployErr { return &deployErr{reason, fmt.Sprintf(format, a...)} }
	step := func(state, msg string, data map[string]any) *deployErr {
		if err := c.setState(ctx, depID, state, msg, data); err != nil {
			return fail(ReasonInvalidConfig, "database: %v", err)
		}
		return nil
	}

	if e := step("building", "pulling the image", nil); e != nil {
		return e
	}
	var raw []byte
	var oldRelease *string
	if err := c.DB.QueryRow(ctx, `SELECT config, live_release_id FROM services WHERE id = $1`, serviceID).Scan(&raw, &oldRelease); err != nil {
		return fail(ReasonInvalidConfig, "service not found: %v", err)
	}
	// A git service is built first (build.go). The result is the image ref by digest in the built-in registry.
	built, berr := c.buildIfGit(ctx, depID, serviceID)
	if berr != nil {
		return berr
	}
	var cfg serviceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil || (cfg.Image == "" && built == "") {
		return fail(ReasonInvalidConfig, "the service config needs an image")
	}
	if built != "" {
		cfg.Image = built
	}
	node, err := c.pickNodeForService(ctx, serviceID)
	if err != nil {
		return fail(ReasonNodeUnavailable, "no ready node (or the node of the volumes is not ready)")
	}
	if c.Sender == nil || !c.Sender.Connected(node.id) {
		return fail(ReasonNodeUnavailable, "node %s is not connected", node.id)
	}
	// Variables, secret versions and volumes of the release (M2). A missing reference fails here, before the pull.
	snap, derr := c.resolveRelease(ctx, serviceID, node.id)
	if derr != nil {
		return derr
	}
	// Redeploy without rebuild (REQ-ENV-003): an image service pulls the same tag and gets the
	// digest below. A git service reuses the digest of its live release (build step of S-B).

	// 1. pull
	pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	ack, err := c.Sender.Send(pullCtx, node.id, &pb.Command{Body: &pb.Command_PullImage{PullImage: &pb.PullImage{ImageRef: cfg.Image, RegistryAuthRef: c.registryAuthRef(cfg.Image)}}})
	cancel()
	if err != nil {
		return fail(ReasonNodeUnavailable, "pull command: %v", err)
	}
	if !ack.GetOk() {
		reason := ReasonImagePullFailed
		if ack.GetErrorCode() == "docker_unavailable" {
			reason = ReasonNodeUnavailable
		}
		return fail(reason, "%s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
	}
	var res struct {
		ImageRef    string `json:"image_ref"`
		Digest      string `json:"digest"`
		ImageDigest string `json:"image_digest"`
	}
	_ = json.Unmarshal(ack.GetResultJson(), &res)
	imageRef, digest := res.ImageRef, res.Digest
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
		return fail(ReasonImagePullFailed, "the agent gave no image digest")
	}
	if !strings.Contains(imageRef, "@") {
		imageRef = imageRepo(cfg.Image) + "@" + digest
	}

	// 2. release
	releaseID := gateway.NewID("rel")
	if err := c.createRelease(ctx, depID, serviceID, releaseID, actorID, imageRef, digest, snap, oldRelease); err != nil {
		return fail(ReasonInvalidConfig, "create release: %v", err)
	}
	if e := step("image_ready", "image pulled", map[string]any{"release_id": releaseID, "image_ref": imageRef}); e != nil {
		return e
	}

	// 3. recreate: remove the old containers, start the new one
	oldPlacements, err := c.placementsOf(ctx, serviceID)
	if err != nil {
		return fail(ReasonContainerStartFailed, "read placements: %v", err)
	}
	for _, p := range oldPlacements {
		c.removeContainer(ctx, p[1], p[0])
	}
	placementID := gateway.NewID("plc")
	if e := step("starting", "starting the container", map[string]any{"placement_id": placementID}); e != nil {
		return e
	}
	err = c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		nodes := []string{node.id}
		for _, p := range oldPlacements {
			nodes = append(nodes, p[1])
		}
		if _, err := tx.Exec(ctx, `DELETE FROM placements WHERE service_id = $1`, serviceID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role)
			 VALUES ($1,$2,$3,$4,0,'blue','live')`, placementID, serviceID, node.id, releaseID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE services SET desired_state = 'running', updated_at = now() WHERE id = $1`, serviceID); err != nil {
			return nil, err
		}
		return nodes, nil
	})
	if err != nil {
		return fail(ReasonContainerStartFailed, "save placement: %v", err)
	}

	// 4. health
	if cfg.healthSpec() != nil {
		if e := step("health_checking", "waiting for the health check", nil); e != nil {
			return e
		}
		timeout := cfg.deployTimeout()
		hctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+30*time.Second)
		ack, err := c.Sender.Send(hctx, node.id, &pb.Command{Body: &pb.Command_CheckHealth{
			CheckHealth: &pb.CheckHealth{PlacementId: placementID, TimeoutSeconds: timeout}}})
		cancel()
		var derr *deployErr
		switch {
		case err != nil:
			derr = fail(ReasonNodeUnavailable, "health command: %v", err)
		case !ack.GetOk() && ack.GetErrorCode() == "container_start_failed":
			derr = fail(ReasonContainerStartFailed, "%s", ack.GetErrorMessage())
		case !ack.GetOk():
			derr = fail(ReasonHealthCheckFailed, "%s: %s", ack.GetErrorCode(), ack.GetErrorMessage())
		}
		if derr != nil {
			c.stopPlacement(ctx, placementID)
			return derr
		}
	}
	_, _ = c.DB.Exec(ctx, `UPDATE placements SET health = 'healthy', updated_at = now() WHERE id = $1`, placementID)

	// 5. switch: the route is in the desired state already, so we only record it
	if e := step("switching", "traffic goes to the new release", nil); e != nil {
		return e
	}
	if _, err := c.DB.Exec(ctx, `UPDATE services SET live_release_id = $2, updated_at = now() WHERE id = $1`, serviceID, releaseID); err != nil {
		return fail(ReasonContainerStartFailed, "set live release: %v", err)
	}
	if e := step("live", "deployment is live", nil); e != nil {
		return e
	}
	return nil
}

// stopPlacement sets a failed placement to stopped and tells the node.
func (c *Control) stopPlacement(ctx context.Context, placementID string) {
	err := c.change(ctx, func(tx pgx.Tx) ([]string, error) {
		return queryStrings(ctx, tx,
			`UPDATE placements SET role = 'stopped', health = 'unhealthy', updated_at = now() WHERE id = $1 RETURNING node_id`, placementID)
	})
	if err != nil {
		c.Log.Warn("stop failed placement", "placement", placementID, "err", err)
	}
}

func (c *Control) placementsOf(ctx context.Context, serviceID string) ([][2]string, error) {
	rows, err := c.DB.Query(ctx, `SELECT id, node_id FROM placements WHERE service_id = $1`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var p [2]string
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (c *Control) createRelease(ctx context.Context, depID, serviceID, releaseID, actorID, imageRef, digest string, snap *vars.Snapshot, previous *string) error {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "rel:"+serviceID); err != nil {
		return err
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	source, _ := json.Marshal(c.releaseSource(ctx, depID, actorID))
	variables, versions := releaseColumns(snap)
	_, err = tx.Exec(ctx,
		`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source, created_by)
		 VALUES ($1, $2, (SELECT COALESCE(MAX(number),0)+1 FROM releases WHERE service_id = $2), $3, $4, $5, $8, $9, $6, $7)`,
		releaseID, serviceID, imageRef, digest, []byte(snap.Config), source, actor, variables, versions)
	if err != nil {
		return err
	}
	if err := auditSecretUse(ctx, tx, snap, releaseID, actorID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET release_id = $2, previous_release_id = $3, updated_at = now() WHERE id = $1`,
		depID, releaseID, previous); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// imageRepo cuts the tag and the digest from an image name.
func imageRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// setState changes the deployment state and writes a step, a log line and an event.
func (c *Control) setState(ctx context.Context, depID, state, msg string, data map[string]any) error {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := c.Clock.Now()
	final := state == "live" || state == "failed"
	var finished *time.Time
	if final {
		finished = &now
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deployments SET state = $2, started_at = COALESCE(started_at, $3), finished_at = COALESCE($4, finished_at), updated_at = $3 WHERE id = $1`,
		depID, state, now, finished); err != nil {
		return err
	}
	if err := c.addStep(ctx, tx, depID, state, msg, data); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Control) addStep(ctx context.Context, tx pgx.Tx, depID, state, msg string, data map[string]any) error {
	var d []byte
	if data != nil {
		d, _ = json.Marshal(data)
	}
	msg = c.maskStep(ctx, depID, msg) // stored log lines never hold a secret value
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_steps (deployment_id, state, message, data) VALUES ($1,$2,$3,$4)`, depID, state, msg, d); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_logs (deployment_id, seq, stream, line)
		 SELECT $1, COALESCE(MAX(seq),0)+1, 'deploy', $2 FROM deployment_logs WHERE deployment_id = $1`, depID, state+": "+msg); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"state": state, "message": msg})
	_, err := tx.Exec(ctx,
		`INSERT INTO events (org_id, topic, object_id, payload)
		 SELECT p.org_id, 'deployment', d.id, $2::jsonb FROM deployments d
		   JOIN environments e ON e.id = d.environment_id JOIN projects p ON p.id = e.project_id
		  WHERE d.id = $1`, depID, payload)
	return err
}

// markFailed sets the failed state with the reason. It uses a fresh context if ctx is done.
func (c *Control) markFailed(ctx context.Context, depID string, e *deployErr) {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	now := c.Clock.Now()
	e = &deployErr{e.reason, c.maskStep(ctx, depID, e.detail)} // the detail goes to the database and the log
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		c.Log.Error("mark failed", "deployment", depID, "err", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx,
		`UPDATE deployments SET state = 'failed', failure_reason = $2, failure_detail = $3, finished_at = $4, updated_at = $4 WHERE id = $1`,
		depID, e.reason, e.detail, now)
	if err == nil {
		err = c.addStep(ctx, tx, depID, "failed", e.reason+": "+e.detail, map[string]any{"reason": e.reason})
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		c.Log.Error("mark failed", "deployment", depID, "err", err)
		return
	}
	c.Log.Warn("deployment failed", "deployment", depID, "reason", e.reason, "detail", e.detail)
}
