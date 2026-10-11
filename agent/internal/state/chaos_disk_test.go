// SPDX-License-Identifier: Apache-2.0

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/polipan/polipan/proto/agentv1"
)

// TestChaos_DiskFull is the "fill disk" scenario of docs/spec/13-testing-and-quality.md section 2. The agent keeps its
// state on a file system that is full: saving a new desired state must fail with an error, the last good state must stay
// readable, no temporary file may stay (it would keep the disk full), and saving works again when space is free.
// It runs only when POLIPAN_CHAOS_SMALLFS is a folder on a small file system, for example a tmpfs of 1 MB.
func TestChaos_DiskFull(t *testing.T) {
	base := os.Getenv("POLIPAN_CHAOS_SMALLFS")
	if base == "" {
		t.Skip("set POLIPAN_CHAOS_SMALLFS to a folder on a small file system to run the disk full scenario")
	}
	dir, err := os.MkdirTemp(base, "agent-state-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SaveDesired(sample(1)); !ok || err != nil {
		t.Fatalf("first save: %v %v", ok, err)
	}

	// Fill the file system until the kernel says there is no space.
	filler, err := os.Create(filepath.Join(dir, "filler"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 4096)
	for {
		if _, err := filler.Write(chunk); err != nil {
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("expected no space left, got %v", err)
			}
			break
		}
	}
	_ = filler.Close()

	big := sample(2)
	for i := 0; i < 200; i++ {
		big.Workloads = append(big.Workloads, &agentv1.WorkloadSpec{PlacementId: fmt.Sprintf("plc_%d", i+10), ContainerName: "pp", ImageRef: "nginx@sha256:bb"})
	}
	if ok, err := s.SaveDesired(big); ok || err == nil {
		t.Fatalf("a save on a full disk reported ok=%v err=%v", ok, err)
	}
	// The last good state is still there and still readable by a new process.
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	ds, err := s2.LoadDesired()
	if err != nil || ds == nil || ds.Version != 1 {
		t.Fatalf("the last good state is lost: %v %v", ds, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "desired.json.tmp")); err == nil {
		t.Fatal("a partial temporary file was left on the full disk")
	}

	// Space is free again: the next save works.
	if err := os.Remove(filepath.Join(dir, "filler")); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SaveDesired(big); !ok || err != nil {
		t.Fatalf("save after the disk was freed: %v %v", ok, err)
	}
}
