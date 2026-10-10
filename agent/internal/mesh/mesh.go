// SPDX-License-Identifier: Apache-2.0

// Package mesh manages the WireGuard mesh of the node (09-multi-server, REQ-AGENT-008). Every node has one
// interface (wg0) and one route per other node: the /24 of that node goes through wg0. The server sends the
// peers in the desired state. The agent only makes the node match them.
package mesh

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/curve25519"

	"github.com/polipan/polipan/proto/agentv1"
)

// Interface is the name of the WireGuard interface.
const Interface = "wg0"

// KeyFile is the private key file in the data dir (mode 0400).
const KeyFile = "wg.key"

// DefaultPort is the WireGuard UDP port (glossary wireguard_port).
const DefaultPort = 51820

// MTU is below the usual 1500 so the WireGuard header fits.
const MTU = 1380

// LoadOrCreateKey returns the private key of the node as base64 text. A new key is made on the first call.
func LoadOrCreateKey(dir string) (string, error) {
	path := filepath.Join(dir, KeyFile)
	if b, err := os.ReadFile(path); err == nil {
		k := strings.TrimSpace(string(b))
		if _, err := decodeKey(k); err == nil {
			return k, nil
		}
	}
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", err
	}
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	k := base64.StdEncoding.EncodeToString(priv[:])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, []byte(k+"\n"), 0o400); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return k, nil
}

func decodeKey(k string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(k))
	if err != nil || len(b) != 32 {
		return nil, errors.New("mesh: a WireGuard key is 32 bytes in base64")
	}
	return b, nil
}

// PublicKey derives the public key from the private key (both base64).
func PublicKey(private string) (string, error) {
	b, err := decodeKey(private)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(b, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Plan is what the node must look like for a list of peers. It has no side effects, so tests can check it.
type Plan struct {
	// Config is the text for "wg syncconf" (wg-quick format without the address lines).
	Config string
	// Routes are the subnets that go through the interface, sorted.
	Routes []string
}

// BuildPlan makes the plan. A peer without a public key or without an allowed subnet is skipped.
// The peers are sorted by node id, so the same input gives the same text and the same hash.
func BuildPlan(private string, port int, peers []*agentv1.WireguardPeer) Plan {
	if port == 0 {
		port = DefaultPort
	}
	list := make([]*agentv1.WireguardPeer, 0, len(peers))
	for _, p := range peers {
		if p.GetPublicKey() != "" && p.GetAllowedIps() != "" {
			list = append(list, p)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].NodeId < list[j].NodeId })
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", private)
	fmt.Fprintf(&b, "ListenPort = %d\n", port)
	var routes []string
	for _, p := range list {
		b.WriteString("\n[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		fmt.Fprintf(&b, "AllowedIPs = %s\n", p.AllowedIps)
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.KeepaliveSeconds > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.KeepaliveSeconds)
		}
		routes = append(routes, strings.Split(p.AllowedIps, ",")...)
	}
	for i := range routes {
		routes[i] = strings.TrimSpace(routes[i])
	}
	sort.Strings(routes)
	return Plan{Config: b.String(), Routes: routes}
}

// Runner runs a command with an optional stdin text. Tests replace it.
type Runner func(ctx context.Context, stdin string, name string, args ...string) (string, error)

