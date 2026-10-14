// SPDX-License-Identifier: Apache-2.0

package control_test

import (
	"strings"
	"testing"

	"github.com/polipan/polipan/server/internal/control"
)

// T-383: the environment domain comes from the id and is unique per environment.
func TestEnvDomain(t *testing.T) {
	if got := control.EnvDomain("env_01ABC_D"); got != "e-01abc-d" {
		t.Fatalf("domain: %s", got)
	}
	if control.EnvServiceName("App", "env_1") != "app.e-1.env" {
		t.Fatalf("service name: %s", control.EnvServiceName("App", "env_1"))
	}
	if control.EnvServiceName("app", "env_1") == control.EnvServiceName("app", "env_2") {
		t.Fatal("two environments must not share a name")
	}
	// A slug has no dot, so a name with three labels can never equal <service>.<slug>.
	if strings.Count(control.EnvServiceName("app", "env_1"), ".") != 2 {
		t.Fatal("the environment name must have three labels")
	}
	if got := control.EnvDNSSearch("env_1"); len(got) != 1 || got[0] != "e-1.env.polipan.internal" {
		t.Fatalf("search: %v", got)
	}
}
