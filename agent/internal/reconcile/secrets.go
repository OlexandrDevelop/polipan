// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/polipan/polipan/agent/internal/docker"
	"github.com/polipan/polipan/proto/agentv1"
)

// PullAuthFor is pullAuth for other packages (the backup runner pulls the image of a restore check).
func (e *Engine) PullAuthFor(ref string) (string, error) { return e.pullAuth(ref) }

// pullAuth returns the X-Registry-Auth value for a registry_auth_ref, or "" when ref is empty.
// The value holds the password: it goes only to Docker, never to a log.
func (e *Engine) pullAuth(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	e.mu.Lock()
	ds := e.desired
	e.mu.Unlock()
	for _, a := range ds.GetRegistryAuths() {
		if a.GetRef() == ref {
			return docker.RegistryAuth(a.GetServer(), a.GetUsername(), a.GetPassword()), nil
		}
	}
	return "", &codedErr{"image_pull_failed", fmt.Errorf("registry credentials %q are not in the desired state", ref)}
}

// secretValues returns the secret values of a desired state: env values in secret_env_keys,
// secret file content and registry passwords. Very short values are skipped (too many false hits).
func secretValues(ds *agentv1.DesiredState) []string {
	var out []string
	add := func(v string) {
		if len(v) >= 4 {
			out = append(out, v)
		}
	}
	for _, w := range ds.GetWorkloads() {
		for _, k := range w.SecretEnvKeys {
			add(w.Env[k])
		}
		for _, m := range w.Mounts {
			if m.Kind == "file" && m.Secret {
				add(string(m.Content))
			}
		}
	}
	for _, a := range ds.GetRegistryAuths() {
		add(a.GetPassword())
		if a.GetPassword() != "" {
			add(docker.RegistryAuth(a.GetServer(), a.GetUsername(), a.GetPassword()))
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// scrub replaces every secret value in s with ***. It is used on every error text the agent logs
// or sends, so a Docker message that repeats an env value or the inspect output does not leak it.
func (e *Engine) scrub(s string) string {
	e.mu.Lock()
	ds := e.desired
	e.mu.Unlock()
	for _, v := range secretValues(ds) {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}