func execRunner(ctx context.Context, stdin string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// Manager keeps the WireGuard interface, the routes and the forwarding rules of the node.
type Manager struct {
	// DataDir holds the private key.
	DataDir string
	// Port is the UDP port, default DefaultPort.
	Port int
	// MeshIP is the address of the node in its subnet (for the source of the routes). Optional.
	MeshIP string
	// MeshCIDR is the pool of all subnets. It is used for the NAT exemption.
	MeshCIDR string
	// MeshSubnet is the subnet of this node, NetSubnet the subnet of its Docker network. They must be equal.
	MeshSubnet, NetSubnet string
	// Run replaces command execution (tests). Nil = the real commands.
	Run Runner
	Log *slog.Logger

	mu       sync.Mutex
	lastHash [32]byte
	routes   []string
	up       bool
}

func (m *Manager) run() Runner {
	if m.Run != nil {
		return m.Run
	}
	return execRunner
}

func (m *Manager) port() int {
	if m.Port > 0 {
		return m.Port
	}
	return DefaultPort
}

// Apply makes the node match the peers. It does nothing when the peers did not change since the last success.
// A node without peers still gets the interface, so the self-check passes and the first peer joins at once.
func (m *Manager) Apply(ctx context.Context, peers []*agentv1.WireguardPeer, meshCIDR string) error {
	key, err := LoadOrCreateKey(m.DataDir)
	if err != nil {
		return err
	}
	plan := BuildPlan(key, m.port(), peers)
	m.mu.Lock()
	defer m.mu.Unlock()
	if meshCIDR != "" {
		m.MeshCIDR = meshCIDR
	}
	h := sha256.Sum256([]byte(plan.Config + "|" + m.MeshIP + "|" + m.MeshCIDR))
	if m.up && h == m.lastHash {
		return nil
	}
	run := m.run()
	if _, err := run(ctx, "", "ip", "link", "show", Interface); err != nil {
		if _, err := run(ctx, "", "ip", "link", "add", "dev", Interface, "type", "wireguard"); err != nil {
			return fmt.Errorf("mesh: no WireGuard interface (is the wireguard module loaded?): %w", err)
		}
	}
	if _, err := run(ctx, plan.Config, "wg", "syncconf", Interface, "/dev/stdin"); err != nil {
		return err
	}
	if _, err := run(ctx, "", "ip", "link", "set", "dev", Interface, "mtu", fmt.Sprint(MTU), "up"); err != nil {
		return err
	}
	if err := m.syncRoutes(ctx, run, plan.Routes); err != nil {
		return err
	}
	if err := m.forwarding(ctx, run); err != nil {
		m.logf("forwarding rules are not complete", err)
	}
	m.lastHash, m.up = h, true
	return nil
}

func (m *Manager) logf(msg string, err error) {
	if m.Log != nil {
		m.Log.Warn(msg, "error", err)
	}
}

// syncRoutes adds the route of every peer subnet and removes the routes of peers that are gone.
func (m *Manager) syncRoutes(ctx context.Context, run Runner, want []string) error {
	have := map[string]bool{}
	for _, r := range m.routes {
		have[r] = true
	}
	var errs []error
	for _, r := range want {
		args := []string{"route", "replace", r, "dev", Interface}
		if m.MeshIP != "" {
			if _, err := run(ctx, "", "ip", append(args, "src", m.MeshIP)...); err == nil {
				delete(have, r)
				continue
			}
		}
		if _, err := run(ctx, "", "ip", args...); err != nil {
			errs = append(errs, err)
		}
		delete(have, r)
	}
	for r := range have { // peers that are gone
		_, _ = run(ctx, "", "ip", "route", "del", r, "dev", Interface)
	}
	m.routes = want
	return errors.Join(errs...)
}

// forwarding makes the node route between its Docker network and the mesh: IP forwarding on, Docker's own rules
// out of the way for mesh traffic (DOCKER-USER), and no NAT for traffic between two mesh addresses.
func (m *Manager) forwarding(ctx context.Context, run Runner) error {
	var errs []error
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil && m.Run == nil {
		errs = append(errs, err)
	}
	rules := [][]string{
		{"-t", "filter", "DOCKER-USER", "-i", Interface, "-j", "ACCEPT"},
		{"-t", "filter", "DOCKER-USER", "-o", Interface, "-j", "ACCEPT"},
	}
	if m.MeshCIDR != "" {
		rules = append(rules, []string{"-t", "nat", "POSTROUTING", "-s", m.MeshCIDR, "-d", m.MeshCIDR, "-j", "ACCEPT"})
	}
	ipt := m.iptablesBinary(ctx, run)
	for _, r := range rules {
		table, chain, rest := r[1], r[2], r[3:]
		check := append([]string{"-t", table, "-C", chain}, rest...)
		if _, err := run(ctx, "", ipt, check...); err == nil {
			continue
		}
		add := append([]string{"-t", table, "-I", chain, "1"}, rest...)
		if _, err := run(ctx, "", ipt, add...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// iptablesBinary picks the iptables program that Docker uses: the one that has the DOCKER-USER chain. A host can
// have the legacy and the nft backend side by side, and rules in the wrong one have no effect.
func (m *Manager) iptablesBinary(ctx context.Context, run Runner) string {
	for _, bin := range []string{"iptables", "iptables-nft", "iptables-legacy"} {
		if _, err := run(ctx, "", bin, "-t", "filter", "-S", "DOCKER-USER"); err == nil {
			return bin
		}
	}
	return "iptables"
}

// Check is the WireGuard part of the self-check: the interface exists and the Docker network of the node is the
// mesh subnet of the node (else the containers cannot be reached from other nodes).
func (m *Manager) Check(ctx context.Context) error {
	if _, err := m.run()(ctx, "", "wg", "show", Interface); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.NetSubnet != "" && m.MeshSubnet != "" && m.NetSubnet != m.MeshSubnet {
		return fmt.Errorf("the Docker network has the subnet %s, the mesh subnet of the node is %s: make the network again", m.NetSubnet, m.MeshSubnet)
	}
	return nil
}
