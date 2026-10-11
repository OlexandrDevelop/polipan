// SPDX-License-Identifier: Apache-2.0

package reconcile

import (
	"github.com/polipan/polipan/agent/internal/metrics"
	"github.com/polipan/polipan/proto/agentv1"
)

// Desired returns the desired state that the agent holds, or nil.
func (e *Engine) Desired() *agentv1.DesiredState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.desired
}

// HealthSamples gives the last health result of every replica that has a health check (for the metrics).
func (e *Engine) HealthSamples() []metrics.HealthSample {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.desired == nil {
		return nil
	}
	var out []metrics.HealthSample
	for _, w := range e.desired.Workloads {
		s := e.hs[w.PlacementId]
		if s == nil || s.checked.IsZero() {
			continue
		}
		replica := w.Labels["polipan.replica"]
		out = append(out, metrics.HealthSample{Service: w.ServiceId, Release: w.ReleaseId, Replica: replica, Up: s.health == "healthy", DurationSeconds: s.duration.Seconds()})
	}
	return out
}

// VolumeSamples gives the last usage of the volumes, with the service that mounts each one (for the metrics).
func (e *Engine) VolumeSamples() []metrics.VolumeSample {
	e.mu.Lock()
	ds, usage := e.desired, e.usage
	e.mu.Unlock()
	if ds == nil || len(usage) == 0 {
		return nil
	}
	byName := map[string]*agentv1.VolumeUsage{}
	for _, u := range usage {
		byName[u.Name] = u
	}
	var out []metrics.VolumeSample
	seen := map[string]bool{}
	for _, w := range ds.Workloads {
		for _, m := range w.Mounts {
			name := m.Source
			if m.Kind == "bind" {
				d, err := e.bindPath(w.ServiceId, m.Source, false)
				if err != nil {
					continue
				}
				name = d
			} else if m.Kind != "volume" && m.Kind != "" {
				continue
			}
			u := byName[name]
			if u == nil || seen[w.ServiceId+"/"+name] {
				continue
			}
			seen[w.ServiceId+"/"+name] = true
			out = append(out, metrics.VolumeSample{Service: w.ServiceId, Volume: name, UsedBytes: u.UsedBytes, UsedInodes: u.UsedInodes})
		}
	}
	return out
}

// SecretValues returns the secret values of the desired state, for the masking of shipped logs (REQ-SEC-007).
func (e *Engine) SecretValues() []string {
	e.mu.Lock()
	ds := e.desired
	e.mu.Unlock()
	return secretValues(ds)
}
