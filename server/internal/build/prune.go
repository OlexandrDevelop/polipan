// SPDX-License-Identifier: Apache-2.0

package build

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"
)

// Prune removes unused build cache of buildkitd: first the records older than keepFor, then the least recently
// used ones until at most maxMB megabytes stay. BuildKit never removes a record that a running build uses, and
// --all is not given, so the frontend images stay too.
func (b *BuildKit) Prune(ctx context.Context, keepFor time.Duration, maxMB int) error {
	base := []string{"--addr", b.Addr}
	if b.CertDir != "" {
		base = append(base,
			"--tlscacert", filepath.Join(b.CertDir, "ca.crt"),
			"--tlscert", filepath.Join(b.CertDir, "client.crt"),
			"--tlskey", filepath.Join(b.CertDir, "client.key"),
			"--tlsservername", "buildkitd")
	}
	runs := [][]string{
		{"prune", "--keep-duration", keepFor.String()},
		{"prune", "--keep-storage", strconv.Itoa(maxMB)},
	}
	for _, r := range runs {
		var last string
		args := append(append([]string{}, base...), r...)
		if err := b.run(ctx, b.buildctl(), args, nil, func(l string) { last = l }); err != nil {
			return fmt.Errorf("buildctl %v: %w (%s)", r, err, last)
		}
	}
	return nil
}

// Defaults of the cache policy (12-observability section 7.1).
const (
	DefaultCacheKeepFor = 7 * 24 * time.Hour
	DefaultCacheMaxMB   = 5120
	cacheEvery          = time.Hour
	cacheCheck          = 5 * time.Minute
	cacheHighPause      = 15 * time.Minute
)

// CacheCleaner prunes the build cache every hour, and sooner when High says that the BuildKit disk is high.
type CacheCleaner struct {
	BuildKit *BuildKit
	KeepFor  time.Duration
	MaxMB    int
	// High is true when the file system of the BuildKit state is at the cleanup level. Optional.
	High func(ctx context.Context) bool
	Log  *slog.Logger

	last time.Time
}

// Run works until ctx ends.
func (c *CacheCleaner) Run(ctx context.Context) {
	if c.KeepFor <= 0 {
		c.KeepFor = DefaultCacheKeepFor
	}
	if c.MaxMB <= 0 {
		c.MaxMB = DefaultCacheMaxMB
	}
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(cacheCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		since := time.Since(c.last)
		reason := ""
		switch {
		case c.last.IsZero() || since >= cacheEvery:
			reason = "schedule"
		case since >= cacheHighPause && c.High != nil && c.High(ctx):
			reason = "disk high"
		default:
			continue
		}
		c.last = time.Now()
		pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := c.BuildKit.Prune(pctx, c.KeepFor, c.MaxMB)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("build cache prune failed", "reason", reason, "err", err)
			}
			continue
		}
		log.Info("build cache pruned", "reason", reason, "keep_for", c.KeepFor.String(), "max_mb", c.MaxMB)
	}
}
