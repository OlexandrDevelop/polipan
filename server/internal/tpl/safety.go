// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"fmt"
	"path"
	"strings"
)

// BindsRoot is the only place on the host that a bind mount may use (REQ-TPL-005).
const BindsRoot = "/var/lib/polipan/binds"

// SafetyOfManifest lists what is not allowed in the services of a raw template document (REQ-TPL-005):
// privileged, cap_add except NET_BIND_SERVICE, network_mode host, pid host, devices, the Docker
// socket, and bind mounts outside /var/lib/polipan/binds/<service>.
func SafetyOfManifest(doc map[string]any) Problems {
	var out Problems
	services, _ := doc["services"].(map[string]any)
	for _, name := range sortedKeys(services) {
		if svc, ok := services[name].(map[string]any); ok {
			out = append(out, serviceViolations("services."+name, name, svc)...)
		}
	}
	return out
}

// SafetyOfCompose is the same check for the services of a Compose file.
func SafetyOfCompose(doc map[string]any) Problems {
	var out Problems
	services, _ := doc["services"].(map[string]any)
	for _, name := range sortedKeys(services) {
		if svc, ok := services[name].(map[string]any); ok {
			out = append(out, serviceViolations("services."+name, name, svc)...)
		}
	}
	return out
}

func serviceViolations(prefix, service string, svc map[string]any) Problems {
	var out Problems
	bad := func(key, format string, a ...any) {
		out = append(out, Problem{Path: prefix + "." + key, Message: fmt.Sprintf(format, a...)})
	}
	if v, ok := svc["privileged"]; ok && truthy(v) {
		bad("privileged", "privileged containers are not allowed")
	}
	if caps, ok := svc["cap_add"]; ok {
		list, _ := caps.([]any)
		for _, c := range list {
			if s := strings.TrimPrefix(strings.ToUpper(fmt.Sprint(c)), "CAP_"); s != "NET_BIND_SERVICE" {
				bad("cap_add", "capability %s is not allowed (only NET_BIND_SERVICE)", fmt.Sprint(c))
			}
		}
		if list == nil {
			bad("cap_add", "cap_add must be a list")
		}
	}
	if v, ok := svc["network_mode"]; ok && fmt.Sprint(v) != "" && fmt.Sprint(v) != "bridge" && fmt.Sprint(v) != "default" {
		if strings.EqualFold(fmt.Sprint(v), "host") {
			bad("network_mode", "network_mode host is not allowed")
		} else {
			bad("network_mode", "network_mode %v is not allowed", v)
		}
	}
	if v, ok := svc["pid"]; ok && fmt.Sprint(v) != "" {
		bad("pid", "pid %v is not allowed", v)
	}
	if v, ok := svc["ipc"]; ok && strings.EqualFold(fmt.Sprint(v), "host") {
		bad("ipc", "ipc host is not allowed")
	}
	if v, ok := svc["devices"]; ok {
		if list, _ := v.([]any); len(list) > 0 || list == nil {
			bad("devices", "devices are not allowed")
		}
	}
	if v, ok := svc["security_opt"]; ok {
		if list, _ := v.([]any); len(list) > 0 {
			bad("security_opt", "security_opt is not allowed")
		}
	}
	if v, ok := svc["volumes"]; ok {
		list, _ := v.([]any)
		for i, item := range list {
			out = append(out, volumeViolations(fmt.Sprintf("%s.volumes[%d]", prefix, i), service, item)...)
		}
	}
	return out
}

func volumeViolations(where, service string, item any) Problems {
	var out Problems
	bad := func(format string, a ...any) {
		out = append(out, Problem{Path: where, Message: fmt.Sprintf(format, a...)})
	}
	switch v := item.(type) {
	case string: // compose short syntax: source:target[:mode]
		parts := strings.Split(v, ":")
		if len(parts) >= 2 {
			if msg := hostSourceViolation(service, parts[0]); msg != "" {
				bad("%s", msg)
			}
		}
		for _, p := range parts {
			if strings.Contains(p, "docker.sock") {
				bad("the Docker socket cannot be mounted")
				break
			}
		}
	case map[string]any:
		typ := fmt.Sprint(v["type"])
		if kind, ok := v["kind"]; ok { // template volume
			typ = fmt.Sprint(kind)
			if typ == "bind" {
				name := fmt.Sprint(v["name"])
				if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
					bad("a bind volume name must be a folder name inside %s/%s", BindsRoot, service)
				}
			}
		}
		if src, ok := v["source"]; ok && typ == "bind" {
			if msg := hostSourceViolation(service, fmt.Sprint(src)); msg != "" {
				bad("%s", msg)
			}
		}
		for _, key := range []string{"source", "path", "target", "name"} {
			if strings.Contains(fmt.Sprint(v[key]), "docker.sock") {
				bad("the Docker socket cannot be mounted")
				break
			}
		}
	}
	return out
}

// hostSourceViolation checks the source of a compose volume. A named volume is fine. A path must
// be inside the binds folder of the service.
func hostSourceViolation(service, src string) string {
	if src == "" || (!strings.HasPrefix(src, "/") && !strings.HasPrefix(src, ".") && !strings.HasPrefix(src, "~")) {
		return "" // a named volume
	}
	clean := path.Clean(src)
	root := BindsRoot + "/" + service
	if strings.HasPrefix(src, "/") && (clean == root || strings.HasPrefix(clean, root+"/")) {
		return ""
	}
	return fmt.Sprintf("bind mount %s is not allowed (only folders under %s)", src, root)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1" || strings.EqualFold(x, "yes")
	case int:
		return x != 0
	}
	return false
}
