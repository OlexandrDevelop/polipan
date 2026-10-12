// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"strings"
)

// Config comes from environment variables.
type Config struct {
	DatabaseURL    string
	Secret         []byte
	BaseURL        string
	Addr           string
	SiteName       string
	MainURL        string
	DocsURL        string
	FirstUserAdmin bool
	TrustedProxy   bool
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (*Config, error) {
	c := &Config{
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		Secret:         []byte(os.Getenv("FORUM_SECRET")),
		BaseURL:        strings.TrimRight(env("FORUM_BASE_URL", "http://localhost:8080"), "/"),
		Addr:           env("FORUM_ADDR", ":8080"),
		SiteName:       env("FORUM_SITE_NAME", "Polipan Community"),
		MainURL:        env("FORUM_MAIN_URL", "https://polipan.dev"),
		DocsURL:        env("FORUM_DOCS_URL", "https://docs.polipan.dev"),
		FirstUserAdmin: os.Getenv("FORUM_FIRST_USER_ADMIN") == "true",
		TrustedProxy:   os.Getenv("FORUM_TRUSTED_PROXY") == "true",
	}
	if c.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if len(c.Secret) < 32 {
		return nil, errors.New("FORUM_SECRET is required and must have at least 32 characters")
	}
	return c, nil
}

func (c *Config) secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }
