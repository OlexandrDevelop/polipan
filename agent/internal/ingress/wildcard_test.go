// SPDX-License-Identifier: Apache-2.0

package ingress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/polipan/polipan/proto/agentv1"
)

func wildcardSpec() *agentv1.IngressSpec {
	up := []*agentv1.Upstream{{Address: "10.0.0.2:80", Weight: 100}}
	return &agentv1.IngressSpec{AcmeEmail: "ops@example.com", Routes: []*agentv1.Route{
		{RouteId: "r1", Hosts: []string{"pr-5-web.preview.example.com"}, PathPrefix: "/", Https: true, Upstreams: up},
		{RouteId: "r2", Hosts: []string{"pr-6-web.preview.example.com"}, PathPrefix: "/", Https: true, Upstreams: up},
		{RouteId: "r3", Hosts: []string{"app.example.com"}, PathPrefix: "/", Https: true, Upstreams: up},
		// Two labels under the wildcard are not covered by it.
		{RouteId: "r4", Hosts: []string{"a.b.preview.example.com"}, PathPrefix: "/", Https: true, Upstreams: up},
	}}
}

// REQ-PREV-005, REQ-DOM-007: with a DNS-01 provider the hosts of the previews share one wildcard certificate. The other
// hosts keep their own certificates.
func TestBuildWildcardCertificate(t *testing.T) {
	dns, err := ParseDNS01("cloudflare", `{"api_token":"tok"}`, "*.preview.example.com")
	if err != nil || dns == nil {
		t.Fatalf("parse: %v %v", dns, err)
	}
	b, err := BuildConfig(wildcardSpec(), Options{DNS01: dns})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, b)
	tls := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	policies := tls["automation"].(map[string]any)["policies"].([]any)
	if len(policies) != 2 {
		t.Fatalf("policies: %v", policies)
	}
	wild := policies[0].(map[string]any)
	if got, _ := json.Marshal(wild["subjects"]); string(got) != `["*.preview.example.com"]` {
		t.Fatalf("the wildcard policy subjects: %s", got)
	}
	issuer := wild["issuers"].([]any)[0].(map[string]any)
	provider := issuer["challenges"].(map[string]any)["dns"].(map[string]any)["provider"].(map[string]any)
	if provider["name"] != "cloudflare" || provider["api_token"] != "tok" {
		t.Fatalf("the DNS provider: %v", provider)
	}
	other := policies[1].(map[string]any)
	if got, _ := json.Marshal(other["subjects"]); string(got) != `["a.b.preview.example.com","app.example.com"]` {
		t.Fatalf("the other subjects: %s", got)
	}
	if _, has := other["issuers"].([]any)[0].(map[string]any)["challenges"]; has {
		t.Fatal("a host without a wildcard uses the DNS challenge")
	}
	if got, _ := json.Marshal(tls["certificates"]); string(got) != `{"automate":["*.preview.example.com"]}` {
		t.Fatalf("certificates: %s", got)
	}
	srv := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["polipan"].(map[string]any)
	auto := srv["automatic_https"].(map[string]any)
	if got, _ := json.Marshal(auto["skip_certificates"]); string(got) != `["pr-5-web.preview.example.com","pr-6-web.preview.example.com"]` {
		t.Fatalf("skip_certificates: %s", got)
	}
}

// Without a provider, nothing changes: one certificate for each host.
func TestBuildWithoutWildcardKeepsHostCertificates(t *testing.T) {
	a, _ := BuildConfig(wildcardSpec(), Options{})
	if strings.Contains(string(a), "challenges") || strings.Contains(string(a), "automate") || strings.Contains(string(a), "skip_certificates") {
		t.Fatalf("a wildcard setting without a provider: %s", a)
	}
	dns, err := ParseDNS01("", "", "*.preview.example.com")
	if dns != nil || err != nil {
		t.Fatalf("no provider: %v %v", dns, err)
	}
}

func TestParseDNS01Errors(t *testing.T) {
	for _, in := range [][3]string{{"cloudflare", "", "preview.example.com"}, {"cloudflare", "", "*.*.example.com"}, {"cloudflare", "[1]", "*.preview.example.com"}} {
		if _, err := ParseDNS01(in[0], in[1], in[2]); err == nil {
			t.Errorf("%v: no error", in)
		}
	}
}
