// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/api"
	"github.com/polipan/polipan/server/internal/auth"
	"github.com/polipan/polipan/server/internal/gateway/gatewaytest"
	"github.com/polipan/polipan/server/internal/store"
)

func execRequest(c *client, svc string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/services/"+svc+"/exec", nil)
	req.RemoteAddr = c.addr
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) login(id, email, role string) *client {
	e.t.Helper()
	hash, _ := auth.HashPassword(ownerPassword)
	var orgID string
	_ = e.pool.QueryRow(context.Background(), `SELECT o.id FROM orgs o JOIN memberships m ON m.org_id = o.id LIMIT 1`).Scan(&orgID)
	e.addUser(id, email, role, hash, orgID)
	c := e.newClient()
	c.takeSession(c.do("POST", "/api/v1/auth/login", map[string]any{"email": email, "password": ownerPassword}))
	return c
}

// AC-APP-07: a developer on a protected environment gets 403 for exec, before any upgrade.
// Admins may exec there, developers on other environments too.
func TestAC_APP_07_ExecProtectedEnvironment(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	owner := e.owner()
	_, envs := owner.project("Shop")
	prod := owner.service(envs["production"], "web")
	staging := owner.service(envs["staging"], "web")
	dev := e.login("usr_dev", "dev@example.com", "developer")
	admin := e.login("usr_adm", "admin@example.com", "admin")
	viewer := e.login("usr_view", "view@example.com", "viewer")

	rec := execRequest(dev, prod)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "protected_environment") {
		t.Fatalf("developer on production: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Upgrade") != "" {
		t.Fatal("the 403 must come before the upgrade")
	}
	if rec := execRequest(viewer, staging); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", rec.Code)
	}
	// Allowed users pass the permission check; the service has no running container yet.
	for name, c := range map[string]*client{"admin on production": admin, "owner on production": owner} {
		if rec := execRequest(c, prod); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_running") {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := execRequest(dev, staging); rec.Code != http.StatusConflict {
		t.Fatalf("developer on staging: %d %s", rec.Code, rec.Body)
	}
}

