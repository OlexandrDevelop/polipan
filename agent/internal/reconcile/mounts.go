// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// Host folders of the mounts (C06, REQ-VOL-001, REQ-VOL-006, 10-security section 4).
const (
	DefaultBindsDir   = "/var/lib/polipan/binds"
	DefaultFilesDir   = "/var/lib/polipan/files"
	DefaultSecretsDir = "/run/polipan/secrets"
	maxFileMount      = 1 << 20 // 1 MiB
)

var (
	safeID     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	volumeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
	// dockerSockets are never mounted, also not through a symlink (REQ-TPL-005).
	dockerSockets = []string{"/var/run/docker.sock", "/run/docker.sock"}
)

// errMount is a mount that the agent refuses.
func errMount(format string, args ...any) error {
	return &codedErr{"container_start_failed", fmt.Errorf("mount refused: "+format, args...)}
}

func (e *Engine) bindsDir() string   { return or(e.BindsDir, DefaultBindsDir) }
func (e *Engine) filesDir() string   { return or(e.FilesDir, DefaultFilesDir) }
func (e *Engine) secretsDir() string { return or(e.SecretsDir, DefaultSecretsDir) }

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// cleanTarget checks the path in the container: absolute, clean and not the root.
func cleanTarget(t string) (string, error) {
	if !strings.HasPrefix(t, "/") || t == "/" || path.Clean(t) != t {
		return "", errMount("target %q must be a clean absolute path", t)
	}
	return t, nil
}

// prepareMounts checks the mounts of a workload, creates volumes and bind folders,
// writes file mounts and returns the Docker mounts.
func (e *Engine) prepareMounts(ctx context.Context, w *agentv1.WorkloadSpec) ([]docker.Mount, error) {
	var out []docker.Mount
	targets := map[string]bool{}
	for i, m := range w.Mounts {
		target, err := cleanTarget(m.Target)
		if err != nil {
			return nil, err
		}
		if targets[target] {
			return nil, errMount("two mounts use the target %q", target)
		}
		targets[target] = true
		switch m.Kind {
		case "volume", "":
			if !volumeName.MatchString(m.Source) {
				return nil, errMount("volume name %q is not valid", m.Source)
			}
			v, err := e.Docker.VolumeCreate(ctx, m.Source, map[string]string{LabelManaged: "true", LabelService: w.ServiceId})
			if err != nil {
				return nil, &codedErr{"volume_missing", err}
			}
			e.limit(m.Source, v.Mountpoint, m.SizeLimitBytes)
			out = append(out, docker.Mount{Type: "volume", Source: m.Source, Target: target, ReadOnly: m.ReadOnly})
		case "bind":
			dir, err := e.bindPath(w.ServiceId, m.Source, true)
			if err != nil {
				return nil, err
			}
			e.limit(dir, dir, m.SizeLimitBytes)
			out = append(out, docker.Mount{Type: "bind", Source: dir, Target: target, ReadOnly: m.ReadOnly})
		case "tmpfs":
			out = append(out, docker.Mount{Type: "tmpfs", Target: target, ReadOnly: m.ReadOnly, TmpfsSize: int64(m.SizeLimitBytes)})
		case "file":
			p, err := e.writeFile(w.PlacementId, i, m)
			if err != nil {
				return nil, err
			}
			out = append(out, docker.Mount{Type: "bind", Source: p, Target: target, ReadOnly: true})
		default:
			return nil, errMount("unknown kind %q", m.Kind)
		}
	}
	return out, nil
}

