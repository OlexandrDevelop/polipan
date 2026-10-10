// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"

	"github.com/polipan/polipan/api/gen"
	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/logging"
	"github.com/polipan/polipan/server/internal/secrets"
	"github.com/polipan/polipan/server/internal/store"
)

// ExecGateway opens terminals on the nodes. *gateway.Gateway implements it.
type ExecGateway interface {
	OpenExec(ctx context.Context, nodeID string, es *pb.ExecSession, deadline time.Time) (*gateway.ExecStream, error)
}

const (
	// execIdle ends a session without input from the user (REQ-APP-010, REQ-SEC-103).
	execIdle = 15 * time.Minute
	// execMaxLife is the deadline of the session on the agent.
	execMaxLife = 12 * time.Hour
	// execMaxFrame is the largest frame the browser may send.
	execMaxFrame = 64 * 1024
)

// execIdleCheck is how often the idle time is checked. Tests make it short.
var execIdleCheck = 5 * time.Second

// execFrame is one WebSocket frame with its type. Binary frames carry terminal bytes both ways.
// A text frame from the browser is a control message: {"type":"resize","rows":R,"cols":C} or
// {"type":"eof"}. The server ends with the text frame {"type":"exit","code":N,"reason":"..."}.
type execFrame struct {
	data []byte
	text bool
}

var execCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		f := v.(execFrame)
		if f.text {
			return f.data, websocket.TextFrame, nil
		}
		return f.data, websocket.BinaryFrame, nil
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		f := v.(*execFrame)
		f.data, f.text = data, payloadType == websocket.TextFrame
		return nil
	},
}

// ExecService is the WebSocket terminal into a running container of the service (REQ-APP-010).
// The guard has checked service:exec. Every check that can fail answers before the upgrade:
// a developer on a protected environment gets 403 (AC-APP-07). Start and end are audited,
// without terminal content.
func (h *Handlers) ExecService(w http.ResponseWriter, r *http.Request, serviceID gen.ServiceId, params gen.ExecServiceParams) {
	s, err := h.Store.GetService(r.Context(), h.orgID(r), serviceID)
	if fail(w, r, err) {
		return
	}
	if denyProtected(w, r, "service:exec", s.EnvProtected) {
		return
	}
	if !h.sameOrigin(r) {
		WriteError(w, r, http.StatusForbidden, CodeForbidden, "the origin of the WebSocket is not allowed", map[string]any{"reason": "origin"})
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		invalid(w, r, "upgrade", "a WebSocket upgrade is needed")
		return
	}
	pl := runningPlacement(s, params.Replica)
	if pl == nil {
		WriteError(w, r, http.StatusConflict, CodeConflict, "the service has no running container", map[string]any{"reason": "not_running"})
		return
	}
	if h.Exec == nil {
		internalError(w, r, errors.New("exec is not available"))
		return
	}
	command := []string{"/bin/sh"}
	if params.Command != nil && strings.TrimSpace(*params.Command) != "" {
		command = strings.Fields(*params.Command)
	}
	start := h.Clock.Now()
	x, err := h.Exec.OpenExec(r.Context(), pl.NodeID, &pb.ExecSession{PlacementId: pl.ID, Command: command, Tty: true, Rows: 24, Cols: 80},
		start.Add(execMaxLife))
	if errors.Is(err, gateway.ErrNodeNotConnected) {
		WriteError(w, r, http.StatusConflict, CodeConflict, "the node of the container is not connected", map[string]any{"reason": "node_unavailable"})
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	audit := func(ctx context.Context, action string, diff map[string]any) error {
		p := PrincipalFrom(r.Context())
		diff["placement_id"], diff["node_id"], diff["session_id"] = pl.ID, pl.NodeID, x.ID()
		return secrets.WriteAudit(ctx, h.Store.Pool, secrets.AuditEvent{OrgID: s.OrgID, ActorType: "user", ActorID: p.User.ID,
			Action: action, TargetType: "service", TargetID: s.ID, ProjectID: s.ProjectID, Diff: diff,
			IP: clientKey(r), RequestID: RequestIDFrom(r.Context())})
	}
	if err := audit(r.Context(), "service.exec.start", map[string]any{"command": strings.Join(command, " ")}); err != nil {
		x.Close()
		internalError(w, r, err)
		return
	}
	reason, code := "upgrade_failed", -1
	srv := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil }, // the origin is checked above
		Handler: func(ws *websocket.Conn) {
			reason, code = h.relayExec(ws, x)
		},
	}
	srv.ServeHTTP(hijackable(w), r)
	x.Close()
	end := map[string]any{"reason": reason, "duration_ms": h.Clock.Now().Sub(start).Milliseconds()}
	if code >= 0 {
		end["exit_code"] = code
	}
	if err := audit(context.WithoutCancel(r.Context()), "service.exec.end", end); err != nil {
		logging.From(r.Context()).Error("exec end audit failed", "error", err)
	}
}

