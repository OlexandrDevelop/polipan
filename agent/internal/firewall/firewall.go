// SPDX-License-Identifier: Apache-2.0

// Package firewall writes the nftables rules of the node (table inet polipan).
package firewall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
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
	b.WriteString("  }\n}\n")
	return b.String()
}

// Firewall applies the ruleset with the nft binary.
type Firewall struct {
	Enabled   bool
	DNSAddr   string
	AgentPort int
	Log       *slog.Logger
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
	script := Ruleset(Params{DNSAddr: f.DNSAddr, AgentPort: f.AgentPort, TCPPorts: tcp, UDPPorts: udp})
	h := sha256.Sum256([]byte(script))
	f.mu.Lock()
	defer f.mu.Unlock()
	if h == f.lastHash {
		return nil
	}
	f.lastHash = h // also on failure, so a node without CAP_NET_ADMIN does not retry every pass
	return run(ctx, script)
}
