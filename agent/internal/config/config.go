// SPDX-License-Identifier: Apache-2.0

// Package config holds the settings of the agent. They come from flags and POLIPAN_AGENT_* variables.
package config

import "errors"

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
}

// FromEnv returns the default config changed by the environment variables.
func FromEnv(getenv func(string) string) Config {
	c := Config{DataDir: DefaultDataDir, LogLevel: "info"}
	if v := getenv("POLIPAN_AGENT_SERVER"); v != "" {
		c.Server = v
	}
	if v := getenv("POLIPAN_AGENT_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := getenv("POLIPAN_AGENT_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	return c
}

// Validate checks the values.
func (c Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("config: data dir is required")
	}
	return nil
}
