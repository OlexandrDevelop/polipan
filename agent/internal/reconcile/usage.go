// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/polipan/polipan/proto/agentv1"
)

// usageEvery is how often the volume usage is measured (REQ-VOL-005).
const usageEvery = 60 * time.Second

// Quota sets a size limit on a folder. Apply returns false when the file system has no quota support.
type Quota interface {
	Apply(dir string, limitBytes uint64) (bool, error)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// limit enforces size_limit_bytes with an XFS project quota when the file system supports it
// (REQ-VOL-009). Otherwise the limit is only an alert threshold: the usage report has the numbers
// and the agent logs a warning when a volume is over its limit.
func (e *Engine) limit(name, dir string, limit uint64) {
	if limit == 0 || dir == "" {
		return
	}
	e.mu.Lock()
	done := e.quotas[name] == limit
	e.quotas[name] = limit
	e.mu.Unlock()
	if done {
		return
	}
	q := e.Quota
	if q == nil {
		q = xfsQuota{}
	}
	ok, err := q.Apply(dir, limit)
	switch {
	case err != nil:
		e.Log.Warn("cannot set the size limit, it is only an alert threshold", "volume", name, "error", err)
	case !ok:
		e.Log.Info("no quota support, the size limit is only an alert threshold", "volume", name)
	}
}

// volumeUsage returns the bytes and inodes of every volume and bind folder of the desired state.
// It measures at most every 60 s and returns the last numbers in between.
func (e *Engine) volumeUsage(ctx context.Context, ds *agentv1.DesiredState) []*agentv1.VolumeUsage {
	now := e.now()
	e.mu.Lock()
	due := e.usageAt.IsZero() || now.Sub(e.usageAt) >= usageEvery
	cached := e.usage
	e.mu.Unlock()
	if !due {
		return cached
	}
	var out []*agentv1.VolumeUsage
	seen := map[string]bool{}
	for _, w := range ds.Workloads {
		for _, m := range w.Mounts {
			kind, name, dir := m.Kind, "", ""
			switch m.Kind {
			case "volume", "":
				v, err := e.Docker.VolumeInspect(ctx, m.Source)
				if err != nil {
					continue
				}
				kind, name, dir = "volume", m.Source, v.Mountpoint
			case "bind":
				d, err := e.bindPath(w.ServiceId, m.Source, false)
				if err != nil {
					continue
				}
				name, dir = d, d
			default:
				continue
			}
			if seen[kind+"/"+name] || dir == "" {
				continue
			}
			seen[kind+"/"+name] = true
			b, n, err := diskUsage(dir)
			if err != nil {
				e.Log.Warn("cannot measure volume", "volume", name, "error", err)
				continue
			}
			if m.SizeLimitBytes > 0 && b > m.SizeLimitBytes {
				e.Log.Warn("volume is over its size limit", "volume", name, "used_bytes", b, "limit_bytes", m.SizeLimitBytes)
			}
			out = append(out, &agentv1.VolumeUsage{Name: name, Kind: kind, UsedBytes: b, UsedInodes: n, SampledUnixMs: now.UnixMilli()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name })
	e.mu.Lock()
	e.usage, e.usageAt = out, now
	e.mu.Unlock()
	return out
}

// diskUsage walks a folder and returns the used bytes (allocated blocks) and the number of inodes.
// Symlinks are not followed and a hard link is counted once.
func diskUsage(dir string) (bytes, inodes uint64, err error) {
	if _, err := os.Lstat(dir); err != nil {
		return 0, 0, err
	}
	seen := map[[2]uint64]bool{}
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip what cannot be read
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)} //nolint:unconvert // the types differ per OS
			if seen[key] {
				return nil
			}
			seen[key] = true
			bytes += uint64(st.Blocks) * 512
		} else {
			bytes += uint64(fi.Size())
		}
		inodes++
		return nil
	})
	return bytes, inodes, err
}
