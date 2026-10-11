// SPDX-License-Identifier: Apache-2.0

package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Failure reasons of a deployment (08-deploy-engine section 2).
const (
	ReasonImagePullFailed      = "image_pull_failed"
	ReasonContainerStartFailed = "container_start_failed"
	ReasonHealthTimeout        = "health_timeout"
	ReasonHealthFailed         = "health_failed"
	ReasonPreDeployFailed      = "pre_deploy_failed"
	ReasonGate5xx              = "gate_5xx"
	ReasonGateLatency          = "gate_latency"
	ReasonSwitchFailed         = "switch_failed"
	ReasonNodeUnavailable      = "node_unavailable"
	ReasonInvalidConfig        = "invalid_config"
	ReasonCancelled            = "cancelled_by_user"
	ReasonSuperseded           = "superseded"
	ReasonInternal             = "internal_error"
	// ReasonServerRestarted is a deployment that a server restart cut before the switch (REQ-DEPLOY-107).
	ReasonServerRestarted = "server_restarted"
)

// deployErr ends a deployment. state is the terminal state: failed (default), rolled_back or cancelled.
type deployErr struct {
	reason, detail string
	state          string
}

func (e *deployErr) Error() string { return e.reason + ": " + e.detail }

func fail(reason, format string, a ...any) *deployErr {
	return &deployErr{reason: reason, detail: fmt.Sprintf(format, a...)}
}

// activeStates are the states of a deployment that runs now (not queued, not terminal).
const runningStates = `'building','image_ready','starting','health_checking','shifting','switching','draining'`

// transitions is the state machine of 08 section 2.
var transitions = map[string][]string{
	"queued":          {"building", "image_ready", "superseded", "cancelled"},
	"building":        {"image_ready", "failed", "cancelled"},
	"image_ready":     {"starting", "failed", "cancelled"},
	"starting":        {"health_checking", "failed", "cancelled"},
	"health_checking": {"switching", "shifting", "failed", "rolled_back", "cancelled"},
	"shifting":        {"switching", "rolled_back", "failed"},
	"switching":       {"draining", "rolled_back"},
	"draining":        {"live", "rolled_back"},
}

var terminalStates = map[string]bool{"live": true, "failed": true, "rolled_back": true, "cancelled": true, "superseded": true}

// activeDep is a deployment that this process runs.
type activeDep struct {
	cancel context.CancelCauseFunc
	locked bool // from switching on, a user cannot cancel
	// cmds carries promote and abort of a canary to the run that owns it.
	cmds chan string
}

var errUserCancel = errors.New("cancelled by the user")

// deployment is the row of a deployment that the runner needs.
type deployment struct {
	id, serviceID, envID, trigger, strategy, state string
	releaseID, prevReleaseID                       *string
	targetNode                                     string
	actorID                                        string
	opts                                           deployOptions
	created                                        time.Time
}

// Deploy queues a deployment with the settings of the service.
func (c *Control) Deploy(ctx context.Context, serviceID, actorID, trigger string) (string, error) {
	id, _, err := c.DeployWith(ctx, orchestrator.DeployParams{ServiceID: serviceID, ActorID: actorID, Trigger: trigger})
	return id, err
}

// Plan answers what DeployWith would do.
func (c *Control) Plan(ctx context.Context, p orchestrator.DeployParams) (orchestrator.Plan, error) {
	s, err := c.loadService(ctx, c.DB, p.ServiceID)
	if err != nil {
		return orchestrator.Plan{}, err
	}
	cfg := s.cfg
	if p.RollbackReleaseID != "" {
		rel, err := c.checkRollback(ctx, s, p)
		if err != nil {
			return orchestrator.Plan{}, err
		}
		cfg = rel.cfg
	}
	if p.Prepared != nil {
		if cfg, err = c.checkPrepared(s, p.Prepared); err != nil {
			return orchestrator.Plan{}, err
		}
	}
	return c.plan(ctx, s, cfg, p, resolveOptions(cfg, p))
}

