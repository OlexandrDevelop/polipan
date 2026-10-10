// SPDX-License-Identifier: Apache-2.0

package build_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/polipan/polipan/server/internal/build"
)

// A symlink in the repo must not move the build context out of the clone.
func TestResolveInsideRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(root, "app")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok", filepath.Join(root, "inner")); err != nil {
		t.Fatal(err)
	}
	if _, err := build.ResolveInside(root, "/app"); err == nil {
		t.Fatal("symlink to / was accepted")
	}
	if _, err := build.ResolveInside(root, "/inner"); err != nil {
		t.Fatalf("inside symlink refused: %v", err)
	}
	if _, err := build.ResolveInside(root, ""); err != nil {
		t.Fatalf("root refused: %v", err)
	}
}
