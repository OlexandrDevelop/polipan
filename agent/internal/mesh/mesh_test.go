// SPDX-License-Identifier: Apache-2.0

package mesh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polipan/polipan/proto/agentv1"
)

func peers() []*agentv1.WireguardPeer {
	return []*agentv1.WireguardPeer{
		{NodeId: "nod_b", PublicKey: "KEYB", Endpoint: "203.0.113.2:51820", AllowedIps: "10.210.2.0/24", KeepaliveSeconds: 25},
		{NodeId: "nod_a", PublicKey: "KEYA", AllowedIps: "10.210.1.0/24", KeepaliveSeconds: 25},
		{NodeId: "nod_x", PublicKey: "", AllowedIps: "10.210.9.0/24"}, // no key: skipped
	}
}

// REQ-AGENT-008: the config has every peer with its own subnet, the same input gives the same text.
func TestREQ_AGENT_008_BuildPlan(t *testing.T) {
	p := BuildPlan("PRIV", 0, peers())
	if !strings.Contains(p.Config, "ListenPort = 51820") || !strings.Contains(p.Config, "PrivateKey = PRIV") {
		t.Fatalf("interface section: %s", p.Config)
	}
	if strings.Index(p.Config, "KEYA") > strings.Index(p.Config, "KEYB") {
		t.Fatal("the peers must be sorted by node id")
	}
	if !strings.Contains(p.Config, "Endpoint = 203.0.113.2:51820") || strings.Count(p.Config, "Endpoint") != 1 {
		t.Fatalf("only the peer with an endpoint has one: %s", p.Config)
	}
	if strings.Contains(p.Config, "10.210.9.0") {
		t.Fatal("a peer without a key must be skipped")
	}
	if strings.Join(p.Routes, ",") != "10.210.1.0/24,10.210.2.0/24" {
		t.Fatalf("routes: %v", p.Routes)
	}
	again := BuildPlan("PRIV", 0, []*agentv1.WireguardPeer{peers()[1], peers()[0]})
	if again.Config != p.Config {
		t.Fatal("the order of the input must not change the text")
	}
}

// The key pair is made once and kept. The public key is derived with curve25519.
func TestREQ_AGENT_008_KeyPair(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := LoadOrCreateKey(dir)
	if k1 != k2 {
		t.Fatal("the key must stay")
	}
	fi, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("key file mode: %v %v", fi, err)
	}
	pub, err := PublicKey(k1)
	if err != nil || len(pub) != 44 {
		t.Fatalf("public key %q %v", pub, err)
	}
	// A known pair of the WireGuard demos (X25519 of the clamped key).
	got, err := PublicKey("yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=")
	if err != nil || got != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Fatalf("known pair: %q %v", got, err)
	}
}

type call struct{ stdin, cmd string }

func fake(calls *[]call, fail map[string]bool) Runner {
	return func(_ context.Context, stdin, name string, args ...string) (string, error) {
		c := name + " " + strings.Join(args, " ")
		*calls = append(*calls, call{stdin, c})
		for k := range fail {
			if strings.HasPrefix(c, k) {
				return "", os.ErrNotExist
			}
		}
		return "", nil
	}
}

func joined(calls []call) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.cmd + "\n")
	}
	return b.String()
}

// Peer sync: the interface is made, the config is loaded, the routes follow the peers. A peer that is gone loses its route.
func TestREQ_AGENT_008_ApplySyncsPeersAndRoutes(t *testing.T) {
	var calls []call
	m := &Manager{DataDir: t.TempDir(), MeshIP: "10.210.3.1", MeshCIDR: "10.210.0.0/16", Run: fake(&calls, map[string]bool{"ip link show": true, "iptables -t filter -C": true, "iptables -t nat -C": true})}
	if err := m.Apply(context.Background(), peers(), ""); err != nil {
		t.Fatal(err)
	}
	out := joined(calls)
	for _, want := range []string{"ip link add dev wg0 type wireguard", "wg syncconf wg0 /dev/stdin", "ip link set dev wg0 mtu 1380 up",
		"ip route replace 10.210.1.0/24 dev wg0 src 10.210.3.1", "ip route replace 10.210.2.0/24 dev wg0 src 10.210.3.1",
		"iptables -t filter -I DOCKER-USER 1 -i wg0 -j ACCEPT", "iptables -t nat -I POSTROUTING 1 -s 10.210.0.0/16 -d 10.210.0.0/16 -j ACCEPT"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// The key never goes on a command line, only on stdin.
	if strings.Contains(out, "PrivateKey") {
		t.Fatal("the private key is on a command line")
	}
	n := len(calls)
	if err := m.Apply(context.Background(), peers(), ""); err != nil || len(calls) != n {
		t.Fatalf("the same peers must do nothing: %v, %d new calls", err, len(calls)-n)
	}
	calls = nil
	if err := m.Apply(context.Background(), peers()[:2][1:], ""); err != nil { // only nod_a is left
		t.Fatal(err)
	}
	out = joined(calls)
	if !strings.Contains(out, "ip route del 10.210.2.0/24 dev wg0") || strings.Contains(out, "ip route del 10.210.1.0/24") {
		t.Fatalf("a peer that is gone loses its route, the others stay:\n%s", out)
	}
}

func TestApplyReportsMissingModule(t *testing.T) {
	var calls []call
	m := &Manager{DataDir: t.TempDir(), Run: fake(&calls, map[string]bool{"ip link": true})}
	if err := m.Apply(context.Background(), nil, ""); err == nil || !strings.Contains(err.Error(), "wireguard module") {
		t.Fatalf("got %v", err)
	}
}

// The self-check fails when the Docker network is not the mesh subnet of the node.
func TestCheckNetworkSubnet(t *testing.T) {
	var calls []call
	m := &Manager{DataDir: t.TempDir(), Run: fake(&calls, nil), MeshSubnet: "10.210.3.0/24", NetSubnet: "172.30.0.0/24"}
	if err := m.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "mesh subnet") {
		t.Fatalf("got %v", err)
	}
	m.NetSubnet = "10.210.3.0/24"
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
