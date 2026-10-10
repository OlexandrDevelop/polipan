// SPDX-License-Identifier: Apache-2.0

package gateway_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
)

// AC-NODE-02: a token works once. A used or expired token is refused.
func TestJoinTokenSingleUseAndExpiry(t *testing.T) {
	env := gatewaytest.Start(t)
	token := env.AddToken(t, "node-a", time.Hour)

	a, err := env.Join(t, token)
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	var state, subnet, ip string
	err = env.Pool.QueryRow(context.Background(),
		`SELECT state, mesh_subnet::text, host(mesh_ip) FROM nodes WHERE id = $1`, a.NodeID).Scan(&state, &subnet, &ip)
	if err != nil {
		t.Fatal(err)
	}
	if state != "pending" || subnet != "10.210.1.0/24" || ip != "10.210.1.1" {
		t.Fatalf("node row: %s %s %s", state, subnet, ip)
	}

	if _, err := env.Join(t, token); status.Code(err) != codes.PermissionDenied ||
		status.Convert(err).Message() != "join token is invalid, used or expired" {
		t.Fatalf("reused token: %v", err)
	}
	expired := env.AddToken(t, "node-b", -time.Minute)
	if _, err := env.Join(t, expired); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expired token: %v", err)
	}
	if _, err := env.Join(t, "pj_unknown"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unknown token: %v", err)
	}

	// The second node gets the next subnet.
	b, err := env.Join(t, env.AddToken(t, "node-c", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Pool.QueryRow(context.Background(), `SELECT mesh_subnet::text FROM nodes WHERE id = $1`, b.NodeID).Scan(&subnet); err != nil || subnet != "10.210.2.0/24" {
		t.Fatalf("second subnet %q %v", subnet, err)
	}
}

func TestConnectMTLS(t *testing.T) {
	env := gatewaytest.Start(t)
	a, err := env.Join(t, env.AddToken(t, "node-a", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a.Connect(t)
	if ds := a.WaitState(t); ds.Version != 1 {
		t.Fatalf("state version %d", ds.Version)
	}
	a.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_ActualState{ActualState: &pb.ActualState{AppliedStateVersion: 1}}})

	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		var acked int64
		if err := env.Pool.QueryRow(context.Background(), `SELECT state, acked_state_version FROM nodes WHERE id = $1`, a.NodeID).Scan(&state, &acked); err != nil {
			t.Fatal(err)
		}
		if state == "ready" && acked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node state %s acked %d", state, acked)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A command is routed to the ack.
	a.OnCommand = func(c *pb.Command) *pb.CommandAck { return &pb.CommandAck{Ok: true, ResultJson: []byte(`{}`)} }
	ack, err := env.GW.Send(context.Background(), a.NodeID, &pb.Command{Body: &pb.Command_PullImage{PullImage: &pb.PullImage{ImageRef: "x"}}})
	if err != nil || !ack.Ok {
		t.Fatalf("send: %v %v", ack, err)
	}
	if _, err := env.GW.Send(context.Background(), "nod_none", &pb.Command{}); err == nil {
		t.Fatal("send to an unknown node must fail")
	}

	// No client certificate: Connect is refused.
	c := pb.NewAgentGatewayClient(env.JoinConn(t))
	st, err := c.Connect(context.Background())
	if err == nil {
		_ = st.Send(&pb.AgentMessage{Body: &pb.AgentMessage_Hello{Hello: &pb.Hello{}}})
		_, err = st.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("connect without cert: %v", err)
	}
}
