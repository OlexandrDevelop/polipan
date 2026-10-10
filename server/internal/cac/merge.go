// SPDX-License-Identifier: Apache-2.0

package cac

import (
	"encoding/json"
	"sort"
)

// The merge rules of 07 section 3:
//  1. services.<name> is the base.
//  2. environments.<env>.deploy is merged into the deploy block.
//  3. environments.<env>.services.<name> is merged over the result.
//
// Maps merge key by key. Lists and scalars replace. The map env merges key by key and each entry is one value.

// leafMaps are maps whose entries are values and not maps to merge deeper.
var leafMaps = map[string]bool{"env": true}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = deepCopy(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = deepCopy(x)
		}
		return out
	}
	return v
}

// mergeMaps returns base with over merged in. It changes neither input.
func mergeMaps(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(over))
	for k, v := range base {
		out[k] = deepCopy(v)
	}
	for k, v := range over {
		bm, bok := out[k].(map[string]any)
		om, ook := v.(map[string]any)
		switch {
		case bok && ook && leafMaps[k]:
			m := make(map[string]any, len(bm)+len(om))
			for kk, vv := range bm {
				m[kk] = vv
			}
			for kk, vv := range om {
				m[kk] = deepCopy(vv)
			}
			out[k] = m
		case bok && ook:
			out[k] = mergeMaps(bm, om)
		default:
			out[k] = deepCopy(v)
		}
	}
	return out
}

// Effective is the service of one environment after the merge.
type Effective struct {
	Env     string
	Name    string
	Raw     map[string]any // the merged keys. A key here is a field that the file sets.
	Service Service
	// Disabled is true when an override has enabled: false.
	Disabled bool
	// DeployFromEnv lists the deploy keys that came from environments.<env>.deploy only.
	DeployFromEnv map[string]bool
}

// EffectiveService merges the service name for the environment env. It returns false if the file has no such service.
func (f *File) EffectiveService(env, name string) (*Effective, bool) {
	baseRaw, ok := f.Services[name]
	if !ok {
		return nil, false
	}
	merged := deepCopy(baseRaw).(map[string]any)
	eff := &Effective{Env: env, Name: name, DeployFromEnv: map[string]bool{}}
	if e := f.Environments[env]; e != nil {
		if len(e.Deploy) > 0 {
			baseDeploy, _ := merged["deploy"].(map[string]any)
			for k := range e.Deploy {
				if _, set := baseDeploy[k]; !set {
					eff.DeployFromEnv[k] = true
				}
			}
			merged = mergeMaps(merged, map[string]any{"deploy": map[string]any(e.Deploy)})
		}
		if ov, ok := e.Services[name]; ok {
			if v, set := ov["enabled"].(bool); set && !v {
				eff.Disabled = true
			}
			ovDeploy, _ := ov["deploy"].(map[string]any)
			for k := range ovDeploy {
				delete(eff.DeployFromEnv, k)
			}
			merged = mergeMaps(merged, ov)
			fixSource(merged, baseRaw, ov)
		}
	}
	delete(merged, "enabled")
	eff.Raw = merged
	raw, _ := json.Marshal(merged)
	_ = json.Unmarshal(raw, &eff.Service)
	return eff, true
}

// fixSource keeps one kind of source. If both git and image are there after the merge, the one that the override sets wins.
func fixSource(merged, base, ov map[string]any) {
	src, _ := merged["source"].(map[string]any)
	if src == nil || src["git"] == nil || src["image"] == nil {
		return
	}
	ovSrc, _ := ov["source"].(map[string]any)
	if ovSrc["image"] != nil {
		delete(src, "git")
	} else {
		delete(src, "image")
	}
}

// sortedKeys returns the keys of a map in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
