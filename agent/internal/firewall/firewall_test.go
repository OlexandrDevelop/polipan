// SPDX-License-Identifier: Apache-2.0

package firewall

import (
	"context"
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
