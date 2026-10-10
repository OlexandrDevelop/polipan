// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/git"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// R4: the node pull credential of the built-in registry is for built images of git services only.
// An image service that points to the registry (to pull the built images of others) fails.
func TestR4_ImageServiceCannotUseBuiltInRegistry(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	ctx := context.Background()
	if _, err := f.env.Pool.Exec(ctx, `UPDATE services SET source_type = 'image', config = $2 WHERE id = $1`,
		f.serviceID, `{"image":"`+pullHost+`/svc-other@sha256:abc","port":8080}`); err != nil {
		t.Fatal(err)
	}
	st, reason, detail := f.deploy(t, ctx, "manual")
	if st != "failed" || reason != control.ReasonInvalidConfig || !strings.Contains(detail, "built-in registry") {
		t.Fatalf("%s %s %s", st, reason, detail)
	}
	if len(f.pulls) != 0 {
		t.Fatalf("the node must not pull: %v", f.pulls)
	}
}

// R4 (carry-over of M3): the same rule holds for a rollback. A release of a git service has an image of the built-in
// registry. If the service became an image service, a rollback to that release must not pull it with the node credential.
func TestR4_RollbackOfImageServiceCannotUseBuiltInRegistry(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	ctx := context.Background()
	for _, commit := range []string{"aaaaaaa1111111", "bbbbbbb2222222"} {
		if st, reason, detail := f.deploy(t, orchestrator.WithBuildRequest(ctx, orchestrator.BuildRequest{Commit: commit}), "manual"); st != "live" {
			t.Fatalf("deploy: %s %s %s", st, reason, detail)
		}
	}
	var first string
	if err := f.env.Pool.QueryRow(ctx, `SELECT id FROM releases WHERE service_id = $1 AND number = 1`, f.serviceID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Pool.Exec(ctx, `UPDATE services SET source_type = 'image' WHERE id = $1`, f.serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Pool.Exec(ctx, `DELETE FROM placements WHERE release_id = $1`, first); err != nil { // no kept container: a normal rollback
		t.Fatal(err)
	}
	pulls := len(f.pulls)
	id, _, err := f.ctl.DeployWith(ctx, orchestrator.DeployParams{ServiceID: f.serviceID, RollbackReleaseID: first})
	if err != nil {
		t.Fatal(err)
	}
	ff := &fixture{env: f.env, ctl: f.ctl, serviceID: f.serviceID}
	if st, reason := ff.waitDeployment(t, id); st != "failed" || reason != control.ReasonInvalidConfig {
		t.Fatalf("rollback: %s %s", st, reason)
	}
	if len(f.pulls) != pulls {
		t.Fatalf("the node pulled the image: %v", f.pulls[pulls:])
	}
}

// R6: the SSH host key of a service is kept after the first clone; a later clone with another key fails.
func TestR6_HostKeyPinnedAfterFirstClone(t *testing.T) {
	f := gitSetup(t, "dockerfile")
	ctx := context.Background()
	ref, err := f.ctl.Build.Keys.Put(ctx, git.Scope{}, "deploy", "PRIVATE")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.env.Pool.Exec(ctx, `UPDATE services SET config = $2 WHERE id = $1`, f.serviceID,
		`{"port":8080,"strategy_options":{"drain_seconds":0},"git":{"repo":"git@git.example.com:acme/shop.git","branch":"main","builder":"dockerfile","dockerfile":"Dockerfile","deploy_key_ref":"`+ref+`"}}`); err != nil {
		t.Fatal(err)
	}
	f.cloner.HostKey = "ssh-ed25519 AAAAKEY1"
	if st, reason, detail := f.deploy(t, ctx, "manual"); st != "live" {
		t.Fatalf("first clone: %s %s %s", st, reason, detail)
	}
	var key string
	if err := f.env.Pool.QueryRow(ctx, `SELECT host_key FROM git_host_keys WHERE service_id = $1`, f.serviceID).Scan(&key); err != nil || key != "ssh-ed25519 AAAAKEY1" {
		t.Fatalf("stored key %q %v", key, err)
	}
	if st, reason, detail := f.deploy(t, ctx, "manual"); st != "live" {
		t.Fatalf("same key: %s %s %s", st, reason, detail)
	}
	if last := f.cloner.Calls[len(f.cloner.Calls)-1]; last.HostKey != "ssh-ed25519 AAAAKEY1" {
		t.Fatalf("the second clone was not pinned: %q", last.HostKey)
	}
	f.cloner.HostKey = "ssh-ed25519 AAAAKEY2"
	st, reason, detail := f.deploy(t, ctx, "manual")
	if st != "failed" || reason != control.ReasonBuildFailed || !strings.Contains(detail, "host key") {
		t.Fatalf("changed key: %s %s %s", st, reason, detail)
	}
}