// DeployWith stores a queued deployment (replacing older queued ones) and starts it when the service is free.
func (c *Control) DeployWith(ctx context.Context, p orchestrator.DeployParams) (string, []orchestrator.Warning, error) {
	trigger := p.Trigger
	if p.RollbackReleaseID != "" {
		trigger = "rollback"
	}
	if trigger == "" {
		trigger = "manual"
	}
	s, err := c.loadService(ctx, c.DB, p.ServiceID)
	if err != nil {
		return "", nil, err
	}
	if trigger != "move" && trigger != "failover" {
		if _, err := c.pickNodeForService(ctx, s.id); err != nil {
			return "", nil, orchestrator.ErrNoNode
		}
	}
	cfg := s.cfg
	var rollbackTo *string
	if p.RollbackReleaseID != "" {
		rel, err := c.checkRollback(ctx, s, p)
		if err != nil {
			return "", nil, err
		}
		cfg, rollbackTo = rel.cfg, &rel.id
	}
	if p.Prepared != nil {
		if cfg, err = c.checkPrepared(s, p.Prepared); err != nil {
			return "", nil, err
		}
	}
	opts := resolveOptions(cfg, p)
	p.Trigger = trigger
	plan, err := c.plan(ctx, s, cfg, p, opts)
	if err != nil {
		return "", nil, err
	}
	optsJSON, _ := json.Marshal(opts)
	warnings, _ := json.Marshal(plan.Warnings)
	if plan.Warnings == nil {
		warnings = []byte("[]")
	}

	id := gateway.NewID("dep")
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, s.id); err != nil {
		return "", nil, err
	}
	// A newer request replaces the older queued ones (08 section 10).
	old, err := queryStrings(ctx, tx, `UPDATE deployments SET state = 'superseded', failure_reason = 'superseded',
		finished_at = now(), updated_at = now() WHERE service_id = $1 AND state = 'queued' RETURNING id`, s.id)
	if err != nil {
		return "", nil, err
	}
	for _, o := range old {
		if err := c.addStep(ctx, tx, o, "superseded", "a newer deployment was queued: "+id, map[string]any{"by": id}); err != nil {
			return "", nil, err
		}
	}
	var actor *string
	if p.ActorID != "" {
		actor = &p.ActorID
	}
	if p.Prepared != nil {
		relID, err := c.storePrepared(ctx, tx, s, p.Prepared, p.ActorID)
		if err != nil {
			return "", nil, err
		}
		rollbackTo = &relID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployments (id, service_id, environment_id, trigger, strategy, strategy_options, state, actor_id, release_id, warnings, target_node_id, batch_id, after_deployment_id)
		 VALUES ($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,NULLIF($10,''),NULLIF($11,''),NULLIF($12,''))`,
		id, s.id, s.envID, trigger, plan.Strategy, optsJSON, actor, rollbackTo, warnings, p.TargetNodeID, p.BatchID, p.AfterDeploymentID); err != nil {
		return "", nil, err
	}
	if err := saveBuildRequest(ctx, tx, id); err != nil { // commit and no_cache of a git deploy (build.go)
		return "", nil, err
	}
	if err := c.addStep(ctx, tx, id, "queued", "deployment created", map[string]any{"strategy": plan.Strategy, "forced": plan.Forced}); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	for _, o := range old {
		c.settleDependents(ctx, o, "superseded")
	}
	c.pump(s.id)
	return id, plan.Warnings, nil
}

// pump starts the queued deployment of a service when no other one runs (08 section 10).
func (c *Control) pump(serviceID string) {
	ctx := c.runCtx
	if ctx.Err() != nil {
		return
	}
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, serviceID); err != nil {
		return
	}
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments WHERE service_id = $1 AND state IN (`+runningStates+`))`, serviceID).Scan(&busy); err != nil || busy {
		return
	}
	var id, trigger, sourceType string
	var releaseID *string
	// A deployment of a batch waits until the one before it is live (REQ-PROMO-008).
	err = tx.QueryRow(ctx, `SELECT d.id, d.trigger, s.source_type, d.release_id FROM deployments d JOIN services s ON s.id = d.service_id
		WHERE d.service_id = $1 AND d.state = 'queued'
		  AND (d.after_deployment_id IS NULL OR EXISTS (SELECT 1 FROM deployments a WHERE a.id = d.after_deployment_id AND a.state = 'live'))
		ORDER BY d.created_at DESC, d.id DESC LIMIT 1`, serviceID).Scan(&id, &trigger, &sourceType, &releaseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	if err != nil {
		c.Log.Error("pump", "service", serviceID, "err", err)
		return
	}
	state, msg := "image_ready", "resolving the image"
	if sourceType == "git" && !usesStoredRelease(trigger, releaseID != nil) {
		state, msg = "building", "building the image"
	}
	if err := c.setStateTx(ctx, tx, id, state, msg, nil); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			c.Log.Error("pump claim", "service", serviceID, "err", err)
		}
		return
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}
	c.startRun(id)
}

