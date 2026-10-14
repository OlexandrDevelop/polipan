// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/api"
)

// The panel and the updater of the host talk through a folder. The panel writes request and auto, the host
// writes status.json. These tests are the panel side.
func TestSystemUpdate(t *testing.T) {
	dir := t.TempDir()
	e := newTestEnv(t, "http://localhost")
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: e.pool, Clock: e.clk, RateLimit: 100000,
		Pool: e.pool, Orchestrator: e.orch, ExternalURL: "http://localhost", AgentListen: ":9443", SetupToken: "setup-secret",
		UpdaterDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	owner := e.owner()

	got := owner.ok(200, "GET", "/api/v1/system/update", nil)
	if got["available"] != true || got["state"] != "unknown" || got["auto_update"] != false || got["pending"] != false {
		t.Fatalf("first state: %v", got)
	}

	// The host reports a new version.
	status := `{"state":"available","message":"A new version is ready: v1.2.0.","current":"sha256:a","latest":"sha256:b","version":"v1.2.0","checked_at":"2026-10-14T03:30:00Z","updated_at":"2026-10-14T03:30:01Z"}`
	if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	got = owner.ok(200, "GET", "/api/v1/system/update", nil)
	if got["state"] != "available" || got["latest_version"] != "v1.2.0" || got["checked_at"] != "2026-10-14T03:30:00Z" {
		t.Fatalf("available state: %v", got)
	}

	// An update request is written whole, and a second one is refused until the host took the first.
	got = owner.ok(202, "POST", "/api/v1/system/update", map[string]any{"action": "apply"})
	if got["pending"] != true {
		t.Fatalf("pending: %v", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "request"))
	if err != nil || strings.TrimSpace(string(raw)) != "apply" {
		t.Fatalf("request file: %q %v", raw, err)
	}
	if rec := owner.do("POST", "/api/v1/system/update", map[string]any{"action": "check"}); rec.Code != 409 {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body)
	}
	_ = os.Remove(filepath.Join(dir, "request"))
	if _, err := os.Stat(filepath.Join(dir, "request.tmp")); err == nil {
		t.Fatal("a temporary file was left behind")
	}

	// The panel does not start a second update while the host updates.
	_ = os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"state":"updating"}`), 0o644)
	if rec := owner.do("POST", "/api/v1/system/update", map[string]any{"action": "apply"}); rec.Code != 409 {
		t.Fatalf("request while updating: %d %s", rec.Code, rec.Body)
	}

	// The nightly update is a switch.
	got = owner.ok(202, "POST", "/api/v1/system/update", map[string]any{"action": "auto_on"})
	if got["auto_update"] != true {
		t.Fatalf("auto on: %v", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "auto")); strings.TrimSpace(string(raw)) != "on" {
		t.Fatalf("auto file: %q", raw)
	}
	owner.ok(202, "POST", "/api/v1/system/update", map[string]any{"action": "auto_off"})
	if raw, _ := os.ReadFile(filepath.Join(dir, "auto")); strings.TrimSpace(string(raw)) != "off" {
		t.Fatalf("auto file: %q", raw)
	}

	// A bad action is refused, and the audit log has the requests.
	if rec := owner.do("POST", "/api/v1/system/update", map[string]any{"action": "format-disk"}); rec.Code != 422 && rec.Code != 400 {
		t.Fatalf("bad action: %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'system.update'`); n < 3 {
		t.Fatalf("audit events: %d", n)
	}

	// A developer sees the state, and cannot ask for an update.
	dev := e.userClient("usr_dev", "dev@example.com", "developer")
	dev.ok(200, "GET", "/api/v1/system/update", nil)
	if rec := dev.do("POST", "/api/v1/system/update", map[string]any{"action": "apply"}); rec.Code != 403 {
		t.Fatalf("developer: %d %s", rec.Code, rec.Body)
	}
}

// A panel that was not installed with the install script has no updater folder: it says so, and refuses requests.
func TestSystemUpdateWithoutUpdater(t *testing.T) {
	e := newTestEnv(t, "http://localhost")
	h, err := api.NewRouter(api.Deps{
		Log: slog.New(slog.NewJSONHandler(io.Discard, nil)), DB: e.pool, Clock: e.clk, RateLimit: 100000,
		Pool: e.pool, Orchestrator: e.orch, ExternalURL: "http://localhost", AgentListen: ":9443", SetupToken: "setup-secret",
		UpdaterDir: filepath.Join(t.TempDir(), "missing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.h = h
	owner := e.owner()
	got := owner.ok(200, "GET", "/api/v1/system/update", nil)
	if got["available"] != false {
		t.Fatalf("state: %v", got)
	}
	if rec := owner.do("POST", "/api/v1/system/update", map[string]any{"action": "apply"}); rec.Code != 409 {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
}