// AC-APP-07 (REQ-APP-010, REQ-SEC-103): the terminal relays browser <-> agent, writes audit
// events at start and end without terminal content, and closes after 15 minutes without input.
func TestAC_APP_07_ExecRelayAuditIdle(t *testing.T) {
	defer api.SetExecIdleCheck(10 * time.Millisecond)()
	ge := gatewaytest.Start(t)
	orch := &fakeOrch{pool: ge.Pool}
	h, err := api.NewRouter(api.Deps{Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: ge.Pool, Pool: ge.Pool,
		Clock: ge.Clock, RateLimit: 100000, Orchestrator: orch, SetupToken: "setup-secret", Exec: ge.GW})
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, h: h, pool: ge.Pool, orch: orch, clk: ge.Clock}
	owner := e.owner()
	_, envs := owner.project("Shop")
	svc := owner.service(envs["production"], "web")

	agent := ge.NewReadyNode(t, "n1")
	ctx := context.Background()
	var orgID string
	_ = ge.Pool.QueryRow(ctx, `SELECT org_id FROM projects p JOIN environments e ON e.project_id = p.id JOIN services s ON s.environment_id = e.id WHERE s.id = $1`, svc).Scan(&orgID)
	relID, plID := store.NewID("rel"), store.NewID("plc")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE nodes SET org_id = $1 WHERE id = $2`, []any{orgID, agent.NodeID}},
		{`INSERT INTO releases (id, service_id, number, image_ref, image_digest, config_snapshot, variable_snapshot, secret_versions, source)
		  VALUES ($1, $2, 1, 'nginx@sha256:aa', 'sha256:aa', '{}', '{}', '{}', '{}')`, []any{relID, svc}},
		{`INSERT INTO placements (id, service_id, node_id, release_id, replica_index, color, role) VALUES ($1, $2, $3, $4, 0, 'blue', 'live')`,
			[]any{plID, svc, agent.NodeID, relID}},
	} {
		if _, err := ge.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	dial := func() *websocket.Conn {
		t.Helper()
		cfg, err := websocket.NewConfig(strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/services/"+svc+"/exec?command=/bin/bash", srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Header.Set("Cookie", owner.cookie.Name+"="+owner.cookie.Value)
		ws, err := websocket.DialConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	nextCmd := func() *pb.Command {
		t.Helper()
		select {
		case c := <-agent.Commands:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("no command for the agent")
			return nil
		}
	}
	readWS := func(ws *websocket.Conn) (string, bool) {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		var data string
		var binary bool
		codec := websocket.Codec{Unmarshal: func(b []byte, pt byte, _ any) error {
			data, binary = string(b), pt == websocket.BinaryFrame
			return nil
		}}
		if err := codec.Receive(ws, nil); err != nil {
			t.Fatal(err)
		}
		return data, binary
	}
	audits := func(action string) []map[string]any {
		rows, err := ge.Pool.Query(ctx, `SELECT jsonb_build_object('action', action, 'diff', diff, 'actor_id', actor_id, 'target_id', target_id) FROM audit_events WHERE target_id = $1 AND action = $2`, svc, action)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var p map[string]any
			_ = rows.Scan(&p)
			out = append(out, p)
		}
		return out
	}

	// Session 1: output and input are relayed, then it is idle for 16 minutes.
	ws := dial()
	cmd := nextCmd()
	es := cmd.GetExecSession()
	if es == nil || es.PlacementId != plID || !es.Tty || strings.Join(es.Command, " ") != "/bin/bash" {
		t.Fatalf("exec session: %v", cmd)
	}
	agent.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_Chunk{Chunk: &pb.StreamChunk{CommandId: cmd.CommandId, Stream: "stdout", Data: []byte("hello")}}})
	if got, bin := readWS(ws); got != "hello" || !bin {
		t.Fatalf("output: %q binary=%v", got, bin)
	}
	if err := websocket.Message.Send(ws, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	if in := nextCmd().GetExecInput(); in == nil || string(in.Data) != "ls\n" || in.SessionCommandId != cmd.CommandId {
		t.Fatalf("input: %v", in)
	}
	if err := websocket.Message.Send(ws, `{"type":"resize","rows":40,"cols":120}`); err != nil {
		t.Fatal(err)
	}
	if in := nextCmd().GetExecInput(); in == nil || in.Rows != 40 || in.Cols != 120 {
		t.Fatalf("resize: %v", in)
	}
	start := audits("service.exec.start")
	if len(start) != 1 || start[0]["actor_id"] == "" || start[0]["target_id"] != svc {
		t.Fatalf("start audit: %v", start)
	}
	if d, _ := start[0]["diff"].(map[string]any); d["placement_id"] != plID || d["session_id"] != cmd.CommandId {
		t.Fatalf("start audit diff: %v", start[0])
	}
	ge.Clock.Advance(16 * time.Minute)
	got, bin := readWS(ws)
	var exit map[string]any
	if bin || json.Unmarshal([]byte(got), &exit) != nil || exit["type"] != "exit" || exit["reason"] != "idle_timeout" {
		t.Fatalf("idle end: %q", got)
	}
	if in := nextCmd().GetExecInput(); in == nil || !in.Eof {
		t.Fatalf("the agent must get the end of input: %v", in)
	}
	_ = ws.Close()

	// Session 2: the command ends on the agent; the exit code reaches the browser.
	ws = dial()
	cmd = nextCmd()
	agent.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_Chunk{Chunk: &pb.StreamChunk{CommandId: cmd.CommandId, Stream: "stdout", Eof: true}}})
	agent.Send(t, &pb.AgentMessage{Body: &pb.AgentMessage_Ack{Ack: &pb.CommandAck{CommandId: cmd.CommandId, Ok: true, ResultJson: []byte(`{"exit_code":7}`)}}})
	got, _ = readWS(ws)
	if !strings.Contains(got, `"code":7`) || !strings.Contains(got, `"reason":"ended"`) {
		t.Fatalf("exit: %q", got)
	}
	_ = ws.Close()

	var end []map[string]any
	for i := 0; i < 200 && len(end) < 2; i++ {
		time.Sleep(10 * time.Millisecond)
		end = audits("service.exec.end")
	}
	reasons := map[any]bool{}
	for _, a := range end {
		reasons[a["diff"].(map[string]any)["reason"]] = true
	}
	if len(end) != 2 || !reasons["idle_timeout"] || !reasons["ended"] {
		t.Fatalf("end audits: %v", end)
	}
	var all string
	_ = ge.Pool.QueryRow(ctx, `SELECT string_agg(diff::text, ' ') FROM audit_events`).Scan(&all)
	if strings.Contains(all, "hello") || strings.Contains(all, "ls\\n") {
		t.Fatalf("terminal content in the audit: %s", all)
	}
}
