// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/pki"
)

// session is one open stream of one node.
type session struct {
	nodeID string
	out    chan *pb.ServerMessage
	ctx    context.Context
	cancel context.CancelFunc
}

// enqueue puts a message in the send queue. It returns false if the session is closed.
func (s *session) enqueue(m *pb.ServerMessage) bool {
	select {
	case <-s.ctx.Done():
		return false
	default:
	}
	select {
	case s.out <- m:
		return true
	case <-s.ctx.Done():
		return false
	}
}

type gatewayServer struct {
	pb.UnimplementedAgentGatewayServer
	g *Gateway
}

type recvResult struct {
	msg *pb.AgentMessage
	err error
}

// Connect is the agent stream. Order: Hello, DesiredState, ActualState, heartbeats (09 section 3).
func (s *gatewayServer) Connect(stream pb.AgentGateway_ConnectServer) error {
	g := s.g
	ctx := stream.Context()
	nodeID, serial, err := peerNode(ctx)
	if err != nil {
		return err
	}
	if revoked, err := pki.IsRevoked(ctx, g.DB, serial); err != nil {
		return status.Error(codes.Internal, "database error")
	} else if revoked {
		return status.Error(codes.PermissionDenied, "certificate is revoked")
	}
	var exists bool
	if err := g.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = $1)`, nodeID).Scan(&exists); err != nil {
		return status.Error(codes.Internal, "database error")
	}
	if !exists {
		return status.Error(codes.PermissionDenied, "node is unknown or deleted")
	}

	// Read messages in a goroutine, so a replaced session can stop at once.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	msgs := make(chan recvResult)
	go func() {
		for {
			m, err := stream.Recv()
			select {
			case msgs <- recvResult{m, err}:
			case <-sctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// First message must be Hello.
	var hello *pb.Hello
	select {
	case r := <-msgs:
		if r.err != nil {
			return r.err
		}
		hello = r.msg.GetHello()
	case <-time.After(g.IdleTimeout):
		return status.Error(codes.DeadlineExceeded, "no hello")
	case <-sctx.Done():
		return sctx.Err()
	}
	if hello == nil {
		return status.Error(codes.FailedPrecondition, "the first message must be hello")
	}
	if hello.GetNodeId() != "" && hello.GetNodeId() != nodeID {
		return status.Error(codes.PermissionDenied, "hello node id does not match the certificate")
	}

	sess := &session{nodeID: nodeID, out: make(chan *pb.ServerMessage, 64), ctx: sctx, cancel: cancel}
	g.register(sess)
	defer g.unregister(sess)

	if err := g.onHello(ctx, nodeID, hello); err != nil {
		g.Log.Error("hello", "node", nodeID, "err", err)
		return status.Error(codes.Internal, "database error")
	}
	if g.State != nil {
		ds, err := g.State.DesiredState(ctx, nodeID)
		if err != nil {
			g.Log.Error("desired state", "node", nodeID, "err", err)
			return status.Error(codes.Internal, "desired state error")
		}
		sess.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_DesiredState{DesiredState: ds}})
	}
	// Commands that were not acked before the reconnect are sent again (09 section 4).
	g.mu.Lock()
	var again []*pb.Command
	for _, p := range g.pending[nodeID] {
		again = append(again, p.cmd)
	}
	g.mu.Unlock()
	for _, c := range again {
		sess.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_Command{Command: c}})
	}

	// Writer: one goroutine sends, so Send is never called twice at once.
	writeErr := make(chan error, 1)
	go func() {
		ping := time.NewTicker(g.PingInterval)
		defer ping.Stop()
		for {
			var m *pb.ServerMessage
			select {
			case m = <-sess.out:
			case <-ping.C:
				m = &pb.ServerMessage{Body: &pb.ServerMessage_Ping{Ping: &pb.Ping{}}}
			case <-sctx.Done():
				return
			}
			if err := stream.Send(m); err != nil {
				writeErr <- err
				return
			}
		}
	}()

	idle := time.NewTimer(g.IdleTimeout)
	defer idle.Stop()
	for {
		select {
		case r := <-msgs:
			if r.err != nil {
				return r.err
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(g.IdleTimeout)
			g.handle(ctx, sess, r.msg)
		case err := <-writeErr:
			return err
		case <-idle.C:
			return status.Error(codes.DeadlineExceeded, "no data from the agent")
		case <-sctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return status.Error(codes.Aborted, "stream was replaced or closed")
		}
	}
}

// peerNode reads the node id and the serial from the verified client certificate.
func peerNode(ctx context.Context) (nodeID, serial string, err error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", "", status.Error(codes.Unauthenticated, "no peer")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return "", "", status.Error(codes.Unauthenticated, "client certificate required")
	}
	leaf := ti.State.VerifiedChains[0][0]
	cn := leaf.Subject.CommonName
	if !strings.HasPrefix(cn, "nod_") {
		return "", "", status.Error(codes.PermissionDenied, "certificate subject is not a node")
	}
	return cn, pki.SerialHex(leaf), nil
}

func (g *Gateway) register(s *session) {
	g.mu.Lock()
	old := g.sessions[s.nodeID]
	g.sessions[s.nodeID] = s
	g.mu.Unlock()
	if old != nil {
		old.cancel() // a new Connect replaces the old stream
	}
}

func (g *Gateway) unregister(s *session) {
	g.mu.Lock()
	if g.sessions[s.nodeID] == s {
		delete(g.sessions, s.nodeID)
	}
	g.mu.Unlock()
	s.cancel()
}

func (g *Gateway) nodeEvent(ctx context.Context, nodeID string, payload map[string]any) {
	b, _ := json.Marshal(payload)
	_, err := g.DB.Exec(ctx,
		`INSERT INTO events (org_id, topic, object_id, payload) SELECT org_id, 'node', id, $2 FROM nodes WHERE id = $1`,
		nodeID, b)
	if err != nil {
		g.Log.Warn("event", "err", err)
	}
}

// onHello stores the node info and moves the node to ready (from pending, suspect or down).
func (g *Gateway) onHello(ctx context.Context, nodeID string, h *pb.Hello) error {
	info := h.GetNodeInfo()
	var before string
	err := g.DB.QueryRow(ctx, `SELECT state FROM nodes WHERE id = $1`, nodeID).Scan(&before)
	if err != nil {
		return err
	}
	_, err = g.DB.Exec(ctx,
		`UPDATE nodes SET
		   state = CASE WHEN state IN ('pending','suspect','down') THEN 'ready' ELSE state END,
		   last_heartbeat_at = $2,
		   acked_state_version = GREATEST(acked_state_version, $3),
		   os = COALESCE(NULLIF($4,''), os), arch = COALESCE(NULLIF($5,''), arch),
		   docker_version = COALESCE(NULLIF($6,''), docker_version),
		   agent_version = COALESCE(NULLIF($7,''), agent_version),
		   updated_at = $2
		 WHERE id = $1`,
		nodeID, g.Clock.Now(), h.GetLastAppliedStateVersion(),
		info.GetOs(), info.GetArch(), info.GetDockerVersion(), info.GetAgentVersion())
	if err != nil {
		return err
	}
	if before == "pending" || before == "suspect" || before == "down" {
		g.nodeEvent(ctx, nodeID, map[string]any{"kind": "state", "from": before, "to": "ready"})
	}
	return nil
}

// handle processes one message after Hello.
func (g *Gateway) handle(ctx context.Context, s *session, m *pb.AgentMessage) {
	nodeID := s.nodeID
	var err error
	switch b := m.Body.(type) {
	case *pb.AgentMessage_Heartbeat:
		err = g.onHeartbeat(ctx, nodeID, b.Heartbeat)
	case *pb.AgentMessage_ActualState:
		err = g.onActualState(ctx, nodeID, b.ActualState)
	case *pb.AgentMessage_Ack:
		g.onAck(nodeID, b.Ack)
	case *pb.AgentMessage_Chunk:
		g.onChunk(nodeID, b.Chunk)
	case *pb.AgentMessage_Health:
		_, err = g.DB.Exec(ctx, `UPDATE placements SET health = $3, updated_at = now()
			WHERE id = $1 AND node_id = $2 AND $3 IN ('unknown','starting','healthy','unhealthy')`,
			b.Health.GetPlacementId(), nodeID, b.Health.GetHealth())
	case *pb.AgentMessage_ContainerEvent:
		ev := b.ContainerEvent
		payload, _ := json.Marshal(map[string]any{"kind": ev.GetKind(), "placement_id": ev.GetPlacementId(), "exit_code": ev.GetExitCode()})
		_, err = g.DB.Exec(ctx,
			`INSERT INTO events (org_id, topic, object_id, payload)
			 SELECT n.org_id, 'service', p.service_id, $3 FROM placements p JOIN nodes n ON n.id = p.node_id
			  WHERE p.id = $1 AND p.node_id = $2`, ev.GetPlacementId(), nodeID, payload)
	case *pb.AgentMessage_CertRenew:
		err = g.onCertRenew(ctx, s, b.CertRenew)
	}
	if err != nil {
		g.Log.Warn("message failed", "node", nodeID, "err", err)
	}
}

func (g *Gateway) onHeartbeat(ctx context.Context, nodeID string, h *pb.Heartbeat) error {
	var before string
	err := g.DB.QueryRow(ctx,
		`WITH old AS (SELECT state FROM nodes WHERE id = $1)
		 UPDATE nodes SET last_heartbeat_at = $2, updated_at = $2,
		   acked_state_version = GREATEST(acked_state_version, $3),
		   state = CASE WHEN state IN ('suspect','down') THEN 'ready' ELSE state END
		 WHERE id = $1 RETURNING (SELECT state FROM old)`,
		nodeID, g.Clock.Now(), h.GetAppliedStateVersion()).Scan(&before)
	if err != nil {
		return err
	}
	if before == "suspect" || before == "down" {
		g.nodeEvent(ctx, nodeID, map[string]any{"kind": "state", "from": before, "to": "ready"})
	}
	return nil
}

// onActualState stores container health, domain TLS status and the acked version.
func (g *Gateway) onActualState(ctx context.Context, nodeID string, a *pb.ActualState) error {
	tx, err := g.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE nodes SET acked_state_version = GREATEST(acked_state_version, $2), updated_at = now() WHERE id = $1`,
		nodeID, a.GetAppliedStateVersion()); err != nil {
		return err
	}
	for _, c := range a.GetContainers() {
		_, err := tx.Exec(ctx,
			`UPDATE placements SET
			   container_id = COALESCE(NULLIF($3,''), container_id),
			   health = CASE
			     WHEN $4 IN ('unknown','starting','healthy','unhealthy') AND $4 <> '' THEN $4
			     WHEN $5 = 'exited' AND role <> 'stopped' THEN 'unhealthy'
			     ELSE health END,
			   updated_at = now()
			 WHERE id = $1 AND node_id = $2`,
			c.GetPlacementId(), nodeID, c.GetContainerId(), c.GetHealth(), c.GetState())
		if err != nil {
			return err
		}
	}
	for _, d := range a.GetDomains() {
		var exp *time.Time
		if d.GetExpiresUnixMs() > 0 {
			t := time.UnixMilli(d.GetExpiresUnixMs()).UTC()
			exp = &t
		}
		_, err := tx.Exec(ctx,
			`UPDATE domains SET tls_status = $2, tls_error = NULLIF($3,''), tls_expires_at = $4
			 WHERE host = $1 AND $2 IN ('pending','issued','failed')`,
			strings.ToLower(d.GetHost()), d.GetTlsStatus(), d.GetError(), exp)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// onCertRenew signs a new certificate. It always answers: the agent also asks early,
// and the answer is a fresh certificate in every case.
func (g *Gateway) onCertRenew(ctx context.Context, s *session, r *pb.CertRenewRequest) error {
	cert, err := g.CA.SignAgentCert(r.GetCsrPem(), s.nodeID, g.Config.AgentCertValidity)
	if err != nil {
		return err
	}
	if _, err := g.DB.Exec(ctx, `UPDATE nodes SET cert_serial = $2, cert_expires_at = $3, updated_at = now() WHERE id = $1`,
		s.nodeID, cert.Serial, cert.NotAfter); err != nil {
		return err
	}
	s.enqueue(&pb.ServerMessage{Body: &pb.ServerMessage_CertRenewed{CertRenewed: &pb.CertRenewResponse{ClientCertPem: cert.PEM}}})
	return nil
}
