// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"context"
	"testing"
	"time"

	pb "github.com/polipan/polipan/proto/agentv1"
	"github.com/polipan/polipan/server/internal/control"
	"github.com/polipan/polipan/server/internal/orchestrator"
)

// TestChaos_KillNodeWhileDeploying is the scenario "kill node while deploying" of docs/spec/13-testing-and-quality.md
// section 2. The node that holds the new containers goes down while the deployment waits for the health check. The
// deployment must end as failed with node_unavailable (it must not wait for the whole health timeout), the release
// that was live must stay the live release, and the control plane must keep answering.
func TestChaos_KillNodeWhileDeploying(t *testing.T) {
	const downAfter = 3 * time.Second
	m := setupM7With(t, func(d *control.Deps) { d.Config.NodeDownAfter = downAfter })
	a := m.addNode(t, "n-a")
	m.addNode(t, "n-b")
	m.addDomain(t)
	m.deploy(t, orchestrator.DeployParams{})
	live := m.q(t, `SELECT id FROM releases WHERE service_id = $1 ORDER BY number DESC LIMIT 1`, m.serviceID)

	// The health check of the next deployment never answers, like a node that is cut off.
	release := make(chan struct{})
	a.sim.set(func() {
		a.sim.check = func(*pb.CheckHealth) *pb.CommandAck {
			<-release
			return &pb.CommandAck{Ok: true}
		}
	})
	defer close(release)
	id, _, err := m.ctl.DeployWith(context.Background(), orchestrator.DeployParams{ServiceID: m.serviceID})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the deployment waits for the health check", func() bool {
		return m.q(t, `SELECT state FROM deployments WHERE id = $1`, id) == "health_checking"
	})
	start := time.Now()
	_ = a.a.Stream.CloseSend() // the connection of the node ends, like a killed machine
	waitFor(t, "the deployment ends", func() bool { return terminalDeployment[m.q(t, `SELECT state FROM deployments WHERE id = $1`, id)] })
	took := time.Since(start)
	if took > downAfter+5*time.Second {
		t.Fatalf("the deployment needed %s after the kill, node_down_after is %s", took, downAfter)
	}
	t.Logf("CHAOS node killed during health_checking: deployment ended as %s (%s) %s after the kill",
		m.q(t, `SELECT state FROM deployments WHERE id = $1`, id), m.q(t, `SELECT failure_reason FROM deployments WHERE id = $1`, id), took.Round(time.Millisecond))
	if got := m.q(t, `SELECT state || ':' || COALESCE(failure_reason, '') FROM deployments WHERE id = $1`, id); got != "failed:node_unavailable" {
		t.Fatalf("deployment ended as %s", got)
	}
	if got := m.q(t, `SELECT id FROM releases r WHERE service_id = $1 AND EXISTS (SELECT 1 FROM placements p WHERE p.release_id = r.id AND p.role = 'live') LIMIT 1`, m.serviceID); got != live {
		t.Fatalf("the live release changed: %s, want %s", got, live)
	}
	if m.q(t, `SELECT 1`) != "1" {
		t.Fatal("the database does not answer")
	}
}
