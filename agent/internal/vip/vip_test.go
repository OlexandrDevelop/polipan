// SPDX-License-Identifier: Apache-2.0

package vip

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func pool(t *testing.T) *net.IPNet {
	_, n, err := net.ParseCIDR(DefaultCIDR)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAllocateIsStableAndUnique(t *testing.T) {
	ids := []string{"svc_a", "svc_b", "svc_c", "svc_d"}
	a := Allocate(pool(t), ids)
	b := Allocate(pool(t), []string{"svc_d", "svc_c", "svc_b", "svc_a"})
	seen := map[string]bool{}
	for _, id := range ids {
		if a[id] == "" || a[id] != b[id] {
			t.Fatalf("%s: %q vs %q", id, a[id], b[id])
		}
		if seen[a[id]] {
			t.Fatalf("address %s used twice", a[id])
		}
		seen[a[id]] = true
		if !pool(t).Contains(net.ParseIP(a[id])) {
			t.Fatalf("%s outside the pool", a[id])
		}
	}
}

func TestAllocateClashTakesNextAddress(t *testing.T) {
	_, tiny, _ := net.ParseCIDR("198.18.0.0/29") // 6 usable addresses
	ids := []string{"1", "2", "3", "4", "5", "6", "7"}
	out := Allocate(tiny, ids)
	if len(out) != 6 {
		t.Fatalf("got %d addresses: %v", len(out), out)
	}
	seen := map[string]bool{}
	for _, v := range out {
		if seen[v] {
			t.Fatalf("clash: %v", out)
		}
		seen[v] = true
	}
}

func TestRulesetSingleAndMultiBackend(t *testing.T) {
	vips := map[string]string{"s1": "198.18.0.5", "s2": "198.18.0.6"}
	script := Ruleset(vips, []Service{{ID: "s1", Backends: []string{"10.0.0.2"}}, {ID: "s2", Backends: []string{"10.0.0.4", "10.0.1.3", "10.0.0.4"}}}, "10.0.0.0/24")
	for _, want := range []string{
		"delete table ip polipan_nat",
		"ip daddr 198.18.0.5 dnat to 10.0.0.2",
		"ip daddr 198.18.0.6 dnat to numgen random mod 2 map { 0 : 10.0.0.4, 1 : 10.0.1.3 }",
		"ip saddr 10.0.0.0/24 ip daddr 10.0.0.0/24 ct status dnat masquerade",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in:\n%s", want, script)
		}
	}
}

func TestRulesetWithoutBackendsClearsTable(t *testing.T) {
	script := Ruleset(map[string]string{"s1": "198.18.0.5"}, []Service{{ID: "s1"}}, "10.0.0.0/24")
	if strings.Contains(script, "dnat") || !strings.Contains(script, "delete table ip polipan_nat") {
		t.Fatalf("script:\n%s", script)
	}
}

func TestManagerAppliesOnlyChanges(t *testing.T) {
	var scripts []string
	m := &Manager{Enabled: true, Run: func(_ context.Context, s string) error { scripts = append(scripts, s); return nil }}
	ctx := context.Background()
	blue := []Service{{ID: "s1", Backends: []string{"10.0.0.2"}}}
	green := []Service{{ID: "s1", Backends: []string{"10.0.0.3"}}}
	v1, err := m.Apply(ctx, blue, "10.0.0.0/24")
	if err != nil || v1["s1"] == "" {
		t.Fatalf("%v %v", v1, err)
	}
	if _, err := m.Apply(ctx, blue, "10.0.0.0/24"); err != nil || len(scripts) != 1 {
		t.Fatalf("unchanged rules must not be written again: %d", len(scripts))
	}
	v2, err := m.Apply(ctx, green, "10.0.0.0/24")
	if err != nil || len(scripts) != 2 {
		t.Fatalf("%v scripts %d", err, len(scripts))
	}
	if v1["s1"] != v2["s1"] {
		t.Fatalf("the address of the service changed: %s -> %s", v1["s1"], v2["s1"])
	}
	if !strings.Contains(scripts[1], "dnat to 10.0.0.3") {
		t.Fatalf("script:\n%s", scripts[1])
	}
}

func TestManagerErrorsAreRetried(t *testing.T) {
	fail := true
	calls := 0
	m := &Manager{Enabled: true, Run: func(context.Context, string) error {
		calls++
		if fail {
			return errors.New("boom")
		}
		return nil
	}}
	svc := []Service{{ID: "s1", Backends: []string{"10.0.0.2"}}}
	if v, err := m.Apply(context.Background(), svc, "10.0.0.0/24"); err == nil || v != nil {
		t.Fatalf("want an error, got %v %v", v, err)
	}
	fail = false
	if v, err := m.Apply(context.Background(), svc, "10.0.0.0/24"); err != nil || v["s1"] == "" || calls != 2 {
		t.Fatalf("%v %v calls %d", v, err, calls)
	}
}

func TestManagerDisabledOrNoSubnet(t *testing.T) {
	var m *Manager
	if v, err := m.Apply(context.Background(), nil, "x"); v != nil || err != nil {
		t.Fatal("nil manager")
	}
	m = &Manager{Enabled: true, Run: func(context.Context, string) error { return nil }}
	if _, err := m.Apply(context.Background(), nil, ""); err == nil {
		t.Fatal("no subnet must be an error")
	}
}
