// SPDX-License-Identifier: Apache-2.0

// Package stream keeps the mTLS stream to the server: Hello, desired states, commands,
// heartbeats, events and certificate renewal (09-multi-server, section 3).
package stream

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/agent/internal/join"
	"github.com/polipan/polipan/proto/agentv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

const (
	maxBackoff    = 30 * time.Second
	silenceLimit  = 45 * time.Second
	renewBefore   = 10 * 24 * time.Hour
	connectWindow = 10 * time.Second
)

// Handler is the reconciler as the stream sees it.
type Handler interface {
	OnConnect()
	OnUnreachable()
	SetDesired(ds *agentv1.DesiredState)
	HandleCommand(ctx context.Context, cmd *agentv1.Command)
	Version() int64
	Applied() (version int64, running uint32)
}

// Client is the stream client. It also implements reconcile.Sink.
type Client struct {
	Server    string
	DataDir   string
	Handler   Handler
	Docker    docker.Client // for container events, optional
	NodeInfo  func(ctx context.Context) *agentv1.NodeInfo
	Heartbeat time.Duration // default 5 s
	Log       *slog.Logger

	mu  sync.Mutex
	out chan *agentv1.AgentMessage
}

// Send queues a message for the current stream. It returns false when there is no stream.
func (c *Client) Send(m *agentv1.AgentMessage) bool {
	c.mu.Lock()
	out := c.out
	c.mu.Unlock()
	if out == nil {
		return false
	}
	select {
	case out <- m:
		return true
	default:
		return false
	}
}

// Run connects and reconnects until ctx ends.
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		reached, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		c.Log.Warn("stream ended", "error", err)
		if !reached {
			c.Handler.OnUnreachable()
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		wait := backoff + time.Duration(rand.Int63n(int64(backoff)/2+1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// session runs one connection. reached tells that the server was reachable.
func (c *Client) session(ctx context.Context) (reached bool, err error) {
	id, err := join.Load(c.DataDir)
	if err != nil {
		return false, err
	}
	conn, err := grpc.NewClient(c.Server, grpc.WithTransportCredentials(credentials.NewTLS(id.TLSConfig())))
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	conn.Connect()
	wctx, wcancel := context.WithTimeout(ctx, connectWindow)
	defer wcancel()
	for {
		s := conn.GetState()
		if s == connectivity.Ready {
			break
		}
		if s == connectivity.TransientFailure || s == connectivity.Shutdown {
			return false, errors.New("server is not reachable")
		}
		if !conn.WaitForStateChange(wctx, s) {
			return false, errors.New("timeout while connecting")
		}
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := agentv1.NewAgentGatewayClient(conn).Connect(sctx)
	if err != nil {
		return false, err
	}
	out := make(chan *agentv1.AgentMessage, 256)
	c.mu.Lock()
	c.out = out
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.out = nil
		c.mu.Unlock()
	}()
	c.Handler.OnConnect()
	c.Log.Info("connected to server", "server", c.Server)

	var info *agentv1.NodeInfo
	if c.NodeInfo != nil {
		info = c.NodeInfo(sctx)
	}
	if err := st.Send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Hello{Hello: &agentv1.Hello{
		NodeId: id.Node.NodeID, LastAppliedStateVersion: c.Handler.Version(), NodeInfo: info}}}); err != nil {
		return true, err
	}

	var wg sync.WaitGroup
	var lastRecv int64 // unix nano, guarded by mu
	touch := func() { c.mu.Lock(); lastRecv = time.Now().UnixNano(); c.mu.Unlock() }
	touch()
	fail := make(chan error, 4)
	spawn := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				select {
				case fail <- err:
				default:
				}
				cancel()
			}
		}()
	}
	spawn(func() error { // writer
		for {
			select {
			case <-sctx.Done():
				return nil
			case m := <-out:
				if err := st.Send(m); err != nil {
					return err
				}
			}
		}
	})
	spawn(func() error { c.heartbeats(sctx); return nil })
	spawn(func() error { c.events(sctx); return nil })
	spawn(func() error { c.renewLoop(sctx, id); return nil })
	spawn(func() error { // watchdog
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return nil
			case <-t.C:
				c.mu.Lock()
				silent := time.Since(time.Unix(0, lastRecv))
				c.mu.Unlock()
				if silent > silenceLimit {
					return errors.New("no message from the server")
				}
			}
		}
	})

	var recvErr error
	for {
		m, err := st.Recv()
		if err != nil {
			recvErr = err
			break
		}
		touch()
		switch b := m.Body.(type) {
		case *agentv1.ServerMessage_DesiredState:
			c.Handler.SetDesired(b.DesiredState)
		case *agentv1.ServerMessage_Command:
			go c.Handler.HandleCommand(sctx, b.Command)
		case *agentv1.ServerMessage_CertRenewed:
			if err := join.SaveCert(c.DataDir, b.CertRenewed.ClientCertPem); err != nil {
				c.Log.Error("cannot save renewed certificate", "error", err)
			} else {
				c.Log.Info("certificate renewed")
			}
		}
	}
	cancel()
	wg.Wait()
	select {
	case err := <-fail:
		return true, err
	default:
	}
	return true, recvErr
}

func (c *Client) heartbeats(ctx context.Context) {
	iv := c.Heartbeat
	if iv == 0 {
		iv = 5 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		ver, running := c.Handler.Applied()
		total, avail := join.Mem()
		_, disk := join.Disk(c.DataDir)
		c.Send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_Heartbeat{Heartbeat: &agentv1.Heartbeat{
			SentUnixMs: time.Now().UnixMilli(), AppliedStateVersion: ver, RunningContainers: running,
			CpuUsedMillis: uint64(join.Load1() * 1000), MemoryUsedBytes: total - avail, DiskUsedBytes: disk}}})
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// events forwards Docker container events of managed containers.
func (c *Client) events(ctx context.Context) {
	if c.Docker == nil {
		return
	}
	for ctx.Err() == nil {
		ch, err := c.Docker.Events(ctx)
		if err == nil {
			for ev := range ch {
				pid := ev.Labels["polipan.placement"]
				kind := map[string]string{"start": "started", "die": "died", "oom": "oom_killed", "restart": "restarted"}[ev.Action]
				if pid == "" || kind == "" {
					continue
				}
				c.Send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_ContainerEvent{ContainerEvent: &agentv1.ContainerEvent{
					PlacementId: pid, Kind: kind, ExitCode: int32(ev.ExitCode), TimeUnixMs: ev.Time.UnixMilli()}}})
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// renewLoop asks for a new certificate when less than 10 days remain.
func (c *Client) renewLoop(ctx context.Context, id *join.Identity) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if exp := id.CertExpiry(); !exp.IsZero() && time.Until(exp) < renewBefore {
			if csr, err := join.NewCSR(id.Key); err == nil {
				c.Send(&agentv1.AgentMessage{Body: &agentv1.AgentMessage_CertRenew{CertRenew: &agentv1.CertRenewRequest{CsrPem: csr}}})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
