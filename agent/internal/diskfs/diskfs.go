// SPDX-License-Identifier: Apache-2.0

// Package diskfs measures the file systems that matter on a node: the root, the Docker data-root and the BuildKit
// state (12-observability section 7.1). Paths on one file system are measured once.
package diskfs

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The roles of a path.
const (
	RoleRoot     = "root"
	RoleDocker   = "docker"
	RoleBuildKit = "buildkit"
)

// FS is one file system with the roles of the paths on it.
type FS struct {
	Mount string   // mount point, for example "/" or "/var/lib/docker"
	Roles []string // sorted, for example ["buildkit","docker","root"]
	Used  uint64   // bytes in use
	Avail uint64   // bytes free for a normal user (without the root reserve)
	Size  uint64   // size of the file system
}

// Percent is the used share like df shows it: used / (used + avail).
func (f FS) Percent() float64 {
	if f.Used+f.Avail == 0 {
		return 0
	}
	return float64(f.Used) / float64(f.Used+f.Avail) * 100
}

// RoleList is the roles joined with commas.
func (f FS) RoleList() string { return strings.Join(f.Roles, ",") }

// Docker is the part of the Docker client that finds the paths.
type Docker interface {
	RootDir(ctx context.Context) (string, error)
	BuildKitRoot(ctx context.Context) (string, error)
}

// Paths finds the path of each role. The Docker answers are kept for a few minutes.
type Paths struct {
	Docker Docker
	// BuildKitRoot overrides the BuildKit state path (POLIPAN_AGENT_BUILDKIT_ROOT). Empty = the mount of the
	// buildkitd container, when this node runs one.
	BuildKitRoot string

	mu   sync.Mutex
	at   time.Time
	last map[string]string
}

const pathsFor = 5 * time.Minute

// Get returns role -> path. The root is always there.
func (p *Paths) Get(ctx context.Context) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last != nil && time.Since(p.at) < pathsFor {
		return p.last
	}
	out := map[string]string{RoleRoot: "/"}
	if p.Docker != nil {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if d, err := p.Docker.RootDir(dctx); err == nil && d != "" {
			out[RoleDocker] = d
		}
		if p.BuildKitRoot == "" {
			if b, err := p.Docker.BuildKitRoot(dctx); err == nil && b != "" {
				out[RoleBuildKit] = b
			}
		}
		cancel()
	}
	if p.BuildKitRoot != "" {
		out[RoleBuildKit] = p.BuildKitRoot
	}
	p.last, p.at = out, time.Now()
	return out
}

// Measure measures the file systems of the paths (role -> path). Paths on the same device give one FS.
// A path that cannot be read is skipped.
func Measure(paths map[string]string) []FS {
	byDev := map[uint64]*FS{}
	var order []uint64
	roles := make([]string, 0, len(paths))
	for r := range paths {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	for _, role := range roles {
		path := filepath.Clean(paths[role])
		dev, err := deviceOf(path)
		if err != nil {
			continue
		}
		if f, ok := byDev[dev]; ok {
			f.Roles = append(f.Roles, role)
			continue
		}
		used, avail, size, err := usage(path)
		if err != nil {
			continue
		}
		byDev[dev] = &FS{Mount: mountOf(path, dev), Roles: []string{role}, Used: used, Avail: avail, Size: size}
		order = append(order, dev)
	}
	out := make([]FS, 0, len(order))
	for _, d := range order {
		f := byDev[d]
		sort.Strings(f.Roles)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

// mountOf walks up from path while the device stays the same. The last folder is the mount point.
func mountOf(path string, dev uint64) string {
	cur := path
	for cur != "/" {
		parent := filepath.Dir(cur)
		d, err := deviceOf(parent)
		if err != nil || d != dev {
			return cur
		}
		cur = parent
	}
	return "/"
}
