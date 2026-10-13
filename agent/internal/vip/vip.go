// SPDX-License-Identifier: Apache-2.0

// Package vip gives every service a stable virtual address. The internal DNS name of a service answers with that
// address and never changes. The node maps it to the live containers with nftables DNAT. A deploy moves the map in
// one atomic nft transaction, so a client that cached the address (nginx, a connection pool) keeps working.
package vip

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// DefaultCIDR is the pool of the virtual addresses (RFC 2544 benchmark range, never routed on the internet).
// Addresses of the pool are used only on the node that resolved them: the DNAT happens before routing.
const DefaultCIDR = "198.18.0.0/15"

// Service is one service with the addresses of its live containers.
type Service struct {
	ID       string
	Backends []string
}

// Allocate gives a stable address to every id. The address comes from a hash of the id. A clash moves to the next
// free address, in the order of the sorted ids.
func Allocate(cidr *net.IPNet, ids []string) map[string]string {
	out := map[string]string{}
	ip4 := cidr.IP.To4()
	if ip4 == nil {
		return out
	}
	ones, bits := cidr.Mask.Size()
	size := uint64(1) << uint(bits-ones)
	if size < 4 {
		return out
	}
	usable := size - 2 // not the network and not the broadcast address
	base := uint64(binary.BigEndian.Uint32(ip4))
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	used := map[uint64]bool{}
	for _, id := range sorted {
		if _, dup := out[id]; dup || uint64(len(used)) >= usable {
			continue
		}
		h := sha256.Sum256([]byte(id))
		n := uint64(binary.BigEndian.Uint32(h[:4])) % usable
		for used[n] {
			n = (n + 1) % usable
		}
		used[n] = true
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], uint32(base+1+n))
		out[id] = net.IP(a[:]).String()
	}
	return out
}

// Ruleset builds the nft script of the nat table. localSubnet is the subnet of the Docker network of this node.
func Ruleset(vips map[string]string, services []Service, localSubnet string) string {
	var rules []string
	sorted := append([]Service(nil), services...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, s := range sorted {
		v := vips[s.ID]
		backends := dedupe(s.Backends)
		if v == "" || len(backends) == 0 {
			continue
		}
		if len(backends) == 1 {
			rules = append(rules, fmt.Sprintf("ip daddr %s dnat to %s", v, backends[0]))
			continue
		}
		elems := make([]string, len(backends))
		for i, b := range backends {
			elems[i] = fmt.Sprintf("%d : %s", i, b)
		}
		rules = append(rules, fmt.Sprintf("ip daddr %s dnat to numgen random mod %d map { %s }", v, len(backends), strings.Join(elems, ", ")))
	}
	var b strings.Builder
	b.WriteString("table ip polipan_nat\n")
	b.WriteString("delete table ip polipan_nat\n")
	if len(rules) == 0 {
		return b.String()
	}
	b.WriteString("table ip polipan_nat {\n")
	for _, hook := range []string{"prerouting", "output"} {
		fmt.Fprintf(&b, "  chain %s {\n    type nat hook %s priority dstnat; policy accept;\n", hook, hook)
		for _, r := range rules {
			fmt.Fprintf(&b, "    %s\n", r)
		}
		b.WriteString("  }\n")
	}
	// A client and its backend on the same bridge: the answer would go straight back to the client, not through
	// this node, and the client would not know it. The source becomes the bridge address for these packets.
	b.WriteString("  chain postrouting {\n    type nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&b, "    ip saddr %s ip daddr %s ct status dnat masquerade\n", localSubnet, localSubnet)
	b.WriteString("  }\n}\n")
	return b.String()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Manager applies the virtual addresses with the nft binary.
type Manager struct {
	Enabled bool
	// CIDR is the pool of the addresses. Empty = DefaultCIDR.
	CIDR string
	Log  *slog.Logger
	// Run executes nft with the script on stdin. Nil = the real nft binary.
	Run func(ctx context.Context, script string) error

	mu       sync.Mutex
	lastHash [32]byte
	lastVIPs map[string]string
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

// Apply maps the services to stable addresses and writes the rules if they changed. It returns the address of
// every service. On an error the caller must answer DNS with the real container addresses.
func (m *Manager) Apply(ctx context.Context, services []Service, localSubnet string) (map[string]string, error) {
	if m == nil || !m.Enabled {
		return nil, nil
	}
	if localSubnet == "" {
		return nil, fmt.Errorf("vip: the subnet of the network is not known")
	}
	cidr := m.CIDR
	if cidr == "" {
		cidr = DefaultCIDR
	}
	_, pool, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("vip: bad pool %q: %w", cidr, err)
	}
	ids := make([]string, len(services))
	for i, s := range services {
		ids[i] = s.ID
	}
	vips := Allocate(pool, ids)
	script := Ruleset(vips, services, localSubnet)
	h := sha256.Sum256([]byte(script))

	m.mu.Lock()
	defer m.mu.Unlock()
	if h == m.lastHash && m.lastVIPs != nil {
		return m.lastVIPs, nil
	}
	run := m.Run
	if run == nil {
		if _, err := exec.LookPath("nft"); err != nil {
			return nil, fmt.Errorf("vip: nft not found")
		}
		run = runNft
	}
	if err := run(ctx, script); err != nil {
		m.lastVIPs = nil // a failed write is tried again on the next pass
		return nil, err
	}
	m.lastHash, m.lastVIPs = h, vips
	return vips, nil
}
