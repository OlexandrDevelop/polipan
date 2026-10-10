// SPDX-License-Identifier: Apache-2.0

// Package ssrf is the deny list for outbound requests to user-supplied URLs (REQ-SEC-104).
//
// Always denied: loopback, link-local (169.254.0.0/16, fe80::/10), unspecified, multicast,
// the mesh CIDR and the Docker networks of Polipan. Private ranges (RFC 1918, fc00::/7,
// 100.64.0.0/10) are denied unless the org setting allow_private_targets is on.
// The check runs on the IP addresses after DNS resolution, at connect time and on every redirect.
package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrDenied is returned when a target address is on the deny list.
var ErrDenied = errors.New("ssrf: the target address is not allowed")

// DefaultDockerNetworks are the Docker networks of a Polipan host: the default bridge, the
// app network polipan (172.30.0.0/24), the control-plane network and the build network.
var DefaultDockerNetworks = []string{"172.17.0.0/16", "172.30.0.0/24", "172.31.251.0/24", "172.31.250.0/24"}

// alwaysDenied are ranges no user URL may reach, even with allow_private_targets.
var alwaysDenied = mustPrefixes(
	"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	"::/128", "::1/128", "fe80::/10", "ff00::/8",
)

// privateRanges are denied unless allow_private_targets is on.
var privateRanges = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7")

// Required are the ranges the deny list must always cover (tests check them).
var Required = []string{"127.0.0.0/8", "169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Policy is the deny list of one org.
type Policy struct {
	// AllowPrivate is the org setting allow_private_targets.
	AllowPrivate bool
	// Denied are extra ranges that are always denied: the mesh CIDR and the Docker networks.
	Denied []netip.Prefix
	// Resolver looks up host names. Nil means net.DefaultResolver.
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// New makes a policy. meshCIDR and dockerNetworks are added to the always-denied list.
// Bad CIDRs are an error.
func New(allowPrivate bool, meshCIDR string, dockerNetworks []string) (Policy, error) {
	p := Policy{AllowPrivate: allowPrivate}
	for _, s := range append([]string{meshCIDR}, dockerNetworks...) {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pf, err := netip.ParsePrefix(s)
		if err != nil {
			return p, fmt.Errorf("ssrf: %q is not a CIDR", s)
		}
		p.Denied = append(p.Denied, pf.Masked())
	}
	return p, nil
}

// CheckIP returns ErrDenied when ip is on the deny list.
func (p Policy) CheckIP(ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return ErrDenied
	}
	for _, list := range [][]netip.Prefix{alwaysDenied, p.Denied} {
		for _, pf := range list {
			if pf.Contains(ip) {
				return fmt.Errorf("%w: %s is in %s", ErrDenied, ip, pf)
			}
		}
	}
	if !p.AllowPrivate {
		for _, pf := range privateRanges {
			if pf.Contains(ip) {
				return fmt.Errorf("%w: %s is a private address (allow_private_targets is off)", ErrDenied, ip)
			}
		}
	}
	return nil
}

// Resolve looks up host and checks every address. An IP literal is checked as it is.
// It returns the allowed addresses. If one address is denied, the whole host is denied,
// so a DNS answer cannot mix a public and an internal address.
func (p Policy) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.Trim(host, "[]")
	if ip, err := netip.ParseAddr(host); err == nil {
		if err := p.CheckIP(ip); err != nil {
			return nil, err
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return nil, fmt.Errorf("%w: host %q", ErrDenied, host)
	}
	var r interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	} = net.DefaultResolver
	if p.Resolver != nil {
		r = p.Resolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("ssrf: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("ssrf: %s has no address", host)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if err := p.CheckIP(a); err != nil {
			return nil, err
		}
		out = append(out, a.Unmap())
	}
	return out, nil
}

// CheckURL checks the scheme (http, https, ssh) and the addresses of the host of a URL.
// scp-like Git addresses (git@host:org/repo.git) are accepted too.
func (p Policy) CheckURL(ctx context.Context, raw string) error {
	host, _, err := HostPort(raw)
	if err != nil {
		return err
	}
	_, err = p.Resolve(ctx, host)
	return err
}

// HostPort returns the host and port of a URL or of an scp-like Git address.
// The port is empty when the URL has none.
func HostPort(raw string) (host, port string, err error) {
	if !strings.Contains(raw, "://") {
		// scp-like: [user@]host:path
		at := strings.LastIndex(raw[:max(strings.Index(raw, ":"), 0)], "@")
		rest := raw[at+1:]
		h, _, ok := strings.Cut(rest, ":")
		if !ok || h == "" || strings.ContainsAny(h, "/ ") {
			return "", "", fmt.Errorf("ssrf: %q is not a valid URL", raw)
		}
		return h, "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("ssrf: %q is not a valid URL", raw)
	}
	switch u.Scheme {
	case "http", "https", "ssh":
	default:
		return "", "", fmt.Errorf("%w: scheme %q", ErrDenied, u.Scheme)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("ssrf: %q has no host", raw)
	}
	return u.Hostname(), u.Port(), nil
}

// control runs at connect time, after DNS: it checks the real address of the socket.
func (p Policy) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDenied, address)
	}
	return p.CheckIP(ap.Addr())
}

// Dialer returns a dialer that refuses denied addresses at connect time.
func (p Policy) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: p.control}
}

// Client returns an HTTP client that checks every connection and every redirect.
// No proxy from the environment is used, so the check sees the real target.
func (p Policy) Client(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         p.Dialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("ssrf: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: redirect to scheme %q", ErrDenied, req.URL.Scheme)
			}
			_, err := p.Resolve(req.Context(), req.URL.Hostname())
			return err
		},
	}
}