// relayExec copies browser input to the agent and the output back, until the command ends,
// the browser leaves or the session is idle for 15 minutes. It returns the end reason and the exit code.
func (h *Handlers) relayExec(ws *websocket.Conn, x *gateway.ExecStream) (string, int) {
	_ = ws.SetDeadline(time.Time{}) // the HTTP server timeouts do not apply to a terminal
	ws.MaxPayloadBytes = execMaxFrame
	var last atomic.Int64
	last.Store(h.Clock.Now().UnixNano())

	inDone := make(chan struct{})
	go func() {
		defer close(inDone)
		for {
			var f execFrame
			if err := execCodec.Receive(ws, &f); err != nil {
				return
			}
			last.Store(h.Clock.Now().UnixNano())
			if !f.text {
				if len(f.data) > 0 && x.Input(f.data, 0, 0, false) != nil {
					return
				}
				continue
			}
			var c struct {
				Type       string `json:"type"`
				Rows, Cols uint32
			}
			if json.Unmarshal(f.data, &c) != nil {
				continue
			}
			switch c.Type {
			case "resize":
				if c.Rows > 0 && c.Cols > 0 && c.Rows < 1000 && c.Cols < 1000 {
					_ = x.Input(nil, c.Rows, c.Cols, false)
				}
			case "eof":
				_ = x.Input(nil, 0, 0, true)
			}
		}
	}()
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		for data := range x.Output() {
			if execCodec.Send(ws, execFrame{data: data}) != nil {
				x.Close()
			}
		}
	}()

	tick := time.NewTicker(execIdleCheck)
	defer tick.Stop()
	reason := "ended"
loop:
	for {
		select {
		case <-outDone:
			break loop
		case <-inDone:
			reason = "client_closed"
			break loop
		case <-tick.C:
			if h.Clock.Now().Sub(time.Unix(0, last.Load())) >= execIdle {
				reason = "idle_timeout"
				break loop
			}
		}
	}
	if reason == "ended" {
		// The output ended: the ack with the exit code follows soon.
		select {
		case <-x.Done():
		case <-time.After(5 * time.Second):
		}
	}
	x.Close()
	<-outDone
	code, ok := x.ExitCode()
	if !ok {
		code = -1
	}
	msg, _ := json.Marshal(map[string]any{"type": "exit", "code": code, "reason": reason})
	_ = execCodec.Send(ws, execFrame{data: msg, text: true})
	_ = ws.Close()
	return reason, code
}

// runningPlacement picks the live placement of a running service: the asked replica or the first.
func runningPlacement(s *store.Service, replica *int) *store.Placement {
	if s.DesiredState == "stopped" {
		return nil
	}
	var best *store.Placement
	for i := range s.Placements {
		p := &s.Placements[i]
		if p.Role != "live" || (replica != nil && p.ReplicaIndex != *replica) {
			continue
		}
		if best == nil || p.ReplicaIndex < best.ReplicaIndex {
			best = p
		}
	}
	return best
}

// sameOrigin checks the Origin header of the upgrade: a page of another site must not open a
// terminal with the cookie of the user. A client without Origin (the CLI) is allowed.
func (h *Handlers) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	ext, err := url.Parse(h.Settings.ExternalURL)
	return err == nil && ext.Host != "" && strings.EqualFold(u.Host, ext.Host)
}

// hijackable returns the first writer under the middleware wrappers that can be hijacked.
func hijackable(w http.ResponseWriter) http.ResponseWriter {
	for {
		if _, ok := w.(http.Hijacker); ok {
			return w
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}
