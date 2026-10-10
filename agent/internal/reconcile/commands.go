// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
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
		ack.ErrorCode, ack.ErrorMessage = code, msg
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
		ref, err := e.Docker.PullImage(ctx, b.PullImage.ImageRef, b.PullImage.RegistryAuthRef)
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
	case *agentv1.Command_StreamLogs:
		return e.streamLogs(ctx, cmd.CommandId, b.StreamLogs)
	case *agentv1.Command_StopWorkload:
		cs, err := e.byPlacement(ctx, b.StopWorkload.PlacementId)
		if err != nil {
			return fail(err, "docker_unavailable")
		}
		e.suppress(b.StopWorkload.PlacementId)
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
		for _, c := range cs {
			if err := e.Docker.Remove(ctx, c.ID, true); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fail(err, "internal_error")
			}
		}
		return []byte("{}"), "", ""
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

// checkHealth waits until the workload passes healthy_threshold probes in a row.
func (e *Engine) checkHealth(ctx context.Context, ch *agentv1.CheckHealth) ([]byte, string, string) {
	timeout := time.Duration(ch.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	passes, lastErr, wasRunning := 0, "container is not running", false
	for {
		w := e.workload(ch.PlacementId)
		interval, threshold := 2*time.Second, 2
		if w != nil && w.Health != nil {
			if w.Health.IntervalSeconds > 0 {
				interval = time.Duration(w.Health.IntervalSeconds) * time.Second
			}
			if w.Health.HealthyThreshold > 0 {
				threshold = int(w.Health.HealthyThreshold)
			}
		}
		if w == nil {
			passes, lastErr = 0, "workload is not in the desired state"
		} else if cs, err := e.byPlacement(ctx, ch.PlacementId); err == nil && len(cs) > 0 && cs[0].State == "running" {
			wasRunning = true
			if err := probe(ctx, w, cs[0].IP); err != nil {
				passes, lastErr = 0, err.Error()
			} else if passes++; passes >= threshold {
				return []byte("{}"), "", ""
			}
		} else {
			passes, lastErr = 0, "container is not running"
		}
		select {
		case <-ctx.Done():
			if !wasRunning {
				return nil, "container_start_failed", "container did not start in time: " + lastErr
			}
			return nil, "health_check_failed", "health check did not pass in time: " + lastErr
		case <-time.After(interval):
		}
	}
}

func probe(ctx context.Context, w *agentv1.WorkloadSpec, ip string) error {
	if ip == "" {
		return errors.New("container has no address")
	}
	hc := w.Health
	port := uint32(0)
	if hc != nil {
		port = hc.Port
	}
	if port == 0 && len(w.Ports) > 0 {
		port = w.Ports[0].ContainerPort
	}
	if port == 0 {
		return nil // nothing to probe: running is enough
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(int(port)))
	if hc != nil && hc.Type == "http" {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(pctx, http.MethodGet, "http://"+addr+hc.Path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		lo, hi := hc.ExpectedStatusMin, hc.ExpectedStatusMax
		if lo == 0 {
			lo, hi = 200, 399
		}
		if hi == 0 {
			hi = lo
		}
		if uint32(resp.StatusCode) < lo || uint32(resp.StatusCode) > hi {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
