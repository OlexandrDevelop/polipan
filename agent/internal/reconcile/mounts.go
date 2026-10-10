// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"context"
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
