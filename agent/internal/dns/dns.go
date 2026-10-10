// SPDX-License-Identifier: Apache-2.0

// Package dns is a small DNS server for the polipan network. It answers A queries for
// local workload names and forwards everything else to the host resolvers.
package dns

import (
	"bufio"
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Zone is the internal domain: <alias>.polipan.internal also resolves.
const Zone = ".polipan.internal"

// Server is the DNS server.
type Server struct {
	// Upstreams are host:port addresses. Empty = read from /etc/resolv.conf.
	Upstreams []string
	Timeout   time.Duration

	mu    sync.RWMutex
	names map[string]net.IP
	conn  net.PacketConn
}

// Set replaces the name to IP table.
func (s *Server) Set(table map[string]string) {
	m := make(map[string]net.IP, len(table))
	for n, ip := range table {
		if p := net.ParseIP(ip).To4(); p != nil {
			m[strings.ToLower(strings.TrimSuffix(n, "."))] = p
		}
	}
	s.mu.Lock()
	s.names = m
	s.mu.Unlock()
}

func (s *Server) lookup(name string) (net.IP, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ip, ok := s.names[name]; ok {
		return ip, true
	}
	if strings.HasSuffix(name, Zone) {
		ip, ok := s.names[strings.TrimSuffix(name, Zone)]
		return ip, ok
	}
	return nil, false
}

// Start listens on gateway:53 (UDP), or on 127.0.0.1:53 when that fails.
// It returns the address that is used. The server stops when ctx ends.
func (s *Server) Start(ctx context.Context, gateway string) (string, error) {
	var pc net.PacketConn
	var err error
	var addr string
	for _, a := range []string{net.JoinHostPort(gateway, "53"), "127.0.0.1:53"} {
		if gateway == "" && a != "127.0.0.1:53" {
			continue
		}
		if pc, err = net.ListenPacket("udp", a); err == nil {
			addr = a
			break
		}
	}
	if pc == nil {
		return "", err
	}
	s.Serve(ctx, pc)
	return addr, nil
}

// Serve answers queries on pc in the background.
func (s *Server) Serve(ctx context.Context, pc net.PacketConn) {
	s.conn = pc
	go func() { <-ctx.Done(); _ = pc.Close() }()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			go func() {
				if resp := s.handle(q); resp != nil {
					_, _ = pc.WriteTo(resp, from)
				}
			}()
		}
	}()
}

func (s *Server) handle(raw []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(raw)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	if q.Class == dnsmessage.ClassINET && (q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeAAAA) {
		if ip, ok := s.lookup(q.Name.String()); ok {
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired, RecursionAvailable: true},
				Questions: []dnsmessage.Question{q},
			}
			if q.Type == dnsmessage.TypeA {
				var a [4]byte
				copy(a[:], ip)
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 5},
					Body:   &dnsmessage.AResource{A: a},
				}}
			}
			out, err := resp.Pack()
			if err != nil {
				return nil
			}
			return out
		}
	}
	return s.forward(raw)
}

func (s *Server) upstreams() []string {
	if len(s.Upstreams) > 0 {
		return s.Upstreams
	}
	return HostResolvers()
}

func (s *Server) forward(raw []byte) []byte {
	to := s.Timeout
	if to == 0 {
		to = 2 * time.Second
	}
	for _, up := range s.upstreams() {
		c, err := net.DialTimeout("udp", up, to)
		if err != nil {
			continue
		}
		_ = c.SetDeadline(time.Now().Add(to))
		_, err = c.Write(raw)
		buf := make([]byte, 4096)
		var n int
		if err == nil {
			n, err = c.Read(buf)
		}
		_ = c.Close()
		if err == nil {
			return buf[:n]
		}
	}
	return nil
}

// HostResolvers reads nameservers from resolv.conf and skips loopback addresses (stub resolvers).
func HostResolvers() []string {
	for _, p := range []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"} {
		if r := parseResolv(p); len(r) > 0 {
			return r
		}
	}
	return []string{"1.1.1.1:53", "8.8.8.8:53"}
}

func parseResolv(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 2 && fs[0] == "nameserver" {
			ip := net.ParseIP(fs[1])
			if ip == nil || ip.IsLoopback() {
				continue
			}
			out = append(out, net.JoinHostPort(fs[1], "53"))
		}
	}
	return out
}
