// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/protobuf/proto"
)

const chunkSize = 64 * 1024

func (e *Engine) send(m *agentv1.AgentMessage) bool {
	if e.Sink == nil {
		return false
	}
	return e.Sink.Send(m)
}

// HandleCommand runs one command and sends the ack. A repeated command id gets the stored ack (REQ-AGENT-004).
func (e *Engine) HandleCommand(ctx context.Context, cmd *agentv1.Command) {
	if in := cmd.GetExecInput(); in != nil {
		e.execInput(in) // keys of a terminal: no ack, not stored
		return
	}
	id := cmd.CommandId
	if ack, ok := e.Store.Ack(id); ok {
		e.send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Ack{Ack: ack}})
		return
	}
	e.mu.Lock()
	if e.inflight[id] {
		e.mu.Unlock()
		return
	}
	e.inflight[id] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.inflight, id)
		e.mu.Unlock()
	}()

	ack := &agentv1.CommandAck{CommandId: id}
	if cmd.DeadlineUnixMs > 0 {
		if time.Now().UnixMilli() > cmd.DeadlineUnixMs {
			ack.ErrorCode, ack.ErrorMessage = "deadline_exceeded", "the deadline of the command has passed"
			e.finishAck(ack)
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(cmd.DeadlineUnixMs))
		defer cancel()
	}
	result, code, msg := e.run(ctx, cmd)
	if ctx.Err() != nil && code != "" {
		code, msg = "deadline_exceeded", "the deadline of the command has passed"
	}
	if code == "" {
		ack.Ok, ack.ResultJson = true, result
	} else {
		ack.ErrorCode, ack.ErrorMessage = code, e.scrub(msg)
	}
	e.finishAck(ack)
}

func (e *Engine) finishAck(ack *agentv1.CommandAck) {
	if err := e.Store.PutAck(ack); err != nil {
		e.Log.Error("cannot store command ack", "command_id", ack.CommandId, "error", err)
	}
	e.send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Ack{Ack: ack}})
}