// startRun runs a claimed deployment in the background.
func (c *Control) startRun(id string) {
	rctx, cancel := context.WithCancelCause(c.runCtx)
	ad := &activeDep{cancel: cancel, cmds: make(chan string, 8)}
	c.mu.Lock()
	c.active[id] = ad
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel(nil)
		var serviceID string
		defer func() {
			c.mu.Lock()
			delete(c.active, id)
			c.mu.Unlock()
			if serviceID != "" {
				c.pump(serviceID)
			}
		}()
		d, err := c.loadDeployment(rctx, id)
		if err != nil {
			c.Log.Error("load deployment", "deployment", id, "err", err)
			return
		}
		serviceID = d.serviceID
		c.runDeployment(rctx, ad, d, false)
	}()
}

func (c *Control) loadDeployment(ctx context.Context, id string) (*deployment, error) {
	var d deployment
	var actor, target *string
	var opts []byte
	err := c.DB.QueryRow(ctx, `SELECT id, service_id, environment_id, trigger, strategy, state, release_id, previous_release_id, actor_id, strategy_options, created_at, target_node_id
		FROM deployments WHERE id = $1`, id).Scan(&d.id, &d.serviceID, &d.envID, &d.trigger, &d.strategy, &d.state, &d.releaseID, &d.prevReleaseID, &actor, &opts, &d.created, &target)
	if err != nil {
		return nil, err
	}
	if target != nil {
		d.targetNode = *target
	}
	if actor != nil {
		d.actorID = *actor
	}
	_ = json.Unmarshal(opts, &d.opts)
	return &d, nil
}

// Cancel cancels a deployment up to health_checking (08 section 10).
func (c *Control) Cancel(ctx context.Context, deploymentID, actorID string) error {
	var state string
	if err := c.DB.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1`, deploymentID).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.ErrNotFound
		}
		return err
	}
	switch state {
	case "queued":
		tx, err := c.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		tag, err := tx.Exec(ctx, `UPDATE deployments SET state = 'cancelled', failure_reason = $2, finished_at = now(), updated_at = now() WHERE id = $1 AND state = 'queued'`,
			deploymentID, ReasonCancelled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return orchestrator.ErrNotCancellable
		}
		if err := c.addStep(ctx, tx, deploymentID, "cancelled", "cancelled by the user", nil); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		c.settleDependents(ctx, deploymentID, "cancelled")
		return nil
	case "building", "image_ready", "starting", "health_checking":
		c.mu.Lock()
		ad := c.active[deploymentID]
		if ad == nil || ad.locked {
			c.mu.Unlock()
			return orchestrator.ErrNotCancellable
		}
		ad.cancel(errUserCancel)
		c.mu.Unlock()
		// Wait for the cleanup, so the answer shows the end state.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			var s string
			if err := c.DB.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1`, deploymentID).Scan(&s); err == nil && terminalStates[s] {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(50 * time.Millisecond):
			}
		}
		return nil
	}
	return orchestrator.ErrNotCancellable
}

// lockCancel stops user cancels from now on. It returns false when the user cancelled already.
func (c *Control) lockCancel(ad *activeDep) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ad != nil {
		ad.locked = true
	}
	return true
}

// ---- state changes ----

