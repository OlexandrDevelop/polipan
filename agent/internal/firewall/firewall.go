// SPDX-License-Identifier: Apache-2.0

// Package firewall writes the nftables rules of the node (table inet polipan).
package firewall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"

	"github.com/polipan/polipan/proto/agentv1"
)

// Params are the inputs of the ruleset.
type Params struct {
	DNSAddr   string // address of the agent DNS on the polipan bridge, empty = no rule
	WGPort    int    // WireGuard UDP port, default 51820
	AgentPort int    // server gateway port, always open so agents can reach a control plane on this host
	TCPPorts  []int  // published host tcp ports
	UDPPorts  []int  // published host udp ports

	// Environment isolation (REQ-SEC-106). Without MeshCIDR there are no such rules.
	MeshCIDR string              // the pool of all mesh subnets: container addresses are inside it
	Envs     map[string][]string // environment id -> the addresses of its workloads, on this node and on other nodes
	CaddyIPs []string            // the Caddy containers of all ingress nodes: they may reach every container
}

// PortsFromDesired returns the published host ports of the workloads.
func PortsFromDesired(ds *agentv1.DesiredState) (tcp, udp []int) {
	if ds == nil {
		return
	}
	for _, w := range ds.Workloads {
		if w.Role == "stopped" {
			continue
		}
		for _, p := range w.Ports {
			if p.HostPort == 0 {
				continue
			}
			if p.Protocol == "udp" {
				udp = append(udp, int(p.HostPort))
			} else {
				tcp = append(tcp, int(p.HostPort))
			}
		}
	}
	return
}

