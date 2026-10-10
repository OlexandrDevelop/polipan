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
	"os"
	"strconv"
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
}

// Default returns the config with the default values.
func Default() Config {
	return Config{
		Listen:          ":8080",
		LogLevel:        "info",
		APIRateLimit:    600,
		ShutdownTimeout: 15 * time.Second,
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
	if c.APIRateLimit < 1 {
		return errors.New("config: api_rate_limit must be at least 1")
	}
	return nil
}