// setState moves the deployment to a state and writes a step, a log line and an event.
// A change that the state machine does not allow is refused (REQ-DEPLOY-100).
func (c *Control) setState(ctx context.Context, depID, state, msg string, data map[string]any) error {
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := c.setStateTx(ctx, tx, depID, state, msg, data); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *Control) setStateTx(ctx context.Context, tx pgx.Tx, depID, state, msg string, data map[string]any) error {
	var cur string
	if err := tx.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1 FOR UPDATE`, depID).Scan(&cur); err != nil {
		return err
	}
	if cur != state { // the same state again only adds a step
		allowed := false
		for _, n := range transitions[cur] {
			if n == state {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("deployment %s: the change %s to %s is not allowed", depID, cur, state)
		}
	}
	now := c.Clock.Now()
	var finished *time.Time
	if terminalStates[state] {
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
	if state == "live" && cur != "live" { // the alert event is in the same transaction as the state (REQ-ALERT-009)
		c.deploymentAlert(ctx, tx, depID, "deployment_live", "info")
	}
	return nil
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
	if err := addLog(ctx, tx, depID, "deploy", state+": "+msg); err != nil {
		return err
	}
	return addEvent(ctx, tx, depID, map[string]any{"type": "state", "state": state, "message": msg, "data": data})
}

func addLog(ctx context.Context, q dbq, depID, stream, line string) error {
	_, err := q.Exec(ctx,
		`INSERT INTO deployment_logs (deployment_id, seq, stream, line)
		 SELECT $1, COALESCE(MAX(seq),0)+1, $3, $2 FROM deployment_logs WHERE deployment_id = $1`, depID, line, stream)
	return err
}

func addEvent(ctx context.Context, q dbq, depID string, payload map[string]any) error {
	b, _ := json.Marshal(payload)
	_, err := q.Exec(ctx,
		`INSERT INTO events (org_id, topic, object_id, payload)
		 SELECT p.org_id, 'deployment', d.id, $2::jsonb FROM deployments d
		   JOIN environments e ON e.id = d.environment_id JOIN projects p ON p.id = e.project_id
		  WHERE d.id = $1`, depID, b)
	return err
}

// event writes a health or log event of a deployment outside of a state change.
func (c *Control) event(ctx context.Context, depID string, payload map[string]any) {
	if err := addEvent(ctx, c.DB, depID, payload); err != nil {
		c.Log.Warn("deployment event", "deployment", depID, "err", err)
	}
}

// endDeployment writes the terminal state with the reason. It uses a fresh context if ctx is done.
func (c *Control) endDeployment(ctx context.Context, depID string, e *deployErr) {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	state := e.state
	if state == "" {
		state = "failed"
	}
	e = &deployErr{reason: e.reason, detail: c.maskStep(ctx, depID, e.detail), state: state} // the detail goes to the database and the log
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		c.Log.Error("end deployment", "deployment", depID, "err", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// rolled_back needs the state machine to allow it from where the run stopped; else the deployment failed.
	var cur string
	if err := tx.QueryRow(ctx, `SELECT state FROM deployments WHERE id = $1`, depID).Scan(&cur); err == nil && state == "rolled_back" {
		allowed := false
		for _, n := range transitions[cur] {
			allowed = allowed || n == state
		}
		if !allowed {
			state = "failed"
		}
	}
	if err := c.setStateTx(ctx, tx, depID, state, e.reason+": "+e.detail, map[string]any{"reason": e.reason}); err != nil {
		c.Log.Error("end deployment", "deployment", depID, "err", err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET failure_reason = $2, failure_detail = $3, canary_routing = false, canary_paused = false WHERE id = $1`, depID, e.reason, e.detail); err != nil {
		c.Log.Error("end deployment", "deployment", depID, "err", err)
		return
	}
	switch state {
	case "failed":
		c.deploymentAlert(ctx, tx, depID, "deployment_failed", "critical")
	case "rolled_back":
		c.deploymentAlert(ctx, tx, depID, "deployment_rolled_back", "warning")
	}
	if err := tx.Commit(ctx); err != nil {
		c.Log.Error("end deployment", "deployment", depID, "err", err)
		return
	}
	c.Log.Warn("deployment ended", "deployment", depID, "state", state, "reason", e.reason, "detail", e.detail)
	c.settleDependents(ctx, depID, state)
}

// ---- commands ----

// cmdID is the command id of a step. A resumed deployment sends the same id again, so the agent
// answers with the stored ack (idempotent commands, 08 section 1).
func cmdID(depID, name string) string {
	h := sha256.Sum256([]byte(depID + "/" + name))
	return "cmd_" + hex.EncodeToString(h[:10])
}

// sendCmd sends a command of a deployment to a node and waits for the ack (at most timeout).
func (c *Control) sendCmd(ctx context.Context, node, depID, name string, body *pb.Command, timeout time.Duration) (*pb.CommandAck, error) {
	if c.Sender == nil {
		return nil, errors.New("no agent gateway")
	}
	body.CommandId = cmdID(depID, name)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Sender.Send(cctx, node, body)
}

type readyNode struct{ id string }

// pickNode returns the first ready node (oldest first). Scheduling by capacity comes later (09 section 7).
func (c *Control) pickNode(ctx context.Context) (readyNode, error) {
	var n readyNode
	err := c.DB.QueryRow(ctx, `SELECT id FROM nodes WHERE state = 'ready' ORDER BY created_at, id LIMIT 1`).Scan(&n.id)
	return n, err
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
