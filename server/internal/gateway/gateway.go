// SPDX-License-Identifier: Apache-2.0

// Package gateway is the gRPC side for the agents: AgentJoin (TLS) and AgentGateway (mTLS stream).
// It also keeps the open streams and sends commands to the nodes.
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// ErrNodeNotConnected is returned when a command goes to a node without an open stream.
var ErrNodeNotConnected = errors.New("gateway: node is not connected")

// StateProvider builds the desired state of a node. The control package implements it.
type StateProvider interface {
	DesiredState(ctx context.Context, nodeID string) (*pb.DesiredState, error)
}

// HealthHook is told about a change of the health of a placement (REQ-HC-007).
type HealthHook interface {
	HealthChanged(ctx context.Context, nodeID, placementID, from, to string)
}

// Deps are the parts the gateway needs.
type Deps struct {
	DB     *pgxpool.Pool
	CA     *pki.CA
	Config config.Config
	Clock  clock.Clock // default clock.Real
	Log    *slog.Logger
	State  StateProvider
	// Health is told when the health of a placement changes (optional). Control sets it after New.
	Health HealthHook
	// PingInterval (default 15 s) and IdleTimeout (default 30 s) can be changed by tests.
	PingInterval time.Duration
	IdleTimeout  time.Duration
}

// Gateway serves AgentJoin and AgentGateway.
type Gateway struct {
	Deps
	grpc *grpc.Server

	mu       sync.Mutex
	sessions map[string]*session
	pending  map[string]map[string]*pending // node id -> command id -> pending
	execs    map[string]*ExecStream         // open terminals by command id

	certMu sync.Mutex
	cert   *tls.Certificate
}

// New creates the gateway.
func New(d Deps) (*Gateway, error) {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.PingInterval == 0 {
		d.PingInterval = 15 * time.Second
	}
	if d.IdleTimeout == 0 {
		d.IdleTimeout = 30 * time.Second
	}
	g := &Gateway{
		Deps:     d,
		sessions: map[string]*session{},
		pending:  map[string]map[string]*pending{},
	}
	if _, err := g.serverCert(); err != nil {
		return nil, err
	}
	creds := credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		// Join has no client cert. Connect checks the verified cert in the handler.
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      d.CA.Pool(),
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return g.serverCert() },
	})
	g.grpc = grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(4<<20), grpc.MaxSendMsgSize(4<<20))
	pb.RegisterAgentJoinServer(g.grpc, &joinServer{g: g})
	pb.RegisterAgentGatewayServer(g.grpc, &gatewayServer{g: g})
	return g, nil
}

// serverCert returns the gateway certificate. It is made at start and renewed when less than 30 days remain.
func (g *Gateway) serverCert() (*tls.Certificate, error) {
	g.certMu.Lock()
	defer g.certMu.Unlock()
	if g.cert != nil && time.Until(g.cert.Leaf.NotAfter) > 30*24*time.Hour {
		return g.cert, nil
	}
	c, err := g.CA.ServerCert(g.Config.AgentHosts)
	if err != nil {
		return nil, err
	}
	g.cert = c
	return c, nil
}

// Serve serves gRPC on l until ctx ends. It then stops the server.
func (g *Gateway) Serve(ctx context.Context, l net.Listener) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			g.grpc.Stop()
		case <-done:
		}
	}()
	err := g.grpc.Serve(l)
	close(done)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// Connected tells if the node has an open stream.
func (g *Gateway) Connected(nodeID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[nodeID] != nil
}

// Push sends a desired state to the stream of the node. It returns false if the node is not connected.
func (g *Gateway) Push(nodeID string, ds *pb.DesiredState) bool {
	g.mu.Lock()
	s := g.sessions[nodeID]
	g.mu.Unlock()
	if s == nil {
		return false
	}
	return s.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_DesiredState{DesiredState: ds}})
}

// Disconnect closes the stream of a node (for example after the node was deleted).
func (g *Gateway) Disconnect(nodeID string) {
	g.mu.Lock()
	s := g.sessions[nodeID]
	g.mu.Unlock()
	if s != nil {
		s.cancel()
	}
}

// pending is a command that waits for its ack.
type pending struct {
	cmd     *pb.Command
	done    chan struct{}
	once    sync.Once
	ack     *pb.CommandAck
	collect bool
	buf     []byte
}

func (p *pending) finish() { p.once.Do(func() { close(p.done) }) }

// Send sends a command and waits for the ack. The deadline is the one of ctx
// (or 60 s when ctx has none) and goes into the command. A failed ack is returned
// as a normal ack (ok=false). The error is for transport problems.
func (g *Gateway) Send(ctx context.Context, nodeID string, cmd *pb.Command) (*pb.CommandAck, error) {
	_, ack, err := g.send(ctx, nodeID, cmd, false)
	return ack, err
}

// SendCollect is like Send, but it also collects StreamChunk data of the command until eof
// (used for one-shot logs). It returns when the eof chunk or the ack comes.
func (g *Gateway) SendCollect(ctx context.Context, nodeID string, cmd *pb.Command) ([]byte, *pb.CommandAck, error) {
	return g.send(ctx, nodeID, cmd, true)
}

func (g *Gateway) send(ctx context.Context, nodeID string, cmd *pb.Command, collect bool) ([]byte, *pb.CommandAck, error) {
	g.mu.Lock()
	s := g.sessions[nodeID]
	g.mu.Unlock()
	if s == nil {
		return nil, nil, ErrNodeNotConnected
	}
	dl, ok := ctx.Deadline()
	if !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		dl, _ = ctx.Deadline()
	}
	if cmd.CommandId == "" {
		cmd.CommandId = NewID("cmd")
	}
	cmd.DeadlineUnixMs = dl.UnixMilli()
	p := &pending{cmd: cmd, done: make(chan struct{}), collect: collect}

	g.mu.Lock()
	if g.pending[nodeID] == nil {
		g.pending[nodeID] = map[string]*pending{}
	}
	g.pending[nodeID][cmd.CommandId] = p
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending[nodeID], cmd.CommandId)
		g.mu.Unlock()
	}()

	if !s.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_Command{Command: cmd}}) {
		// The stream is closing. The command stays pending and is re-sent on reconnect.
		g.Log.Debug("command queued for reconnect", "node", nodeID, "command", cmd.CommandId)
	}
	select {
	case <-p.done:
		g.mu.Lock()
		defer g.mu.Unlock()
		return p.buf, p.ack, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// onAck routes an ack to the waiting command.
func (g *Gateway) onAck(nodeID string, ack *pb.CommandAck) {
	if g.execAck(nodeID, ack) {
		return
	}
	g.mu.Lock()
	p := g.pending[nodeID][ack.CommandId]
	if p != nil {
		p.ack = ack
	}
	g.mu.Unlock()
	if p != nil && (!p.collect || !ack.Ok) {
		p.finish() // a collecting command with ok=true waits for the eof chunk
	}
}

// onChunk adds data to a collecting command. The eof chunk ends it.
func (g *Gateway) onChunk(nodeID string, ch *pb.StreamChunk) {
	if g.execChunk(nodeID, ch) {
		return
	}
	g.mu.Lock()
	p := g.pending[nodeID][ch.CommandId]
	if p != nil && p.collect {
		p.buf = append(p.buf, ch.Data...)
		if ch.Eof && p.ack == nil {
			p.ack = &pb.CommandAck{CommandId: ch.CommandId, Ok: true}
		}
	}
	g.mu.Unlock()
	if p != nil && p.collect && ch.Eof {
		p.finish()
	}
}