// bindPath returns the host folder of a bind mount. Only folders under <binds>/<service> are
// allowed: no "..", no symlink on the way, nothing that resolves outside. With create the
// missing folders are made.
func (e *Engine) bindPath(service, sub string, create bool) (string, error) {
	if !safeID.MatchString(service) {
		return "", errMount("service id %q is not valid", service)
	}
	root := filepath.Join(e.bindsDir(), service)
	rel := sub
	if filepath.IsAbs(sub) {
		if !strings.HasPrefix(sub, root+"/") && sub != root {
			return "", errMount("bind %q is outside %s", sub, root)
		}
		rel = strings.TrimPrefix(strings.TrimPrefix(sub, root), "/")
	}
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		switch p {
		case "", ".":
			continue
		case "..":
			return "", errMount("bind %q goes up with ..", sub)
		}
		parts = append(parts, p)
	}
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
	}
	cur := root
	for _, p := range append([]string{""}, parts...) {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist) && create:
			if err := os.Mkdir(cur, 0o755); err != nil {
				return "", err
			}
			continue
		case err != nil:
			return "", err
		case fi.Mode()&os.ModeSymlink != 0:
			return "", errMount("bind %q has a symlink at %s", sub, cur)
		case !fi.IsDir():
			return "", errMount("bind %q is not a folder", sub)
		}
	}
	// A second check on the resolved path, in case a folder above was changed.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+"/") {
		return "", errMount("bind %q resolves outside %s", sub, root)
	}
	for _, s := range dockerSockets {
		if real == s || strings.HasPrefix(s, real+"/") {
			return "", errMount("bind %q would expose the Docker socket", sub)
		}
	}
	return cur, nil
}

// writeFile writes the content of a file mount. A secret goes to a tmpfs folder with mode 0400,
// a normal file to the files folder with mode 0444. The content is never logged.
func (e *Engine) writeFile(placement string, i int, m *agentv1.Mount) (string, error) {
	if !safeID.MatchString(placement) {
		return "", errMount("placement id %q is not valid", placement)
	}
	name := m.Source
	if name == "" {
		name = path.Base(m.Target)
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return "", errMount("file name %q is not valid", name)
	}
	if len(m.Content) > maxFileMount {
		return "", errMount("file %q is larger than 1 MiB", name)
	}
	base, dirMode, mode := e.filesDir(), os.FileMode(0o755), os.FileMode(0o444)
	if m.Secret {
		base, dirMode, mode = e.secretsDir(), 0o700, 0o400
		if err := os.MkdirAll(base, 0o700); err != nil {
			return "", err
		}
		check := e.CheckTmpfs
		if check == nil {
			check = isTmpfs
		}
		if err := check(base); err != nil {
			return "", &codedErr{"container_start_failed", err}
		}
	}
	dir := filepath.Join(base, placement)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("%d-%s", i, name))
	tmp := p + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, m.Content, 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return p, nil
}

// removeFiles deletes the file mounts of a placement (secret files too).
func (e *Engine) removeFiles(placement string) {
	if !safeID.MatchString(placement) {
		return
	}
	for _, base := range []string{e.filesDir(), e.secretsDir()} {
		if err := os.RemoveAll(filepath.Join(base, placement)); err != nil {
			e.Log.Warn("cannot remove file mounts", "placement", placement, "error", err)
		}
	}
}

// removeVolumes deletes the Docker volumes of a service. Only RemoveWorkload with
// remove_volumes does this: volumes stay over redeploys (AC-VOL-01).
func (e *Engine) removeVolumes(ctx context.Context, service string) ([]string, error) {
	if service == "" {
		return nil, nil
	}
	vs, err := e.Docker.VolumeList(ctx, map[string]string{LabelManaged: "true", LabelService: service})
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, v := range vs {
		if err := e.Docker.VolumeRemove(ctx, v.Name); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return removed, err
		}
		removed = append(removed, v.Name)
	}
	return removed, nil
}

// volumeInUseErr is the refusal of the volume guard (08 section 7a). Source is the volume name or
// "bind:<sub path>", never a host path. The text is the agent error contract as JSON.
type volumeInUseErr struct {
	Source                 string `json:"source"`
	ConflictingContainerID string `json:"conflicting_container_id"`
	PlacementID            string `json:"placement_id"`
}

func (v *volumeInUseErr) Error() string {
	b, _ := json.Marshal(struct {
		Code string `json:"code"`
		*volumeInUseErr
	}{"volume_in_use", v})
	return string(b)
}

// guarded is one writable single-writer source of a workload.
type guarded struct {
	label  string // what the error shows
	volume string // volume name, empty for a bind
	path   string // host path of the data, empty when not known
}