// run returns a result, or an error code and message.
func (e *Engine) run(ctx context.Context, cmd *agentv1.Command) ([]byte, string, string) {
	fail := func(err error, def string) ([]byte, string, string) {
		return nil, codeFor(err, def), err.Error()
	}
	switch b := cmd.Body.(type) {
	case *agentv1.Command_PullImage:
		auth, err := e.pullAuth(b.PullImage.RegistryAuthRef)
		if err != nil {
			return fail(err, "image_pull_failed")
		}
		ref, err := e.Docker.PullImage(ctx, b.PullImage.ImageRef, auth)
		if err != nil {
			return fail(err, "image_pull_failed")
		}
		e.mu.Lock()
		e.pulled[b.PullImage.ImageRef], e.pulled[ref] = true, true
		e.mu.Unlock()
		digest := ref
		for i := 0; i+1 < len(ref); i++ {
			if ref[i] == '@' {
				digest = ref[i+1:]
			}
		}
		res, _ := json.Marshal(map[string]string{"image_digest": ref, "image_ref": ref, "digest": digest})
		return res, "", ""
	case *agentv1.Command_CheckHealth:
		return e.checkHealth(ctx, b.CheckHealth)
	case *agentv1.Command_ProbeHealth:
		return e.probeHealthNow(ctx, b.ProbeHealth.PlacementId)
	case *agentv1.Command_ProbeRoute:
		return e.probeRoute(ctx, b.ProbeRoute)
	case *agentv1.Command_RunOneOff:
		return e.runOneOff(ctx, b.RunOneOff)
	case *agentv1.Command_SwitchTraffic:
		hash, err := e.applyIngress(ctx, b.SwitchTraffic.Ingress)
		if err != nil {
			return nil, "caddy_load_failed", err.Error()
		}
		return jsonBytes(map[string]string{"config_hash": hash}), "", ""
	case *agentv1.Command_PruneImages:
		removed := 0
		for _, ref := range b.PruneImages.RemoveImageRefs {
			if err := e.Docker.ImageRemove(ctx, ref); err != nil {
				e.Log.Info("image not removed", "ref", ref, "error", err) // an image in use stays
				continue
			}
			removed++
			e.mu.Lock()
			delete(e.pulled, ref)
			e.mu.Unlock()
		}
		return jsonBytes(map[string]int{"removed": removed}), "", ""
	case *agentv1.Command_RunBackup:
		return e.runBackup(ctx, b.RunBackup)
	case *agentv1.Command_RunRestoreCheck:
		return e.runRestoreCheck(ctx, b.RunRestoreCheck)
	case *agentv1.Command_RestoreSnapshot:
		return e.restoreSnapshot(ctx, b.RestoreSnapshot)
	case *agentv1.Command_TestBackupTarget:
		return e.testBackupTarget(ctx, b.TestBackupTarget)
	case *agentv1.Command_DeleteSnapshots:
		return e.deleteSnapshots(ctx, b.DeleteSnapshots)
	case *agentv1.Command_StreamLogs:
		return e.streamLogs(ctx, cmd.CommandId, b.StreamLogs)
	case *agentv1.Command_StopWorkload:
		cs, err := e.byPlacement(ctx, b.StopWorkload.PlacementId)
		if err != nil {
			return fail(err, "docker_unavailable")
		}
		e.suppress(b.StopWorkload.PlacementId)
		e.closeExecs(b.StopWorkload.PlacementId)
		for _, c := range cs {
			if c.State != "running" {
				continue
			}
			if err := e.Docker.Stop(ctx, c.ID, int(b.StopWorkload.GraceSeconds)); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fail(err, "internal_error")
			}
		}
		return []byte("{}"), "", ""
	case *agentv1.Command_RemoveWorkload:
		cs, err := e.byPlacement(ctx, b.RemoveWorkload.PlacementId)
		if err != nil {
			return fail(err, "docker_unavailable")
		}
		e.suppress(b.RemoveWorkload.PlacementId)
		e.closeExecs(b.RemoveWorkload.PlacementId)
		service := ""
		if w := e.workload(b.RemoveWorkload.PlacementId); w != nil {
			service = w.ServiceId
		}
		for _, c := range cs {
			if err := e.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fail(err, "internal_error")
			}
			if service == "" {
				service = c.Labels[LabelService]
			}
		}
		e.removeFiles(b.RemoveWorkload.PlacementId)
		if !b.RemoveWorkload.RemoveVolumes {
			return []byte("{}"), "", "" // volumes are kept (REQ-VOL-004)
		}
		removed, err := e.removeVolumes(ctx, service)
		if err != nil {
			return fail(err, "internal_error")
		}
		res, _ := json.Marshal(map[string][]string{"removed_volumes": removed})
		return res, "", ""
	case *agentv1.Command_ExecSession:
		return e.execStart(ctx, cmd.CommandId, b.ExecSession)
	case *agentv1.Command_StartWorkload:
		w := b.StartWorkload.Workload
		if w == nil {
			return nil, "internal_error", "workload is missing"
		}
		e.mu.Lock()
		delete(e.suppressed, w.PlacementId)
		e.mu.Unlock()
		var ex *docker.Container
		if c, err := e.Docker.Inspect(ctx, w.ContainerName); err == nil {
			ex = c
		} else if !errors.Is(err, docker.ErrNotFound) {
			return fail(err, "docker_unavailable")
		}
		if w.Role == "" || w.Role == "stopped" {
			w = proto.Clone(w).(*agentv1.WorkloadSpec)
			w.Role = "live"
		}
		if err := e.ensure(ctx, w, ex); err != nil {
			return fail(err, "container_start_failed")
		}
		return []byte("{}"), "", ""
	}
	return nil, "unsupported_version", "this command is not supported by this agent"
}

func (e *Engine) suppress(placement string) {
	e.mu.Lock()
	e.suppressed[placement] = true
	e.mu.Unlock()
}

func (e *Engine) byPlacement(ctx context.Context, id string) ([]docker.Container, error) {
	return e.Docker.List(ctx, map[string]string{LabelPlacement: id})
}

func (e *Engine) workload(id string) *agentv1.WorkloadSpec {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.desired == nil {
		return nil
	}
	for _, w := range e.desired.Workloads {
		if w.PlacementId == id {
			return w
		}
	}
	return nil
}

func (e *Engine) streamLogs(ctx context.Context, cmdID string, sl *agentv1.StreamLogs) ([]byte, string, string) {
	cs, err := e.byPlacement(ctx, sl.PlacementId)
	if err != nil {
		return nil, codeFor(err, "docker_unavailable"), err.Error()
	}
	if len(cs) == 0 {
		return nil, "internal_error", "container not found"
	}
	text, err := e.Docker.Logs(ctx, cs[0].ID, 200)
	if err != nil {
		return nil, codeFor(err, "internal_error"), err.Error()
	}
	data := []byte(text)
	for len(data) > 0 {
		n := len(data)
		if n > chunkSize {
			n = chunkSize
		}
		e.send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Chunk{Chunk: &agentv1.StreamChunk{
			CommandId: cmdID, Stream: "log", Data: data[:n]}}})
		data = data[n:]
	}
	e.send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Chunk{Chunk: &agentv1.StreamChunk{
		CommandId: cmdID, Stream: "log", Eof: true}}})
	return []byte("{}"), "", ""
}
