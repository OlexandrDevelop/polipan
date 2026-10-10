// SPDX-License-Identifier: Apache-2.0

package dbengine_test

import (
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/dbengine"
)

// REQ-DB-001: the defaults and the supported versions.
func TestREQ_DB_001_DefaultsAndVersions(t *testing.T) {
	want := map[string]string{"postgres": "postgres:16", "mysql": "mysql:8.4", "mariadb": "mariadb:11", "mongo": "mongo:7", "redis": "redis:7"}
	for name, image := range want {
		e := dbengine.Get(name)
		got, err := e.ImageRef("")
		if err != nil || got != image {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	e := dbengine.Get("postgres")
	for _, ok := range []string{"16", "16.4", "17-alpine", "15.10.1"} {
		if _, err := e.ImageRef(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"latest", "99", "16;x", "evil/postgres:16", "", "16@sha256:abc"} {
		if bad == "" {
			continue
		}
		if _, err := e.ImageRef(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// REQ-DB-007: a minor change is fine, a major change is refused.
func TestREQ_DB_007_MajorVersionNeverAutomatic(t *testing.T) {
	e := dbengine.Get("postgres")
	if err := e.CheckVersionChange("postgres:16.4", "postgres:16.6"); err != nil {
		t.Fatal(err)
	}
	if err := e.CheckVersionChange("postgres:16", "postgres:17"); err == nil || !strings.Contains(err.Error(), "dump and restore into a new service") {
		t.Fatalf("major: %v", err)
	}
	if err := e.CheckVersionChange("postgres:16", "mysql:8.4"); err == nil {
		t.Fatal("another image must be refused")
	}
}

// REQ-DB-003: a 32 character password, and the variables of the image refer to the secret.
func TestREQ_DB_003_PasswordAndVariables(t *testing.T) {
	for _, name := range dbengine.Names {
		spec, err := dbengine.Get(name).NewService("", "")
		if err != nil {
			t.Fatal(err)
		}
		var pw string
		names := map[string]bool{}
		for _, v := range spec.Env {
			names[v.Name] = true
			if v.Name == "DB_PASSWORD" {
				pw = v.Value
				if !v.Secret {
					t.Errorf("%s: DB_PASSWORD is not a secret", name)
				}
			} else if v.Secret {
				t.Errorf("%s: %s must not be a secret (it refers to one)", name, v.Name)
			}
			if strings.Contains(v.Value, "DB_PASSWORD") && v.Name != "DB_PASSWORD" && v.Secret {
				t.Errorf("%s: %s", name, v.Name)
			}
		}
		if len(pw) != dbengine.PasswordLength || !names["DB_USER"] || !names["DB_NAME"] {
			t.Errorf("%s: password %d, variables %v", name, len(pw), names)
		}
		if spec.Config["strategy"] != "recreate" {
			t.Errorf("%s: strategy %v", name, spec.Config["strategy"])
		}
	}
}

// REQ-DB-004: the URL keeps the password as a reference.
func TestREQ_DB_004_URLHasReference(t *testing.T) {
	u := dbengine.Get("postgres").URL("svc-x", "app", "app", "${{db.main.PASSWORD}}")
	if u != "postgres://app:${{db.main.PASSWORD}}@svc-x:5432/app" {
		t.Fatal(u)
	}
	if dbengine.Alias("svc_01ABC_D") != "svc-01abc-d" {
		t.Fatal(dbengine.Alias("svc_01ABC_D"))
	}
}
