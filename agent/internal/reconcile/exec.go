// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// execSession is one open terminal (REQ-APP-010). Input is written in order by one goroutine.
type execSession struct {
	placement string
	in        chan *agentv1.ExecInput

	mu     sync.Mutex
	conn   docker.ExecConn // nil until Docker answered
	closed bool
}

func (s *execSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && s.conn != nil {
		_ = s.conn.Close()
	}
	s.closed = true
}

// attach sets the connection. It returns false when the session was closed meanwhile.
func (s *execSession) attach(c docker.ExecConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
	if s.closed {
		_ = c.Close()
		return false
	}
	return true
}

// execStart runs an ExecSession command: it attaches to a new exec in the running container of the
// placement, sends the output as StreamChunk (stream stdout) and ends with an eof chunk. The ack has
// the exit code. The session ends when the command ends, the container stops, or ctx ends.
func (e *Engine) execStart(ctx context.Context, cmdID string, es *agentv1.ExecSession) ([]byte, string, string) {
	cs, err := e.byPlacement(ctx, es.PlacementId)
	if err != nil {
		return nil, codeFor(err, "docker_unavailable"), err.Error()
	}
	id := ""
	for _, c := range cs {
		if c.State == "running" {
			id = c.ID
		}
	}
	if id == "" {
		return nil, "container_start_failed", "the container of this placement is not running"
	}
	cmd := es.Command
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	// The session is known before Docker answers, so early input waits in the queue.
	s := &execSession{placement: es.PlacementId, in: make(chan *agentv1.ExecInput, 256)}
	e.mu.Lock()
	e.execs[cmdID] = s
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.execs, cmdID)
		e.mu.Unlock()
		s.close()
	}()
	conn, err := e.Docker.Exec(ctx, id, docker.ExecOptions{Cmd: cmd, Tty: es.Tty, Rows: es.Rows, Cols: es.Cols})
	if err != nil {
		return nil, codeFor(err, "internal_error"), err.Error()
	}
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	if !s.attach(conn) {
		e.sendChunk(&agentv1.StreamChunk{CommandId: cmdID, Stream: "stdout", Eof: true})
		return []byte(`{"exit_code":-1}`), "", ""
	}
	done := make(chan struct{})
	defer close(done)
	go e.execInputLoop(ctx, s, done)

	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			e.sendChunk(&agentv1.StreamChunk{CommandId: cmdID, Stream: "stdout", Data: append([]byte(nil), buf[:n]...)})
		}
		if err != nil {
			break
		}
	}
	e.sendChunk(&agentv1.StreamChunk{CommandId: cmdID, Stream: "stdout", Eof: true})
	wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := conn.Wait(wctx)
	if err != nil {
		code = -1
	}
	res, _ := json.Marshal(map[string]int{"exit_code": code})
	return res, "", ""
}

// execInputLoop writes keys, resizes and the end of input in the order they came.
func (e *Engine) execInputLoop(ctx context.Context, s *execSession, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case in := <-s.in:
			if len(in.Data) > 0 {
				if _, err := s.conn.Write(in.Data); err != nil {
					s.close()
					return
				}
			}
			if in.Rows > 0 && in.Cols > 0 {
				_ = s.conn.Resize(ctx, in.Rows, in.Cols)
			}
			if in.Eof {
				_ = s.conn.CloseWrite()
			}
		}
	}
}

// execInput queues input for an open session. It has no ack: it is not stored as a command.
func (e *Engine) execInput(in *agentv1.ExecInput) {
	e.mu.Lock()
	s := e.execs[in.SessionCommandId]
	e.mu.Unlock()
	if s == nil {
		return
	}
	select {
	case s.in <- in:
	default:
		e.Log.Warn("exec input dropped, the queue is full", "session", in.SessionCommandId)
	}
}

// closeExecs ends the sessions of a placement, when its container stops or is removed.
func (e *Engine) closeExecs(placement string) {
	e.mu.Lock()
	var list []*execSession
	for _, s := range e.execs {
		if s.placement == placement {
			list = append(list, s)
		}
	}
	e.mu.Unlock()
	for _, s := range list {
		s.close()
	}
}

// sendChunk sends a chunk. The send queue can be full for a moment, so it tries again a few times.
func (e *Engine) sendChunk(ch *agentv1.StreamChunk) {
	m := &agentv1.AgentMessage{Body: &agentv1.AgentMessage_Chunk{Chunk: ch}}
	for i := 0; i < 50 && e.Sink != nil; i++ {
		if e.Sink.Send(m) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
