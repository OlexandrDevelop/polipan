// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// R16: the host key that ssh saves in the first connection is given back, as one line.
func TestFirstHostKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(f, []byte("# comment\n\n[host]:22 ssh-ed25519 AAAAKEY\nsecond ssh-rsa BBBB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (&repo{learned: f}).firstHostKey(); got != "[host]:22 ssh-ed25519 AAAAKEY" {
		t.Fatalf("got %q", got)
	}
	// A pinned key (no learned file) and an empty file give nothing.
	if got := (&repo{}).firstHostKey(); got != "" {
		t.Fatalf("pinned: %q", got)
	}
	empty := filepath.Join(t.TempDir(), "k")
	_ = os.WriteFile(empty, nil, 0o600)
	if got := (&repo{learned: empty}).firstHostKey(); got != "" {
		t.Fatalf("empty: %q", got)
	}
}
