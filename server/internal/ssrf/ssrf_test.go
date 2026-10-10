// SPDX-License-Identifier: Apache-2.0

package ssrf_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/polipan/polipan/server/internal/ssrf"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range f[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	if out == nil {
		return nil, errors.New("no such host")
	}
	return out, nil
}

func policy(t *testing.T, allowPrivate bool) ssrf.Policy {
	t.Helper()
	p, err := ssrf.New(allowPrivate, "10.210.0.0/16", ssrf.DefaultDockerNetworks)
	if err != nil {
		t.Fatal(err)
	}
	p.Resolver = fakeResolver{
		"public.example":  {"93.184.216.34"},
		"rebind.example":  {"93.184.216.34", "127.0.0.1"},
		"meta.example":    {"169.254.169.254"},
		"private.example": {"192.168.1.10"},
	}
	return p
}

// REQ-SEC-104: the deny list covers loopback, link-local, mesh, Docker networks and RFC 1918.
func TestREQ_SEC_104_DenyList(t *testing.T) {
	p := policy(t, false)
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "10.210.3.4", "172.17.0.2",
		"172.30.0.5", "172.31.251.10", "172.31.250.2", "10.1.2.3", "172.16.0.1", "192.168.0.1", "fd00::1", "::ffff:127.0.0.1"} {
		if err := p.CheckIP(netip.MustParseAddr(ip)); !errors.Is(err, ssrf.ErrDenied) {
			t.Errorf("%s must be denied, got %v", ip, err)
		}
	}
	for _, ip := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if err := p.CheckIP(netip.MustParseAddr(ip)); err != nil {
			t.Errorf("%s must be allowed: %v", ip, err)
		}
	}
	for _, r := range ssrf.Required {
		pf := netip.MustParsePrefix(r)
		if err := p.CheckIP(pf.Addr().Next()); err == nil {
			t.Errorf("required range %s is not covered", r)
		}
	}

	// allow_private_targets opens RFC 1918, but never loopback, link-local, mesh or Docker.
	open := policy(t, true)
	if err := open.CheckIP(netip.MustParseAddr("192.168.0.1")); err != nil {
		t.Errorf("private allowed: %v", err)
	}
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "10.210.3.4", "172.30.0.5"} {
		if err := open.CheckIP(netip.MustParseAddr(ip)); err == nil {
			t.Errorf("%s must stay denied with allow_private_targets", ip)
		}
	}
}

// The check runs on the addresses after DNS: one bad address denies the host.
func TestREQ_SEC_104_AfterDNS(t *testing.T) {
	p := policy(t, false)
	ctx := context.Background()
	if err := p.CheckURL(ctx, "https://public.example/acme/shop.git"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://rebind.example/x", "https://meta.example/", "http://private.example/", "http://localhost:5432/",
		"git@meta.example:acme/shop.git", "file:///etc/passwd", "ext::sh -c id", "https://[::1]/"} {
		if err := p.CheckURL(ctx, u); err == nil {
			t.Errorf("%s must be denied", u)
		}
	}
	if err := p.CheckURL(ctx, "git@public.example:acme/shop.git"); err != nil {
		t.Errorf("scp-like url: %v", err)
	}
}

// The client refuses a denied address at connect time and on a redirect.
func TestREQ_SEC_104_ClientAndRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	p := policy(t, false)
	c := p.Client(5 * time.Second)
	if _, err := c.Get(srv.URL); err == nil || !errors.Is(err, ssrf.ErrDenied) {
		t.Fatalf("connect to loopback: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://meta.example/latest", nil)
	if err := c.CheckRedirect(req, []*http.Request{{}}); err == nil {
		t.Fatal("redirect to a denied host must fail")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://public.example/", nil)
	if err := c.CheckRedirect(req, []*http.Request{{}}); err != nil {
		t.Fatalf("redirect to a public host: %v", err)
	}
}