// guardMounts refuses to start w while another container holds one of its writable single-writer
// sources: running, restarting, paused, or stopped with a restart policy that Docker may use
// (REQ-VOL-002, 08 section 7a). Every container of the host counts, also the ones that Polipan does
// not manage. A Polipan container of an old release (role stopped, or not in the desired state) gets
// the policy "no" and is stopped; any other holder is never touched and the start is refused.
// self is the id of the container of w, "" for a new one. Call it after prepareMounts.
func (e *Engine) guardMounts(ctx context.Context, w *agentv1.WorkloadSpec, self string) error {
	l, ok := e.Docker.(docker.MountLister)
	if !ok {
		return nil // only test fakes: the real client is a MountLister
	}
	var want []guarded
	for _, m := range w.Mounts {
		if m.ReadOnly || m.Shared {
			continue // Docker mounts it read-only, or the user declared it safe for more writers
		}
		switch m.Kind {
		case "volume", "":
			g := guarded{label: m.Source, volume: m.Source}
			if v, err := e.Docker.VolumeInspect(ctx, m.Source); err == nil {
				g.path = v.Mountpoint
			} else if !errors.Is(err, docker.ErrNotFound) {
				return err
			}
			want = append(want, g)
		case "bind":
			dir, err := e.bindPath(w.ServiceId, m.Source, false)
			if err != nil {
				return err
			}
			want = append(want, guarded{label: "bind:" + m.Source, path: dir})
		}
	}
	if len(want) == 0 {
		return nil
	}
	cs, err := l.MountedContainers(ctx)
	if err != nil {
		return err // no inventory, no start
	}
	for _, c := range cs {
		if c.ID == self || c.Name == w.ContainerName {
			continue
		}
		g, held := holds(c.Mounts, want)
		if !held {
			continue
		}
		active := c.State == "running" || c.State == "restarting" || c.State == "paused"
		if !active {
			ic, err := e.Docker.Inspect(ctx, c.ID)
			if errors.Is(err, docker.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if ic.RestartPolicy == "" || ic.RestartPolicy == "no" {
				continue // stopped and Docker does not start it again
			}
		}
		if old, grace := e.oldRelease(c.Name, c.Labels); old {
			e.Log.Warn("stopping an old container that holds a volume of the workload", "container", c.Name, "placement", w.PlacementId)
			if err := e.retire(ctx, c.ID, c.Labels[LabelPlacement], active, grace); err != nil {
				return err
			}
			continue
		}
		return &volumeInUseErr{Source: g.label, ConflictingContainerID: shortID(c.ID), PlacementID: w.PlacementId}
	}
	return nil
}

// holds tells whether one writable mount of a container is one of the guarded sources. Host paths
// match when one is inside the other: a bind of a parent folder holds the data too.
func holds(ms []docker.MountUse, want []guarded) (guarded, bool) {
	for _, m := range ms {
		if !m.RW {
			continue
		}
		for _, g := range want {
			if g.volume != "" && m.Type == "volume" && m.Name == g.volume {
				return g, true
			}
			if g.path != "" && m.Source != "" && pathOverlap(filepath.Clean(m.Source), filepath.Clean(g.path)) {
				return g, true
			}
		}
	}
	return guarded{}, false
}

func pathOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") || a == "/" || b == "/"
}

// oldRelease tells whether a container is a Polipan container that must not run: its placement has
// the role stopped or is not in the desired state. It returns the stop grace of the placement.
func (e *Engine) oldRelease(name string, labels map[string]string) (bool, int) {
	if labels[LabelManaged] != "true" || labels[LabelPlacement] == "" {
		return false, 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.desired == nil {
		return false, 0 // nothing is known: fail closed
	}
	for _, w := range e.desired.Workloads {
		if w.ContainerName == name {
			return w.Role == "stopped", int(w.StopGraceSeconds)
		}
	}
	return true, 0
}

// retire gives a container the restart policy "no" and stops it when it runs.
func (e *Engine) retire(ctx context.Context, id, placement string, running bool, grace int) error {
	if err := e.Docker.SetRestartPolicy(ctx, id, "no"); err != nil {
		return err
	}
	e.mu.Lock()
	e.noRestart[id] = true
	e.mu.Unlock()
	if !running {
		return nil
	}
	e.closeExecs(placement)
	if grace <= 0 {
		grace = 30
	}
	if err := e.Docker.Stop(ctx, id, grace); err != nil && !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
