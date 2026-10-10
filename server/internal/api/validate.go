// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

var (
	slugRe        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	serviceNameRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	nodeNameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	hostLabelRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	imageRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-/:@]*$`)
	emailRe       = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// slugify makes a slug from a name: lower case letters, digits and single dashes.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// validName checks a display name: 1 to 100 characters after trimming.
func validName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && len([]rune(name)) <= 100
}

// normalizeHost lower-cases a host and checks the syntax (REQ-DOM-004, REQ-DOM-007).
// A wildcard, an IP address, a port or a path is refused.
func normalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case host == "":
		return "", errors.New("host is required")
	case strings.Contains(host, "*"):
		return "", errors.New("wildcard hosts are not allowed")
	case len(host) > 253:
		return "", errors.New("host is too long")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", errors.New("host must be a domain name, not an IP address")
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 {
		return "", errors.New("host must have at least two labels, for example app.example.com")
	}
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return "", fmt.Errorf("host label %q is not valid", l)
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", errors.New("the last label of the host must not be only digits")
	}
	return strings.Join(labels, "."), nil
}

// validPathPrefix checks a path prefix like / or /api.
func validPathPrefix(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 200 {
		return false
	}
	for _, r := range p {
		if r <= ' ' || r == '?' || r == '#' || r == 0x7f {
			return false
		}
	}
	return true
}

// imageConfigKeys are the keys of the config of an image service in M1.
var imageConfigKeys = map[string]bool{
	"image": true, "port": true, "command": true, "entrypoint": true,
	"restart_policy": true, "health": true, "strategy": true,
	"files": true, "secret_files": true, // M2: file mounts and secret files
	// M3: deploy settings
	"host_port": true, "resources": true, "pre_deploy": true, "post_deploy": true,
	"stop_grace_period": true, "retain_stopped": true, "strategy_options": true,
	// M7: many servers
	"replicas": true, "placement": true,
}

// buildImageConfig checks the config of an image service and fills the defaults.
// source may carry the image too ({"image": "nginx:1.27"}). Only image is required.
func buildImageConfig(config, source map[string]any) (json.RawMessage, error) {
	cfg := map[string]any{}
	for k, v := range config {
		cfg[k] = v
	}
	if source != nil {
		for k := range source {
			if k != "image" && k != "type" {
				return nil, fmt.Errorf("source.%s is not available for image services in this version", k)
			}
		}
		if t, ok := source["type"]; ok && t != "image" {
			return nil, errors.New("only image services can be created in this version")
		}
		if img, ok := source["image"]; ok {
			if _, dup := cfg["image"]; !dup {
				cfg["image"] = img
			}
		}
	}
	return checkImageConfig(cfg)
}

func checkImageConfig(cfg map[string]any) (json.RawMessage, error) {
	for k := range cfg {
		if !imageConfigKeys[k] {
			return nil, fmt.Errorf("config.%s is not available in this version", k)
		}
	}
	image, _ := cfg["image"].(string)
	if image == "" {
		return nil, errors.New("config.image is required")
	}
	if len(image) > 255 || !imageRe.MatchString(image) {
		return nil, errors.New("config.image is not a valid image reference")
	}
	port := 0
	if v, ok := cfg["port"]; ok {
		p, ok := asPort(v)
		if !ok {
			return nil, errors.New("config.port must be a number from 1 to 65535")
		}
		port = p
		cfg["port"] = p
	}
	for _, k := range []string{"command", "entrypoint"} {
		if v, ok := cfg[k]; ok {
			list, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("config.%s must be a list of strings", k)
			}
			for _, item := range list {
				if _, ok := item.(string); !ok {
					return nil, fmt.Errorf("config.%s must be a list of strings", k)
				}
			}
		}
	}
	if err := checkMountConfig(cfg); err != nil {
		return nil, err
	}
	if v, ok := cfg["restart_policy"]; ok {
		s, _ := v.(string)
		if s != "always" && s != "on-failure" && s != "unless-stopped" && s != "no" {
			return nil, errors.New("config.restart_policy must be always, on-failure, unless-stopped or no")
		}
	} else {
		cfg["restart_policy"] = "always"
	}
	if v, ok := cfg["strategy"]; ok {
		if s, _ := v.(string); s != "recreate" && s != "blue_green" {
			return nil, errors.New("config.strategy must be recreate or blue_green in this version")
		}
	}
	if err := checkDeployConfig(cfg); err != nil {
		return nil, err
	}
	if v, ok := cfg["health"]; ok {
		h, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("config.health must be an object")
		}
		if err := checkHealth(h, port); err != nil {
			return nil, err
		}
	} else if port > 0 {
		cfg["health"] = map[string]any{"type": "tcp", "port": port} // REQ-HC-003
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkDeployConfig checks the deploy keys of an image config (M3).
func checkDeployConfig(cfg map[string]any) error {
	num := func(key string, lo, hi float64) error {
		if v, ok := cfg[key]; ok {
			n, ok := v.(float64)
			if !ok || n != float64(int(n)) || n < lo || n > hi {
				return fmt.Errorf("config.%s must be a whole number from %d to %d", key, int(lo), int(hi))
			}
		}
		return nil
	}
	if err := num("stop_grace_period", 0, 3600); err != nil {
		return err
	}
	if err := num("retain_stopped", 0, 10); err != nil {
		return err
	}
	if err := num("replicas", 0, 100); err != nil {
		return err
	}
	if err := checkPlacement(cfg); err != nil {
		return err
	}
	if v, ok := cfg["host_port"]; ok {
		p, ok := asPort(v)
		if !ok {
			return errors.New("config.host_port must be a number from 1 to 65535")
		}
		if err := checkHostPort(p); err != nil {
			return err
		}
		cfg["host_port"] = p
	}
	for _, k := range []string{"pre_deploy", "post_deploy"} {
		if v, ok := cfg[k]; ok {
			list, ok := v.([]any)
			if !ok {
				return fmt.Errorf("config.%s must be a list of strings", k)
			}
			for _, item := range list {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("config.%s must be a list of strings", k)
				}
			}
		}
	}
	if v, ok := cfg["resources"]; ok {
		r, ok := v.(map[string]any)
		if !ok {
			return errors.New("config.resources must be an object")
		}
		for k, v := range r {
			n, isNum := v.(float64)
			if (k != "cpu_millis" && k != "memory_bytes") || !isNum || n < 0 || n != float64(int64(n)) {
				return errors.New("config.resources can have cpu_millis and memory_bytes, whole numbers from 0")
			}
		}
	}
	if v, ok := cfg["strategy_options"]; ok {
		o, ok := v.(map[string]any)
		if !ok {
			return errors.New("config.strategy_options must be an object")
		}
		for k, v := range o {
			switch k {
			case "drain_seconds":
				n, ok := v.(float64)
				if !ok || n != float64(int(n)) || n < 0 || n > 3600 {
					return errors.New("config.strategy_options.drain_seconds must be a whole number from 0 to 3600")
				}
			case "allow_downtime_on_low_capacity", "allow_overlap":
				if _, ok := v.(bool); !ok {
					return fmt.Errorf("config.strategy_options.%s must be true or false", k)
				}
			default:
				return fmt.Errorf("config.strategy_options.%s is not available in this version", k)
			}
		}
	}
	return nil
}

// checkPlacement checks config.placement (REQ-NODE-004): node names, required labels and spread.
func checkPlacement(cfg map[string]any) error {
	v, ok := cfg["placement"]
	if !ok {
		return nil
	}
	p, ok := v.(map[string]any)
	if !ok {
		return errors.New("config.placement must be an object")
	}
	for k, v := range p {
		switch k {
		case "nodes":
			list, ok := v.([]any)
			if !ok || len(list) > 100 {
				return errors.New("config.placement.nodes must be a list of node names")
			}
			for _, item := range list {
				if s, ok := item.(string); !ok || !nodeNameRe.MatchString(s) {
					return errors.New("config.placement.nodes must be a list of node names")
				}
			}
		case "labels":
			m, ok := v.(map[string]any)
			if !ok || len(m) > 20 {
				return errors.New("config.placement.labels must be an object of text values (at most 20)")
			}
			for lk, lv := range m {
				if s, ok := lv.(string); !ok || lk == "" || len(lk) > 63 || len(s) > 63 {
					return errors.New("config.placement.labels must be an object of text values (at most 20)")
				}
			}
		case "spread":
			if _, ok := v.(bool); !ok {
				return errors.New("config.placement.spread must be true or false")
			}
		default:
			return fmt.Errorf("config.placement.%s is not available", k)
		}
	}
	return nil
}

// checkHealth checks config.health (F05, REQ-HC-001, REQ-HC-002). Times are whole seconds.
func checkHealth(h map[string]any, servicePort int) error {
	whole := func(k string, v any, lo, hi float64) error {
		n, ok := v.(float64)
		if !ok || n != float64(int(n)) || n < lo || n > hi {
			return fmt.Errorf("config.health.%s must be a whole number from %d to %d", k, int(lo), int(hi))
		}
		return nil
	}
	for k, v := range h {
		var err error
		switch k {
		case "type":
			if s, _ := v.(string); s != "tcp" && s != "http" && s != "command" {
				return errors.New("config.health.type must be tcp, http or command")
			}
		case "port":
			p, ok := asPort(v)
			if !ok {
				return errors.New("config.health.port must be a number from 1 to 65535")
			}
			h[k] = p
		case "path":
			if s, _ := v.(string); !validPathPrefix(s) {
				return errors.New("config.health.path must start with /")
			}
		case "host":
			if s, _ := v.(string); s == "" || len(s) > 253 || strings.ContainsAny(s, " \t\r\n/") {
				return errors.New("config.health.host is not a valid host name")
			}
		case "command":
			list, ok := v.([]any)
			if !ok || len(list) == 0 {
				return errors.New("config.health.command must be a list of strings")
			}
			for _, item := range list {
				if _, ok := item.(string); !ok {
					return errors.New("config.health.command must be a list of strings")
				}
			}
		case "expected_status":
			list, ok := v.([]any)
			if !ok || len(list) == 0 || len(list) > 2 {
				return errors.New("config.health.expected_status must be [status] or [from, to]")
			}
			for _, item := range list {
				n, ok := item.(float64)
				if !ok || n < 100 || n > 599 || n != float64(int(n)) {
					return errors.New("config.health.expected_status must hold HTTP statuses from 100 to 599")
				}
			}
		case "interval", "timeout":
			err = whole(k, v, 1, 3600)
		case "healthy_threshold", "unhealthy_threshold":
			err = whole(k, v, 1, 100)
		case "start_period":
			err = whole(k, v, 0, 3600)
		case "deploy_timeout":
			err = whole(k, v, 1, 3600)
		case "restart_on_unhealthy":
			if _, ok := v.(bool); !ok {
				return errors.New("config.health.restart_on_unhealthy must be true or false")
			}
		default:
			return fmt.Errorf("config.health.%s is not available in this version", k)
		}
		if err != nil {
			return err
		}
	}
	typ, _ := h["type"].(string)
	if typ == "" {
		typ = "tcp"
		h["type"] = typ
	}
	_, hasPort := h["port"]
	switch typ {
	case "tcp", "http":
		if !hasPort && servicePort == 0 {
			return errors.New("config.health.port is required when the service has no port")
		}
		if !hasPort {
			h["port"] = servicePort
		}
	case "command":
		if _, ok := h["command"]; !ok {
			return errors.New("config.health.command is required for a command check")
		}
	}
	return nil
}

// Host ports are from this range (C02 REQ-DB-005). Ports of the system and of the panel are never allowed.
const (
	HostPortMin = 30000
	HostPortMax = 39999
)

var reservedHostPorts = map[int]bool{22: true, 53: true, 80: true, 443: true, 9443: true}

func checkHostPort(p int) error {
	if reservedHostPorts[p] {
		return fmt.Errorf("config.host_port %d is reserved for the system", p)
	}
	if p < HostPortMin || p > HostPortMax {
		return fmt.Errorf("config.host_port must be from %d to %d", HostPortMin, HostPortMax)
	}
	return nil
}

// panelPortClash tells if a stored config publishes a port that the panel or the agent gateway uses.
func (s Settings) panelPortClash(raw json.RawMessage) error {
	var c struct {
		HostPort int `json:"host_port"`
	}
	if json.Unmarshal(raw, &c) != nil || c.HostPort == 0 {
		return nil
	}
	for _, addr := range []string{s.AgentListen, s.ExternalURL} {
		if i := strings.LastIndex(addr, ":"); i >= 0 && addr[i+1:] == fmt.Sprint(c.HostPort) {
			return fmt.Errorf("config.host_port %d is used by the panel", c.HostPort)
		}
	}
	return nil
}

// checkPublishedPort is the host port rule for config as code: the range and the reserved ports, and the ports of
// the panel and the agent gateway.
func (s Settings) checkPublishedPort(p int) error {
	if err := checkHostPort(p); err != nil {
		return err
	}
	return s.panelPortClash(json.RawMessage(fmt.Sprintf(`{"host_port":%d}`, p)))
}

func asPort(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int(f)) || f < 1 || f > 65535 {
		return 0, false
	}
	return int(f), true
}

// configPort returns the port of a stored service config, or 0.
func configPort(raw json.RawMessage) int {
	var c struct {
		Port int `json:"port"`
	}
	_ = json.Unmarshal(raw, &c)
	return c.Port
}
