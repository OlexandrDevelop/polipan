// SPDX-License-Identifier: Apache-2.0

// Package gatewaytest starts a real gateway on 127.0.0.1 and gives a fake agent for tests.
package gatewaytest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/config"
	"github.com/polipan/polipan/server/internal/db"
	"github.com/polipan/polipan/server/internal/db/dbtest"
	"github.com/polipan/polipan/server/internal/gateway"
	"github.com/polipan/polipan/server/internal/pki"
	"github.com/polipan/polipan/server/internal/shared/clock"
)

// Env is a test server: database, CA and gateway.
type Env struct {
	Pool  *pgxpool.Pool
	CA    *pki.CA
	GW    *gateway.Gateway
	Cfg   config.Config
	Clock *clock.Fake
	Addr  string
	OrgID string
}

// Start makes a database, a CA and a gateway on a random local port. It stops when the test ends.
func Start(t *testing.T) *Env {
	t.Helper()
	pool := dbtest.NewDatabase(t, dbtest.Admin(t))
	ctx := context.Background()
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	ca, err := pki.Load(ctx, pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DatabaseURL = "unused"
	clk := clock.NewFake(time.Now().UTC())
	gw, err := gateway.New(gateway.Deps{DB: pool, CA: ca, Config: cfg, Clock: clk, PingInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = gw.Serve(sctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	gw.State = stubState{}
	orgID := gateway.NewID("org")
	if _, err := pool.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1,'Test','test')`, orgID); err != nil {
		t.Fatal(err)
	}
	return &Env{Pool: pool, CA: ca, GW: gw, Cfg: cfg, Clock: clk, Addr: l.Addr().String(), OrgID: orgID}
}

type stubState struct{}

func (stubState) DesiredState(context.Context, string) (*pb.DesiredState, error) {
	return &pb.DesiredState{Version: 1}, nil
}

// AddToken stores a join token that is valid for ttl (a negative ttl gives an expired token).
func (e *Env) AddToken(t *testing.T, name string, ttl time.Duration) string {
	t.Helper()
	token := "pj_" + gateway.NewID("t")
	_, err := e.Pool.Exec(context.Background(),
		`INSERT INTO node_join_tokens (id, org_id, token_hash, node_name, labels, expires_at) VALUES ($1,$2,$3,$4,'{"a":"b"}',$5)`,
		gateway.NewID("jtk"), e.OrgID, gateway.HashToken(token), name, e.Clock.Now().Add(ttl))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// Agent is a fake agent.
type Agent struct {
	Env     *Env
	NodeID  string
	CertPEM []byte
	Key     *ecdsa.PrivateKey

	Stream   pb.AgentGateway_ConnectClient
	States   chan *pb.DesiredState
	Commands chan *pb.Command
	// OnCommand answers a command with an ack. If it is nil, commands go to Commands.
	OnCommand func(*pb.Command) *pb.CommandAck
	// Async runs each command in its own goroutine, so a slow command does not hold the others.
	Async  bool
	sendMu sync.Mutex
	conn   *grpc.ClientConn
}

// NewKeyAndCSR makes a key and a CSR in PEM.
func NewKeyAndCSR(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

// JoinConn dials the gateway over TLS without a client certificate.
func (e *Env) JoinConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(e.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: e.CA.Pool(), MinVersion: tls.VersionTLS13, ServerName: "127.0.0.1",
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Join does the Join call with the token. The error is returned as the gRPC error.
func (e *Env) Join(t *testing.T, token string) (*Agent, error) {
	t.Helper()
	key, csr := NewKeyAndCSR(t)
	resp, err := pb.NewAgentJoinClient(e.JoinConn(t)).Join(context.Background(), &pb.JoinRequest{
		JoinToken: token, CsrPem: csr,
		NodeInfo: &pb.NodeInfo{Hostname: "h", Os: "linux", Arch: "amd64", AgentVersion: "test"},
	})
	if err != nil {
		return nil, err
	}
	return &Agent{Env: e, NodeID: resp.NodeId, CertPEM: resp.ClientCertPem, Key: key,
		States: make(chan *pb.DesiredState, 64), Commands: make(chan *pb.Command, 64)}, nil
}

// NewReadyNode joins an agent, connects it and sends Hello. It waits for the first desired state.
func (e *Env) NewReadyNode(t *testing.T, name string) *Agent {
	t.Helper()
	a, err := e.Join(t, e.AddToken(t, name, time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a.Connect(t)
	a.WaitState(t)
	return a
}

// Connect opens the mTLS stream and sends Hello.
func (a *Agent) Connect(t *testing.T) {
	t.Helper()
	keyDER, err := x509.MarshalECPrivateKey(a.Key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(a.CertPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(a.Env.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: a.Env.CA.Pool(), Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, ServerName: "127.0.0.1",
	})))
	if err != nil {
		t.Fatal(err)
	}
	a.conn = conn
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Stream, err = pb.NewAgentGatewayClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_Hello{Hello: &pb.Hello{NodeId: a.NodeID}}})
	go a.loop()
}

func (a *Agent) loop() {
	for {
		m, err := a.Stream.Recv()
		if err != nil {
			return
		}
		switch b := m.Body.(type) {
		case *pb.ServerMessage_DesiredState:
			a.States <- b.DesiredState
		case *pb.ServerMessage_Command:
			if a.OnCommand == nil {
				a.Commands <- b.Command
				continue
			}
			answer := func(c *pb.Command) {
				ack := a.OnCommand(c)
				ack.CommandId = c.CommandId
				a.sendMu.Lock()
				defer a.sendMu.Unlock()
				_ = a.Stream.Send(&pb.AgentMessage{Body: &pb.AgentMessage_Ack{Ack: ack}})
			}
			if a.Async {
				go answer(b.Command)
			} else {
				answer(b.Command)
			}
		}
	}
}

// Send sends a message to the server.
func (a *Agent) Send(t *testing.T, m *pb.AgentMessage) {
	t.Helper()
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if err := a.Stream.Send(m); err != nil {
		t.Fatal(err)
	}
}

// WaitState waits for the next desired state.
func (a *Agent) WaitState(t *testing.T) *pb.DesiredState {
	t.Helper()
	select {
	case ds := <-a.States:
		return ds
	case <-time.After(10 * time.Second):
		t.Fatal("no desired state")
		return nil
	}
}

// Heartbeat sends a heartbeat.
func (a *Agent) Heartbeat(t *testing.T) {
	t.Helper()
	a.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
}
