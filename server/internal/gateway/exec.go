// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
)

// execBuffer is the number of output chunks that wait for the browser. When it is full the
// session ends: a slow browser must not stop the stream of the whole node.
const execBuffer = 1024

// ExecStream is an open terminal in a container on a node (REQ-APP-010). The output of the
// agent comes as StreamChunk of the ExecSession command id. It is not stored as a pending
// command, so it is not sent again after a reconnect: a lost stream ends the session.
type ExecStream struct {
	g      *Gateway
	nodeID string
	id     string
	s      *session
	out    chan []byte
	done   chan struct{}

	mu        sync.Mutex
	outClosed bool
	ack       *pb.CommandAck
	doneOnce  sync.Once
}

// OpenExec sends an ExecSession to the node. deadline is the longest life of the session on the agent.
func (g *Gateway) OpenExec(_ context.Context, nodeID string, es *pb.ExecSession, deadline time.Time) (*ExecStream, error) {
	g.mu.Lock()
	s := g.sessions[nodeID]
	g.mu.Unlock()
	if s == nil {
		return nil, ErrNodeNotConnected
	}
	x := &ExecStream{g: g, nodeID: nodeID, id: NewID("cmd"), s: s, out: make(chan []byte, execBuffer), done: make(chan struct{})}
	g.mu.Lock()
	if g.execs == nil {
		g.execs = map[string]*ExecStream{}
	}
	g.execs[x.id] = x
	g.mu.Unlock()
	cmd := &pb.Command{CommandId: x.id, DeadlineUnixMs: deadline.UnixMilli(), Body: &pb.Command_ExecSession{ExecSession: es}}
	if !s.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_Command{Command: cmd}}) {
		x.finish(nil)
		return nil, ErrNodeNotConnected
	}
	go func() {
		select {
		case <-s.ctx.Done(): // the node stream ended
			x.finish(nil)
		case <-x.done:
		}
	}()
	return x, nil
}

// ID is the command id of the session.
func (x *ExecStream) ID() string { return x.id }

// Output gives the terminal output. It is closed at the end of the session.
func (x *ExecStream) Output() <-chan []byte { return x.out }

// Done is closed when the agent acked the session or the session was closed.
func (x *ExecStream) Done() <-chan struct{} { return x.done }

// ExitCode returns the exit code from the ack, when there is one.
func (x *ExecStream) ExitCode() (int, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ack == nil || !x.ack.Ok {
		return 0, false
	}
	var r struct {
		ExitCode int `json:"exit_code"`
	}
	if json.Unmarshal(x.ack.ResultJson, &r) != nil {
		return 0, false
	}
	return r.ExitCode, true
}

// Input sends keys, a resize (rows and cols > 0) or the end of input to the agent.
func (x *ExecStream) Input(data []byte, rows, cols uint32, eof bool) error {
	in := &pb.ExecInput{SessionCommandId: x.id, Data: data, Rows: rows, Cols: cols, Eof: eof}
	cmd := &pb.Command{CommandId: NewID("cmd"), Body: &pb.Command_ExecInput{ExecInput: in}}
	if !x.s.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_Command{Command: cmd}}) {
		return ErrNodeNotConnected
	}
	return nil
}

// Close ends the session: the agent gets the end of input, later output is dropped.
func (x *ExecStream) Close() {
	select {
	case <-x.done:
		return
	default:
	}
	_ = x.Input(nil, 0, 0, true)
	x.finish(nil)
}

// finish stores the ack (may be nil), closes the output and forgets the session.
func (x *ExecStream) finish(ack *pb.CommandAck) {
	x.mu.Lock()
	if ack != nil {
		x.ack = ack
	}
	if !x.outClosed {
		close(x.out)
		x.outClosed = true
	}
	x.mu.Unlock()
	x.doneOnce.Do(func() { close(x.done) })
	x.g.mu.Lock()
	if x.g.execs[x.id] == x {
		delete(x.g.execs, x.id)
	}
	x.g.mu.Unlock()
}

func (g *Gateway) execFor(nodeID, id string) *ExecStream {
	g.mu.Lock()
	defer g.mu.Unlock()
	x := g.execs[id]
	if x == nil || x.nodeID != nodeID {
		return nil
	}
	return x
}

// execChunk routes a chunk to an exec session. It returns false when the chunk is not for one.
func (g *Gateway) execChunk(nodeID string, ch *pb.StreamChunk) bool {
	x := g.execFor(nodeID, ch.CommandId)
	if x == nil {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.outClosed {
		return true
	}
	if len(ch.Data) > 0 {
		select {
		case x.out <- ch.Data:
		default:
			g.Log.Warn("exec output buffer is full, the session ends", "node", nodeID, "session", x.id)
			close(x.out)
			x.outClosed = true
			go x.Close()
			return true
		}
	}
	if ch.Eof {
		close(x.out)
		x.outClosed = true
	}
	return true
}

// execAck routes the ack of an ExecSession. It returns false when the ack is not for one.
func (g *Gateway) execAck(nodeID string, ack *pb.CommandAck) bool {
	x := g.execFor(nodeID, ack.CommandId)
	if x == nil {
		return false
	}
	x.finish(ack)
	return true
}
