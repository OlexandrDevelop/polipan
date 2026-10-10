// SPDX-License-Identifier: Apache-2.0

// Package config loads the server settings from a YAML file and from
// POLIPAN_* environment variables. The environment wins (02-architecture, section 5).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is the default config file.
const DefaultPath = "/etc/polipan/server.yaml"

// Config is the server configuration.
type Config struct {
	// Listen is the address of the HTTP server.
	Listen string `yaml:"listen"`
	// DatabaseURL is the PostgreSQL connection string. It is required.
	DatabaseURL string `yaml:"database_url"`
	// LogLevel is debug, info, warn or error.
	LogLevel string `yaml:"log_level"`
	// APIRateLimit is the number of requests per minute per token or IP (glossary default 600).
	APIRateLimit int `yaml:"api_rate_limit"`
	// ShutdownTimeout is the time the server waits for open requests on stop.
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`

	// ExternalURL is the public address of the panel, for example https://panel.example.com.
	// The session cookie is Secure unless this is empty or http:// (local tests).
	ExternalURL string `yaml:"external_url"`
	// MasterKeyFile is the file with the master key (KEK). POLIPAN_MASTER_KEY (base64) wins.
	MasterKeyFile string `yaml:"master_key_file"`
	// AgentListen is the address of the agent gateway (gRPC, agent_grpc_port 9443).
	AgentListen string `yaml:"agent_listen"`
	// AgentHosts are the DNS names and IPs of the gateway certificate.
	AgentHosts []string `yaml:"agent_hosts"`
	// MeshCIDR is the pool of mesh subnets, one /24 per node.
	MeshCIDR string `yaml:"mesh_cidr"`
	// WireguardPort is the UDP port of the mesh on every node (glossary wireguard_port).
	WireguardPort int `yaml:"wireguard_port"`
	// HeartbeatInterval, NodeSuspectAfter, NodeDownAfter and JoinTokenTTL are the glossary defaults.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	NodeSuspectAfter  time.Duration `yaml:"node_suspect_after"`
	NodeDownAfter     time.Duration `yaml:"node_down_after"`
	JoinTokenTTL      time.Duration `yaml:"join_token_ttl"`
	// AgentCertValidity is the life of an agent certificate.
	AgentCertValidity time.Duration `yaml:"agent_cert_validity"`
	// SessionTTL is the sliding session life, SessionAbsoluteTTL the hard limit.
	SessionTTL         time.Duration `yaml:"session_ttl"`
	SessionAbsoluteTTL time.Duration `yaml:"session_absolute_ttl"`
	// ACMEEmail is the contact address for the certificate authority. Optional.
	ACMEEmail string `yaml:"acme_email"`
	// TrustedProxies are the networks of reverse proxies (the Caddy of the node). For a request from
	// one of them the client address is taken from X-Forwarded-For. Default: the polipan network.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// Default returns the config with the default values.
func Default() Config {
	return Config{
		Listen:          ":8080",
		LogLevel:        "info",
		APIRateLimit:    600,
		ShutdownTimeout: 15 * time.Second,

		TrustedProxies:     []string{"172.30.0.0/24"},
		MasterKeyFile:      "/etc/polipan/master.key",
		AgentListen:        ":9443",
		MeshCIDR:           "10.210.0.0/16",
		HeartbeatInterval:  5 * time.Second,
		WireguardPort:      51820,
		NodeSuspectAfter:   15 * time.Second,
		NodeDownAfter:      60 * time.Second,
		JoinTokenTTL:       time.Hour,
		AgentCertValidity:  30 * 24 * time.Hour,
		SessionTTL:         7 * 24 * time.Hour,
		SessionAbsoluteTTL: 30 * 24 * time.Hour,
	}
}

// Load reads the file at path (a missing file is fine), then applies the
// environment variables returned by getenv.
func Load(path string, getenv func(string) string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("config file %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// No file: use defaults and the environment.
	default:
		return cfg, fmt.Errorf("config file %s: %w", path, err)
	}
	if err := applyEnv(&cfg, getenv); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func applyEnv(cfg *Config, getenv func(string) string) error {
	if v := getenv("POLIPAN_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := getenv("POLIPAN_DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := getenv("POLIPAN_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := getenv("POLIPAN_API_RATE_LIMIT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("POLIPAN_API_RATE_LIMIT: %w", err)
		}
		cfg.APIRateLimit = n
	}
	for env, dst := range map[string]*string{
		"POLIPAN_EXTERNAL_URL":    &cfg.ExternalURL,
		"POLIPAN_MASTER_KEY_FILE": &cfg.MasterKeyFile,
		"POLIPAN_AGENT_LISTEN":    &cfg.AgentListen,
		"POLIPAN_MESH_CIDR":       &cfg.MeshCIDR,
		"POLIPAN_ACME_EMAIL":      &cfg.ACMEEmail,
	} {
		if v := getenv(env); v != "" {
			*dst = v
		}
	}
	if v := getenv("POLIPAN_TRUSTED_PROXIES"); v != "" {
		cfg.TrustedProxies = strings.Split(v, ",")
	}
	if v := getenv("POLIPAN_AGENT_HOSTS"); v != "" {
		cfg.AgentHosts = strings.Split(v, ",")
	}
	for env, dst := range map[string]*time.Duration{
		"POLIPAN_NODE_SUSPECT_AFTER": &cfg.NodeSuspectAfter,
		"POLIPAN_NODE_DOWN_AFTER":    &cfg.NodeDownAfter,
	} {
		if v := getenv(env); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s: not a duration", env)
			}
			*dst = d
		}
	}
	if v := getenv("POLIPAN_WIREGUARD_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("POLIPAN_WIREGUARD_PORT: not a port")
		}
		cfg.WireguardPort = n
	}
	if v := getenv("POLIPAN_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("POLIPAN_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	return nil
}

// Validate checks the values.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("config: database_url (POLIPAN_DATABASE_URL) is required")
	}
	for _, p := range c.TrustedProxies {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(p)); err != nil {
			return fmt.Errorf("config: trusted_proxies: %q is not a CIDR", p)
		}
	}
	if c.APIRateLimit < 1 {
		return errors.New("config: api_rate_limit must be at least 1")
	}
	return nil
}
