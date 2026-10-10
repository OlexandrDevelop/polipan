// SPDX-License-Identifier: Apache-2.0

package build_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const repoRoot = "../../../"

type composeFile struct {
	Services map[string]struct {
		Image       string         `yaml:"image"`
		Privileged  bool           `yaml:"privileged"`
		Ports       []string       `yaml:"ports"`
		Volumes     []string       `yaml:"volumes"`
		SecurityOpt []string       `yaml:"security_opt"`
		NetworkMode string         `yaml:"network_mode"`
		Networks    map[string]any `yaml:"networks"`
		CPUs        any            `yaml:"cpus"`
		MemLimit    any            `yaml:"mem_limit"`
	} `yaml:"services"`
	Networks map[string]struct {
		Name       string            `yaml:"name"`
		DriverOpts map[string]string `yaml:"driver_opts"`
		IPAM       struct {
			Config []struct {
				Subnet string `yaml:"subnet"`
			} `yaml:"config"`
		} `yaml:"ipam"`
	} `yaml:"networks"`
}

// AC-SEC-06 (static part, REQ-SEC-102, REQ-SEC-108): buildkitd is on its own network, rootless,
// not privileged, without the Docker socket and with limits; PostgreSQL and the registry have no
// host port and are not on the build network.
func TestAC_SEC_06_ComposeBuildNetwork(t *testing.T) {
	raw, err := os.ReadFile(repoRoot + "deploy/compose/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c composeFile
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	bk, ok := c.Services["buildkitd"]
	if !ok {
		t.Fatal("no buildkitd service")
	}
	if len(bk.Networks) != 1 || bk.Networks["build"] == nil || bk.Privileged || len(bk.Ports) != 0 || !strings.Contains(bk.Image, "rootless") ||
		bk.CPUs == nil || bk.MemLimit == nil {
		t.Fatalf("buildkitd: %+v", bk)
	}
	for _, v := range bk.Volumes {
		if strings.Contains(v, "docker.sock") {
			t.Fatal("buildkitd mounts the Docker socket")
		}
	}
	for _, o := range bk.SecurityOpt {
		if strings.Contains(o, "privileged") {
			t.Fatalf("security_opt %s", o)
		}
	}
	nw := c.Networks["build"]
	if nw.Name != "polipan-build" || nw.DriverOpts["com.docker.network.bridge.name"] != "pp-build0" ||
		len(nw.IPAM.Config) != 1 || nw.IPAM.Config[0].Subnet != "172.31.250.0/24" {
		t.Fatalf("build network: %+v", nw)
	}
	for _, name := range []string{"postgres", "registry"} {
		s := c.Services[name]
		if len(s.Ports) != 0 || s.Networks["build"] != nil || s.Networks["polipan"] != nil {
			t.Fatalf("%s must bind only to the internal network: %+v", name, s)
		}
	}
	for name, s := range c.Services {
		if name != "buildkitd" && name != "server" && s.Networks["build"] != nil {
			t.Fatalf("%s is on the build network", name)
		}
	}
	srv := c.Services["server"]
	if b, _ := srv.Networks["build"].(map[string]any); b == nil || b["ipv4_address"] != "172.31.250.3" {
		t.Fatalf("server on the build network: %v", srv.Networks["build"])
	}
}

func nftRules(t *testing.T, env ...string) string {
	t.Helper()
	cmd := exec.Command("sh", repoRoot+"deploy/nftables/polipan-build.sh", "--print")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

// AC-SEC-06 (rules part): the nftables rules drop new connections from the build network to the
// host (loopback, bridge gateway), the control-plane and Docker networks, mesh_cidr, link-local
// and RFC 1918. With allow_private_targets RFC 1918 opens, the rest stays dropped.
func TestAC_SEC_06_NftablesRules(t *testing.T) {
	rules := nftRules(t, "MESH_CIDR=10.99.0.0/16")
	for _, want := range []string{"127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "10.99.0.0/16", "10.0.0.0/8", "192.168.0.0/16",
		`iifname "pp-build0" ip saddr != 172.31.250.3 ip daddr @deny4 counter drop`,
		`iifname "pp-build0" ip saddr != 172.31.250.3 counter drop`, "hook input", "hook forward", "ct state established,related accept"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("rules miss %q:\n%s", want, rules)
		}
	}
	open := nftRules(t, "MESH_CIDR=10.99.0.0/16", "ALLOW_PRIVATE=1")
	if strings.Contains(open, "10.0.0.0/8") || strings.Contains(open, "192.168.0.0/16") ||
		!strings.Contains(open, "10.99.0.0/16") || !strings.Contains(open, "172.16.0.0/12") || !strings.Contains(open, "127.0.0.0/8") {
		t.Fatalf("allow_private rules:\n%s", open)
	}
}

// AC-SEC-06 (real check): BuildKit and nftables in a throw-away Docker host. Needs Docker and
// POLIPAN_E2E_SEC06=1, else it is skipped.
func TestAC_SEC_06_Integration(t *testing.T) {
	if os.Getenv("POLIPAN_E2E_SEC06") != "1" {
		t.Skip("set POLIPAN_E2E_SEC06=1 to run (needs Docker)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker")
	}
	cmd := exec.Command("sh", repoRoot+"test/e2e/sec06.sh")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "AC-SEC-06 PASSED") {
		t.Fatalf("%v\n%s", err, out)
	}
}
