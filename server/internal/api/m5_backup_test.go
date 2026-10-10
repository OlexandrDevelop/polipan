// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"context"
	"strings"
	"testing"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/ssrf"
)

// M4 carry-over R12, R13, R16: private targets are refused, the test answer is fixed text, the SFTP host key is
// saved at the first test, and a target of a protected policy needs an admin to change.
func TestM5_BackupTargetCarryOver(t *testing.T) {
	e := newM4Env(t)
	c := e.owner()
	e.bk.SSRF = func(ctx context.Context, orgID string) ssrf.Policy {
		var allow bool
		_ = e.pool.QueryRow(ctx, `SELECT COALESCE((settings->>'allow_private_targets')::boolean, false) FROM orgs WHERE id = $1`, orgID).Scan(&allow)
		p, _ := ssrf.New(allow, "", ssrf.DefaultDockerNetworks)
		p.Resolver = publicResolver{}
		return p
	}
	n := 0
	name := func() string { n++; return "target " + string(rune('a'+n)) }
	s3 := func(endpoint string) map[string]any {
		return map[string]any{"name": name(), "kind": "s3", "config": map[string]any{"endpoint": endpoint, "bucket": "backups"},
			"credentials": map[string]string{"access_key_id": "AKIAEXAMPLE1", "secret_access_key": "sekret-value-123456"}}
	}
	sftp := func(host string) map[string]any {
		return map[string]any{"name": name(), "kind": "sftp", "config": map[string]any{"host": host, "user": "bk", "path": "/srv/restic"},
			"credentials": map[string]string{"private_key": "-----BEGIN KEY-----"}}
	}

	// R12: loopback, private, and the Docker bridge are refused for S3 endpoints and SFTP hosts.
	for _, body := range []map[string]any{s3("http://127.0.0.1:9000"), s3("http://10.1.2.3:9000"), s3("http://192.168.0.7"), s3("http://172.17.0.2:9000"),
		s3("http://localhost:9000"), sftp("127.0.0.1"), sftp("10.0.0.9"), sftp("localhost")} {
		rec := c.do("POST", "/api/v1/backup-targets", body)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "allow_private_targets") {
			t.Fatalf("%v: %d %s", body["config"], rec.Code, rec.Body)
		}
	}
	c.ok(201, "POST", "/api/v1/backup-targets", s3("https://s3.example.com"))
	hostTarget := c.ok(201, "POST", "/api/v1/backup-targets", sftp("backup.example.com"))
	// With allow_private_targets the private address is allowed.
	c.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"allow_private_targets": true}})
	c.ok(201, "POST", "/api/v1/backup-targets", s3("http://10.1.2.3:9000"))
	// An update is checked too.
	c.ok(200, "PATCH", "/api/v1/org", map[string]any{"settings": map[string]any{"allow_private_targets": false}})
	if rec := c.do("PATCH", "/api/v1/backup-targets/"+str(hostTarget, "id"), map[string]any{"config": map[string]any{"host": "10.0.0.9", "user": "bk", "path": "/srv/restic"}}); rec.Code != 422 {
		t.Fatalf("update to a private host: %d %s", rec.Code, rec.Body)
	}

	// R12: a failed test gives a fixed text. The first line of the raw error (an address, a path) stays in the log.
	e.onTest = func(*pb.TestBackupTarget) *pb.CommandAck {
		return &pb.CommandAck{ErrorCode: "target_test_failed", ErrorMessage: "dial tcp 10.9.8.7:22: connect: connection refused (/srv/secret-path)"}
	}
	res := c.ok(200, "POST", "/api/v1/backup-targets/"+str(hostTarget, "id")+"/test", nil)
	if res["ok"] != false || strings.Contains(str(res, "error"), "10.9.8.7") || strings.Contains(str(res, "error"), "secret-path") || str(res, "error") == "" {
		t.Fatalf("failed test answer: %v", res)
	}

	// R16: the first good test saves the host key that the node saw. A later test does not replace it.
	const key = "[backup.example.com]:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEKEY"
	e.onTest = func(*pb.TestBackupTarget) *pb.CommandAck {
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"known_host":"` + key + `"}`)}
	}
	c.ok(200, "POST", "/api/v1/backup-targets/"+str(hostTarget, "id")+"/test", nil)
	known := func() string {
		var kh string
		_ = e.pool.QueryRow(context.Background(), `SELECT coalesce(config->>'known_host', '') FROM backup_targets WHERE id = $1`, str(hostTarget, "id")).Scan(&kh)
		return kh
	}
	if known() != key || e.count(`SELECT count(*) FROM audit_events WHERE action = 'backup_target.host_key_pinned' AND target_id = $1`, str(hostTarget, "id")) != 1 {
		t.Fatalf("host key after the first test: %q", known())
	}
	e.onTest = func(*pb.TestBackupTarget) *pb.CommandAck {
		return &pb.CommandAck{Ok: true, ResultJson: []byte(`{"known_host":"other ssh-ed25519 BBBB"}`)}
	}
	c.ok(200, "POST", "/api/v1/backup-targets/"+str(hostTarget, "id")+"/test", nil)
	if known() != key {
		t.Fatalf("a later test replaced the pinned key: %q", known())
	}
	// The next jobs send it to the node, so ssh checks it strictly.
	e.onTest = func(m *pb.TestBackupTarget) *pb.CommandAck {
		if m.Target.SshKnownHost != key {
			return &pb.CommandAck{ErrorCode: "target_test_failed", ErrorMessage: "no pinned key"}
		}
		return &pb.CommandAck{Ok: true, ResultJson: []byte("{}")}
	}
	if res := c.ok(200, "POST", "/api/v1/backup-targets/"+str(hostTarget, "id")+"/test", nil); res["ok"] != true {
		t.Fatalf("the node did not get the pinned key: %v", res)
	}

	// R13: a target of a policy in a protected environment (production) needs an admin to change.
	_, envs := c.project("Shop")
	prodDB := e.dbService(c, envs["production"], "main")
	stagDB := e.dbService(c, envs["staging"], "main")
	mk := func(name string) string {
		return str(c.ok(201, "POST", "/api/v1/backup-targets", map[string]any{"name": name, "kind": "s3",
			"config":      map[string]any{"endpoint": "https://s3.example.com", "bucket": "backups"},
			"credentials": map[string]string{"access_key_id": "AKIAEXAMPLE1", "secret_access_key": "sekret-value-123456"}}), "id")
	}
	prodTarget, stagTarget := mk("for prod"), mk("for staging")
	e.policy(c, prodDB, map[string]any{"schedule": "0 2 * * *", "target_ids": []string{prodTarget}})
	e.policy(c, stagDB, map[string]any{"schedule": "0 2 * * *", "target_ids": []string{stagTarget}})
	dev := e.login("usr_d", "dev@example.com", "developer")
	rename := map[string]any{"name": "renamed"}
	if rec := dev.do("PATCH", "/api/v1/backup-targets/"+prodTarget, rename); rec.Code != 403 || !strings.Contains(rec.Body.String(), "protected_environment") {
		t.Fatalf("developer changes a protected target: %d %s", rec.Code, rec.Body)
	}
	dev.ok(200, "PATCH", "/api/v1/backup-targets/"+stagTarget, map[string]any{"name": "renamed staging"})
	c.ok(200, "PATCH", "/api/v1/backup-targets/"+prodTarget, rename)
}
