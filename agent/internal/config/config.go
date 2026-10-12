// SPDX-License-Identifier: Apache-2.0

// Package config holds the settings of the agent. They come from flags and POLIPAN_AGENT_* variables.
package config

import (
	"errors"
	"strconv"
	"time"
)

// DefaultDataDir is where the agent keeps desired.json and its keys (02-architecture, section 6).
const DefaultDataDir = "/var/lib/polipan-agent"

// Config is the agent configuration.
type Config struct {
	// Server is the address of the server gateway, for example panel.example.com:9443.
	Server string
	// DataDir is the folder of the agent state.
	DataDir string
	// LogLevel is debug, info, warn or error.
	LogLevel string
	// Network is the docker network of the workloads.
	Network string
	// Subnet is used only when the network has to be created. Empty = the mesh subnet of the node (09 section 6),
	// or 172.30.0.0/24 when the node has none.
	Subnet string
	// PublicIP is the address other nodes use for the WireGuard endpoint. Empty = the address of the default route.
	PublicIP string
	// WireguardPort is the UDP port of the mesh (default 51820).
	WireguardPort int
	// Firewall turns the nftables rules on or off.
	Firewall bool
	// Token and CAFingerprint let a new agent join when it has no identity yet.
	Token         string
	CAFingerprint string
	// ACMECAURL overrides the ACME directory (tests).
	ACMECAURL string
	// ACMETrustedRoots is a PEM file that trusts ACMECAURL (tests with a local CA).
	ACMETrustedRoots string
	// CaddyImage is the Caddy image, empty = default. A wildcard certificate by DNS-01 needs a Caddy build with the DNS module.
	CaddyImage string
	// DNS01Provider is the name of the Caddy DNS module (for example cloudflare) for the wildcard certificates, and DNS01Config
	// its config as a JSON object (the credentials stay on this node). WildcardDomains is a comma list like *.preview.example.com
	// (REQ-DOM-007). Without them every host gets its own certificate by HTTP-01.
	DNS01Provider   string
	DNS01Config     string
	WildcardDomains string
	// DockerSocket is the path of the Docker unix socket.
	DockerSocket string
	// MetricsAddr is where the agent serves its metrics for vmagent (12 section 7). Use a loopback address.
	MetricsAddr string
	// BuildKitRoot is the host path of the BuildKit state. Empty = found from the buildkitd container.
	BuildKitRoot string
	// Cleanup turns the removal of dangling images on or off. CleanupInterval is the time between two runs and
	// CleanupHighPercent the disk level that starts a run sooner (12 section 7.1).
	Cleanup            bool
	CleanupInterval    time.Duration
	CleanupHighPercent float64
}

// FromEnv returns the default config changed by the environment variables.
func FromEnv(getenv func(string) string) Config {
	c := Config{DataDir: DefaultDataDir, LogLevel: "info", Network: "polipan", WireguardPort: 51820,
		Firewall: true, DockerSocket: "/var/run/docker.sock", MetricsAddr: "127.0.0.1:9445",
		Cleanup: true, CleanupInterval: time.Hour, CleanupHighPercent: 85}
	set := func(dst *string, key string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	set(&c.Server, "POLIPAN_AGENT_SERVER")
	set(&c.DataDir, "POLIPAN_AGENT_DATA_DIR")
	set(&c.LogLevel, "POLIPAN_AGENT_LOG_LEVEL")
	set(&c.Network, "POLIPAN_AGENT_NETWORK")
	set(&c.Subnet, "POLIPAN_AGENT_SUBNET")
	set(&c.PublicIP, "POLIPAN_AGENT_PUBLIC_IP")
	set(&c.Token, "POLIPAN_AGENT_TOKEN")
	set(&c.CAFingerprint, "POLIPAN_AGENT_CA_FINGERPRINT")
	set(&c.ACMECAURL, "POLIPAN_AGENT_ACME_CA_URL")
	set(&c.ACMETrustedRoots, "POLIPAN_AGENT_ACME_TRUSTED_ROOTS")
	set(&c.CaddyImage, "POLIPAN_AGENT_CADDY_IMAGE")
	set(&c.DNS01Provider, "POLIPAN_AGENT_DNS01_PROVIDER")
	set(&c.DNS01Config, "POLIPAN_AGENT_DNS01_CONFIG")
	set(&c.WildcardDomains, "POLIPAN_AGENT_WILDCARD_DOMAINS")
	set(&c.DockerSocket, "POLIPAN_AGENT_DOCKER_SOCKET")
	set(&c.MetricsAddr, "POLIPAN_AGENT_METRICS_ADDR")
	set(&c.BuildKitRoot, "POLIPAN_AGENT_BUILDKIT_ROOT")
	if v := getenv("POLIPAN_AGENT_WIREGUARD_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			c.WireguardPort = n
		}
	}
	if v := getenv("POLIPAN_AGENT_FIREWALL"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Firewall = b
		}
	}
	if v := getenv("POLIPAN_AGENT_CLEANUP"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Cleanup = b
		}
	}
	if v := getenv("POLIPAN_AGENT_CLEANUP_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
			c.CleanupInterval = d
		}
	}
	if v := getenv("POLIPAN_AGENT_CLEANUP_HIGH_PERCENT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 100 {
			c.CleanupHighPercent = f
		}
	}
	return c
}

// Validate checks the values.
func (c Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("config: data dir is required")
	}
	if c.Network == "" {
		return errors.New("config: network is required")
	}
	return nil
}
