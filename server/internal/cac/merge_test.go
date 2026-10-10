// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"reflect"
	"testing"
)

const mergeDoc = `version: 1
project: shop
environments:
  production:
    kind: production
    deploy: { strategy: blue_green, drain_seconds: 30 }
    services:
      api:
        domains: [{ host: prod.example.com, port: 8080 }]
        env: { LOG: warn, ONLY_PROD: "1" }
        deploy: { drain_seconds: 10 }
      worker: { enabled: false }
  staging:
    kind: staging
services:
  api:
    source: { image: { name: x } }
    ports: [8080]
    domains: [{ host: base.example.com, port: 8080 }, { host: other.example.com, port: 8080 }]
    env: { LOG: info, KEEP: "yes" }
    deploy: { strategy: recreate }
  worker:
    source: { image: { name: x } }
`

// REQ-CAC-101: the merge order and the list and map rules of 07 section 3.
func TestREQ_CAC_101_MergeRules(t *testing.T) {
	p, issues := Parse([]byte(mergeDoc))
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	prod, _ := p.File.EffectiveService("production", "api")
	stag, _ := p.File.EffectiveService("staging", "api")
	// A list of an override replaces the list of the base.
	if len(prod.Service.Domains) != 1 || prod.Service.Domains[0].Host != "prod.example.com" {
		t.Errorf("domains of production: %+v", prod.Service.Domains)
	}
	if len(stag.Service.Domains) != 2 {
		t.Errorf("domains of staging: %+v", stag.Service.Domains)
	}
	// A map merges key by key.
	got := map[string]string{}
	for k, v := range prod.Service.Env {
		got[k] = v.Value
	}
	if want := map[string]string{"LOG": "warn", "KEEP": "yes", "ONLY_PROD": "1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("env of production: %v", got)
	}
	// 1. base, 2. environment deploy, 3. override of the service.
	if d := prod.Service.Deploy; d == nil || d.Strategy != "blue_green" || d.DrainSeconds == nil || *d.DrainSeconds != 10 {
		t.Errorf("deploy of production: %+v", d)
	}
	if d := stag.Service.Deploy; d == nil || d.Strategy != "recreate" {
		t.Errorf("deploy of staging: %+v", d)
	}
	// enabled: false turns a service off in one environment.
	if w, _ := p.File.EffectiveService("production", "worker"); !w.Disabled {
		t.Errorf("worker is not disabled in production")
	}
	if w, _ := p.File.EffectiveService("staging", "worker"); w.Disabled {
		t.Errorf("worker is disabled in staging")
	}
}

// REQ-CAC-102: the plan hash changes when the file or the live state changes.
func TestREQ_CAC_102_PlanHash(t *testing.T) {
	live := &Live{Slug: "shop", Envs: map[string]*LiveEnv{}, ProjectVars: map[string]LiveVar{}, Targets: map[string]*LiveTarget{}}
	a := NewPlan([]byte(mergeDoc), live, BuildContext{})
	b := NewPlan([]byte(mergeDoc), live, BuildContext{})
	if a.PlanHash == "" || a.PlanHash != b.PlanHash {
		t.Fatalf("the hash is not stable: %q %q", a.PlanHash, b.PlanHash)
	}
	if c := NewPlan([]byte(mergeDoc+"\n"), live, BuildContext{}); c.PlanHash == a.PlanHash {
		t.Errorf("the hash does not change with the file")
	}
	live2 := &Live{Slug: "shop", Envs: map[string]*LiveEnv{"staging": {Slug: "staging", Kind: "staging", Services: map[string]*LiveService{}, Vars: map[string]LiveVar{}}},
		ProjectVars: map[string]LiveVar{}, Targets: map[string]*LiveTarget{}}
	if d := NewPlan([]byte(mergeDoc), live2, BuildContext{}); d.PlanHash == a.PlanHash {
		t.Errorf("the hash does not change with the live state")
	}
}

// The units of 07 section 4.
func TestUnits(t *testing.T) {
	if n, err := CPUMillis("500m"); err != nil || n != 500 {
		t.Errorf("500m: %d %v", n, err)
	}
	if n, err := CPUMillis("0.5"); err != nil || n != 500 {
		t.Errorf("0.5: %d %v", n, err)
	}
	if n, err := Bytes("512Mi"); err != nil || n != 536870912 {
		t.Errorf("512Mi: %d %v", n, err)
	}
	for in, want := range map[string]int{"30s": 30, "5m": 300, "7d": 604800} {
		if n, err := Seconds(in); err != nil || n != want {
			t.Errorf("%s: %d %v", in, n, err)
		}
	}
}

// REQ-CAC-009: the export of a project is a valid file and has no secret value.
func TestExportIsValid(t *testing.T) {
	live := &Live{Slug: "shop", Envs: map[string]*LiveEnv{"production": {ID: "env_1", Slug: "production", Kind: "production", Protected: true,
		Vars: map[string]LiveVar{}, Services: map[string]*LiveService{"api": {ID: "svc_1", Name: "api", Type: "app", Failover: "none",
			Config: map[string]any{"image": "nginx:1.27", "port": 80, "restart_policy": "always", "resources": map[string]any{"cpu_millis": 500, "memory_bytes": 536870912}},
			Vars:   map[string]LiveVar{"LOG": {Value: "info"}}}}}},
		ProjectVars: map[string]LiveVar{}, Targets: map[string]*LiveTarget{},
		Secrets: []LiveSecret{{ID: "sec_1", Name: "DB_PASSWORD", ServiceID: "svc_1", EnvID: "env_1"}}}
	raw, err := Export(live)
	if err != nil {
		t.Fatal(err)
	}
	p, issues := Parse(raw)
	if len(issues) > 0 {
		t.Fatalf("%v\n%s", issues, raw)
	}
	if got := p.File.Services["api"]["secrets"]; !reflect.DeepEqual(got, []any{"DB_PASSWORD"}) {
		t.Errorf("secrets: %v\n%s", got, raw)
	}
}
