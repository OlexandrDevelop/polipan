// SPDX-License-Identifier: Apache-2.0

// Package cleanup removes the dangling images of the node by a fixed policy (12-observability section 7.1).
// A dangling image has no tag and no digest name, so no release, no service and no person can name it.
// It never touches volumes, images with a name, or images that any container (running or stopped) uses.
package cleanup

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/polipan/polipan/agent/internal/diskfs"
	"github.com/polipan/polipan/agent/internal/docker"
)

// Docker is the part of the Docker client that the cleanup uses.
type Docker interface {
	Images(ctx context.Context) ([]docker.Image, error)
	List(ctx context.Context, labels map[string]string) ([]docker.Container, error)
	ImageRemove(ctx context.Context, ref string) error
}

// Defaults of the policy.
const (
	DefaultInterval = time.Hour
	DefaultHigh     = 85.0
	// MinAge keeps a new image: a pull or a load may not have its name yet.
	MinAge = time.Hour
	// checkEvery is how often the disk level is checked. highPause is the least time between two runs when the
	// disk stays high.
	checkEvery = 5 * time.Minute
	highPause  = 15 * time.Minute
)

// Cleaner runs the cleanup every Interval, and sooner when a file system is at High percent or more.
type Cleaner struct {
	Docker   Docker
	Disks    func(ctx context.Context) []diskfs.FS // optional
	Interval time.Duration
	High     float64
	Log      *slog.Logger
	Now      func() time.Time

	last time.Time
}

// Result is what one run removed.
type Result struct {
	Images int
	Bytes  int64
}

func (c *Cleaner) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cleaner) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// Run checks every few minutes until ctx ends.
func (c *Cleaner) Run(ctx context.Context) {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.High <= 0 {
		c.High = DefaultHigh
	}
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		reason := c.due(ctx)
		if reason == "" {
			continue
		}
		c.last = c.now()
		res, err := c.Once(ctx)
		if err != nil {
			if ctx.Err() == nil {
				c.log().Warn("cleanup failed", "reason", reason, "error", err)
			}
			continue
		}
		c.log().Info("cleanup done", "reason", reason, "dangling_images", res.Images, "freed_bytes", res.Bytes)
	}
}

// due says why a run is due now, or "".
func (c *Cleaner) due(ctx context.Context) string {
	since := c.now().Sub(c.last)
	if c.last.IsZero() || since >= c.Interval {
		return "schedule"
	}
	if c.Disks == nil || since < highPause {
		return ""
	}
	for _, f := range c.Disks(ctx) {
		if f.Percent() >= c.High {
			return "disk " + f.Mount + " high"
		}
	}
	return ""
}

// Once removes the dangling images that no container uses and that are older than MinAge.
func (c *Cleaner) Once(ctx context.Context) (Result, error) {
	var res Result
	containers, err := c.Docker.List(ctx, nil)
	if err != nil {
		return res, err
	}
	used := map[string]bool{}
	for _, ct := range containers {
		used[ct.ImageID] = true
		used[ct.Image] = true
	}
	images, err := c.Docker.Images(ctx)
	if err != nil {
		return res, err
	}
	cutoff := c.now().Add(-MinAge)
	for _, img := range images {
		if !Dangling(img) || used[img.ID] || img.Created.After(cutoff) {
			continue
		}
		// Docker refuses to remove an image that a container uses (force is off), even one made after the list.
		if err := c.Docker.ImageRemove(ctx, img.ID); err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			c.log().Warn("cleanup: cannot remove a dangling image", "image", img.ID, "error", err)
			continue
		}
		res.Images++
		res.Bytes += img.Size
	}
	return res, nil
}

// Dangling is true for an image without any tag and without any digest name.
func Dangling(img docker.Image) bool {
	for _, t := range img.RepoTags {
		if t != "" && !strings.HasPrefix(t, "<none>") {
			return false
		}
	}
	for _, d := range img.RepoDigests {
		if d != "" && !strings.HasPrefix(d, "<none>") {
			return false
		}
	}
	return true
}
