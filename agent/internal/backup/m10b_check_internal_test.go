// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"errors"
	"testing"

	pb "github.com/polipan/polipan/proto/agentv1"

	"github.com/polipan/polipan/agent/internal/docker"
)

// specRecorder is a Docker that remembers the container spec of the restore check and then fails.
type specRecorder struct {
	Docker
	spec docker.ContainerSpec
}

func (s *specRecorder) List(context.Context, map[string]string) ([]docker.Container, error) {
	return nil, nil
}
func (s *specRecorder) VolumeCreate(context.Context, string, map[string]string) (*docker.Volume, error) {
	return &docker.Volume{}, nil
}
func (s *specRecorder) CreateAndStart(_ context.Context, sp docker.ContainerSpec) (string, error) {
	s.spec = sp
	return "", errors.New("stop here")
}
func (s *specRecorder) Remove(context.Context, string, bool) error { return nil }
func (s *specRecorder) VolumeRemove(context.Context, string) error { return nil }

// Threat model row 24 (REQ-BKP-104): the temporary container of a restore check has no network and no
// published ports, and it has memory and CPU limits.
func TestM10b_RestoreCheckContainerIsIsolated(t *testing.T) {
	rec := &specRecorder{}
	r := &Runner{Docker: rec}
	_, _ = r.startTemp(context.Background(), &pb.RunRestoreCheck{CheckId: "rck_1", ImageRef: "postgres@sha256:aa"}, "pp-check-1", nil, "/var/lib/postgresql/data", nil)
	if rec.spec.Network != "none" {
		t.Fatalf("network %q, want none", rec.spec.Network)
	}
	if len(rec.spec.Ports) != 0 {
		t.Fatalf("published ports: %v", rec.spec.Ports)
	}
	if rec.spec.MemoryBytes == 0 || rec.spec.NanoCPUs == 0 {
		t.Fatalf("no limits: %d %d", rec.spec.MemoryBytes, rec.spec.NanoCPUs)
	}
}
