// SPDX-License-Identifier: Apache-2.0

package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polipan/polipan/proto/agentv1"
)

const golden = `table inet polipan
delete table inet polipan
table inet polipan {
  chain input {
    type filter hook input priority 0; policy drop;
    ct state established,related accept
    ct state invalid drop
    iifname "lo" accept
    ip protocol icmp accept
    meta l4proto ipv6-icmp accept
    tcp dport 22 accept
    tcp dport { 22, 80, 443, 8080, 9443 } accept
    udp dport 51820 accept
    ip daddr 172.30.0.1 udp dport 53 accept
    ip daddr 172.30.0.1 tcp dport 53 accept
    udp dport { 5353 } accept
  }
}
`

func TestRulesetGolden(t *testing.T) {
	got := Ruleset(Params{DNSAddr: "172.30.0.1", AgentPort: 9443, TCPPorts: []int{8080, 80}, UDPPorts: []int{5353}})
	if got != golden {
		t.Fatalf("ruleset differs:\n%s", got)
	}
}

func TestApplyOnlyWhenChanged(t *testing.T) {
	n := 0
	f := &Firewall{Enabled: true, Run: func(context.Context, string) error { n++; return nil }}
	ds := &agentv1.DesiredState{Workloads: []*agentv1.WorkloadSpec{{Role: "live", Ports: []*agentv1.PortSpec{{ContainerPort: 80, HostPort: 8080}}}}}
	_ = f.Apply(context.Background(), ds)
	_ = f.Apply(context.Background(), ds)
	if n != 1 {
		t.Fatalf("runs = %d", n)
	}
	ds.Workloads[0].Ports[0].HostPort = 8081
	_ = f.Apply(context.Background(), ds)
	if n != 2 {
		t.Fatalf("runs = %d", n)
	}
}

const goldenIsolation = `table inet polipan
delete table inet polipan
table inet polipan {
  set caddy {
    type ipv4_addr
    elements = { 10.210.1.2, 10.210.2.2 }
  }
  set env_0 {
    type ipv4_addr
    elements = { 10.210.1.10, 10.210.2.10 }
  }
  set env_1 {
    type ipv4_addr
    elements = { 10.210.1.11 }
  }
  chain input {
    type filter hook input priority 0; policy drop;
    ct state established,related accept
    ct state invalid drop
    iifname "lo" accept
    ip protocol icmp accept
    meta l4proto ipv6-icmp accept
    tcp dport 22 accept
    tcp dport { 22, 80, 443 } accept
    udp dport 51820 accept
  }
  chain forward {
    type filter hook forward priority -10; policy accept;
    ct state established,related accept
    ip saddr @caddy accept
    ip daddr @caddy accept
    ip saddr @env_0 ip daddr @env_0 accept
    ip saddr @env_1 ip daddr @env_1 accept
    ip saddr 10.210.0.0/16 ip daddr 10.210.0.0/16 drop
  }
}
`

func isolationState() *agentv1.DesiredState {
	return &agentv1.DesiredState{
		MeshCidr: "10.210.0.0/16",
		Peers:    []*agentv1.WireguardPeer{{NodeId: "nod_b", PublicKey: "k", AllowedIps: "10.210.2.0/24"}},
		Workloads: []*agentv1.WorkloadSpec{
			{PlacementId: "plc_1", EnvironmentId: "env_a", Ip: "10.210.1.10", Role: "live"},
			{PlacementId: "plc_2", EnvironmentId: "env_b", Ip: "10.210.1.11", Role: "live"},
			{PlacementId: "plc_3", EnvironmentId: "env_a", Ip: "10.210.1.12", Role: "stopped"}, // does not count
		},
		RemoteEndpoints: []*agentv1.Endpoint{{EnvironmentId: "env_a", Ip: "10.210.2.10", Names: []string{"pp-x"}}},
	}
}

// REQ-SEC-106: one set per environment, only same-environment pairs and Caddy are accepted, other mesh pairs are dropped.
func TestRulesetIsolationGolden(t *testing.T) {
	ds := isolationState()
	p := Params{MeshCIDR: ds.MeshCidr, Envs: EnvsFromDesired(ds), CaddyIPs: CaddyIPs("10.210.1.0/24", ds)}
	if got := Ruleset(p); got != goldenIsolation {
		t.Fatalf("ruleset differs:\n%s", got)
	}
}

func TestBridgeRuleset(t *testing.T) {
	ds := isolationState()
	got := BridgeRuleset(Params{MeshCIDR: ds.MeshCidr, Envs: EnvsFromDesired(ds), CaddyIPs: CaddyIPs("10.210.1.0/24", ds)})
	for _, want := range []string{"table bridge polipan_iso {", "type filter hook forward priority -200; policy accept;",
		"ip saddr @env_0 ip daddr @env_0 accept", "ip saddr 10.210.0.0/16 ip daddr 10.210.0.0/16 drop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if BridgeRuleset(Params{}) != "" {
		t.Fatal("no mesh, no bridge rules")
	}
}

// Both rule sets go out on a change, and the bridge failing does not fail the apply.
func TestApplyIsolation(t *testing.T) {
	var scripts []string
	f := &Firewall{Enabled: true, MeshSubnet: "10.210.1.0/24", Run: func(_ context.Context, s string) error {
		scripts = append(scripts, s)
		if strings.Contains(s, "table bridge") {
			return errors.New("no bridge family")
		}
		return nil
	}}
	if err := f.Apply(context.Background(), isolationState()); err != nil {
		t.Fatalf("a failing bridge table must not fail the apply: %v", err)
	}
	if len(scripts) != 2 || !strings.Contains(scripts[0], "table inet polipan") || !strings.Contains(scripts[1], "table bridge polipan_iso") {
		t.Fatalf("scripts: %v", scripts)
	}
	_ = f.Apply(context.Background(), isolationState())
	if len(scripts) != 2 {
		t.Fatal("an unchanged state must not run again")
	}
}

// A one-off container (pre_deploy) is let into its environment while it runs, and out again after.
func TestRetainOneOff(t *testing.T) {
	var scripts []string
	f := &Firewall{Enabled: true, MeshSubnet: "10.210.1.0/24", Run: func(_ context.Context, s string) error {
		scripts = append(scripts, s)
		return nil
	}}
	_ = f.Apply(context.Background(), isolationState())
	n := len(scripts)
	release := f.Retain(context.Background(), "env_a", "10.210.1.200")
	if len(scripts) != n+2 || !strings.Contains(scripts[len(scripts)-2], "10.210.1.200") {
		t.Fatalf("the address must be in the sets of env_a: %v", scripts[n:])
	}
	release()
	if len(scripts) != n+4 || strings.Contains(scripts[len(scripts)-2], "10.210.1.200") {
		t.Fatal("the address must be taken out again")
	}
}