func portSet(ports []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range ports {
		if p > 0 && p < 65536 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

func list(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ", ")
}

// Ruleset builds the nft script. TCP port 22 is always accepted.
func Ruleset(p Params) string {
	if p.WGPort == 0 {
		p.WGPort = 51820
	}
	tcp := []int{22, 80, 443}
	if p.AgentPort > 0 {
		tcp = append(tcp, p.AgentPort)
	}
	tcp = portSet(append(tcp, p.TCPPorts...))
	var b strings.Builder
	b.WriteString("table inet polipan\n")
	b.WriteString("delete table inet polipan\n")
	b.WriteString("table inet polipan {\n")
	isolation := p.MeshCIDR != ""
	var envNames []string
	if isolation {
		envNames = writeSets(&b, p)
	}
	b.WriteString("  chain input {\n")
	b.WriteString("    type filter hook input priority 0; policy drop;\n")
	b.WriteString("    ct state established,related accept\n")
	b.WriteString("    ct state invalid drop\n")
	b.WriteString("    iifname \"lo\" accept\n")
	b.WriteString("    ip protocol icmp accept\n")
	b.WriteString("    meta l4proto ipv6-icmp accept\n")
	b.WriteString("    tcp dport 22 accept\n")
	fmt.Fprintf(&b, "    tcp dport { %s } accept\n", list(tcp))
	fmt.Fprintf(&b, "    udp dport %d accept\n", p.WGPort)
	if p.DNSAddr != "" {
		fmt.Fprintf(&b, "    ip daddr %s udp dport 53 accept\n", p.DNSAddr)
		fmt.Fprintf(&b, "    ip daddr %s tcp dport 53 accept\n", p.DNSAddr)
	}
	if udp := portSet(p.UDPPorts); len(udp) > 0 {
		fmt.Fprintf(&b, "    udp dport { %s } accept\n", list(udp))
	}
	b.WriteString("  }\n")
	if isolation {
		// Mesh traffic that is routed (from another node, or between networks): a container may talk only to
		// the containers of its own environment, and Caddy may talk to all (REQ-SEC-106).
		b.WriteString("  chain forward {\n")
		b.WriteString("    type filter hook forward priority -10; policy accept;\n")
		b.WriteString("    ct state established,related accept\n")
		writeIsolation(&b, p, envNames)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// envSetName is the name of the nft set of the n-th environment.
func envSetName(n int) string { return fmt.Sprintf("env_%d", n) }

func sortedEnvs(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if len(v) > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func addrSet(name string, ips []string) string {
	seen := map[string]bool{}
	var list []string
	for _, ip := range ips {
		if ip != "" && !seen[ip] {
			seen[ip] = true
			list = append(list, ip)
		}
	}
	sort.Strings(list)
	var b strings.Builder
	fmt.Fprintf(&b, "  set %s {\n    type ipv4_addr\n", name)
	if len(list) > 0 {
		fmt.Fprintf(&b, "    elements = { %s }\n", strings.Join(list, ", "))
	}
	b.WriteString("  }\n")
	return b.String()
}

// writeSets writes the sets of the Caddy addresses and of every environment. It returns the set names of the
// environments, in the order of the sorted environment ids.
func writeSets(b *strings.Builder, p Params) []string {
	b.WriteString(addrSet("caddy", p.CaddyIPs))
	var names []string
	for i, env := range sortedEnvs(p.Envs) {
		name := envSetName(i)
		names = append(names, name)
		b.WriteString(addrSet(name, p.Envs[env]))
	}
	return names
}

func writeIsolation(b *strings.Builder, p Params, envNames []string) {
	b.WriteString("    ip saddr @caddy accept\n")
	b.WriteString("    ip daddr @caddy accept\n")
	for _, n := range envNames {
		fmt.Fprintf(b, "    ip saddr @%s ip daddr @%s accept\n", n, n)
	}
	fmt.Fprintf(b, "    ip saddr %s ip daddr %s drop\n", p.MeshCIDR, p.MeshCIDR)
}

// BridgeRuleset isolates the environments inside one node: containers of two environments share the Docker bridge,
// and their traffic never reaches the routing hooks. The bridge family sees it. Empty without MeshCIDR.
func BridgeRuleset(p Params) string {
	if p.MeshCIDR == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("table bridge polipan_iso\n")
	b.WriteString("delete table bridge polipan_iso\n")
	b.WriteString("table bridge polipan_iso {\n")
	names := writeSets(&b, p)
	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority -200; policy accept;\n")
	writeIsolation(&b, p, names)
	b.WriteString("  }\n}\n")
	return b.String()
}

// Firewall applies the ruleset with the nft binary.
type Firewall struct {
	Enabled   bool
	DNSAddr   string
	AgentPort int
	// MeshSubnet is the /24 of this node. Its .2 address is the Caddy container of the node.
	MeshSubnet string
	Log        *slog.Logger
	// Run executes nft with the script on stdin. Nil = the real nft binary.
	Run func(ctx context.Context, script string) error

	mu       sync.Mutex
	lastHash [32]byte
	warned   bool
}

func runNft(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nft: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// Apply writes the rules if they changed. Problems are returned, never fatal.
func (f *Firewall) Apply(ctx context.Context, ds *agentv1.DesiredState) error {
	if f == nil || !f.Enabled {
		return nil
	}
	run := f.Run
	if run == nil {
		if _, err := exec.LookPath("nft"); err != nil {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.warned && f.Log != nil {
				f.Log.Warn("nft not found, firewall rules are not applied")
			}
			f.warned = true
			return nil
		}
		run = runNft
	}
	tcp, udp := PortsFromDesired(ds)
	p := Params{DNSAddr: f.DNSAddr, AgentPort: f.AgentPort, TCPPorts: tcp, UDPPorts: udp}
	if ds != nil && ds.MeshCidr != "" && f.MeshSubnet != "" {
		p.MeshCIDR, p.Envs, p.CaddyIPs = ds.MeshCidr, EnvsFromDesired(ds), CaddyIPs(f.MeshSubnet, ds)
	}
	script, bridge := Ruleset(p), BridgeRuleset(p)
	h := sha256.Sum256([]byte(script + "\x00" + bridge))
	f.mu.Lock()
	defer f.mu.Unlock()
	if h == f.lastHash {
		return nil
	}
	f.lastHash = h // also on failure, so a node without CAP_NET_ADMIN does not retry every pass
	err := run(ctx, script)
	if bridge != "" {
		// The bridge family may be missing in the kernel. The other rules are in place then, so only warn.
		if berr := run(ctx, bridge); berr != nil && f.Log != nil {
			f.Log.Warn("environment isolation inside the node is not applied", "error", berr)
		}
	}
	return err
}

// EnvsFromDesired maps every environment to the addresses of its workloads: the ones on this node and the ones the
// server lists as remote endpoints. A stopped workload does not count.
func EnvsFromDesired(ds *agentv1.DesiredState) map[string][]string {
	out := map[string][]string{}
	for _, w := range ds.GetWorkloads() {
		if w.Ip != "" && w.EnvironmentId != "" && w.Role != "stopped" {
			out[w.EnvironmentId] = append(out[w.EnvironmentId], w.Ip)
		}
	}
	for _, e := range ds.GetRemoteEndpoints() {
		if e.Ip != "" && e.EnvironmentId != "" {
			out[e.EnvironmentId] = append(out[e.EnvironmentId], e.Ip)
		}
	}
	return out
}

// CaddyIPs lists the Caddy address (.2) of this node and of every peer node.
func CaddyIPs(meshSubnet string, ds *agentv1.DesiredState) []string {
	var out []string
	add := func(cidr string) {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && n.IP.To4() != nil {
			ip := make(net.IP, 4)
			copy(ip, n.IP.To4())
			ip[3] = 2
			out = append(out, ip.String())
		}
	}
	add(meshSubnet)
	for _, peer := range ds.GetPeers() {
		for _, a := range strings.Split(peer.AllowedIps, ",") {
			add(a)
		}
	}
	return out
}
