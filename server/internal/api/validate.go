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
		if s, _ := v.(string); s != "recreate" {
			return nil, errors.New("config.strategy must be recreate in this version")
		}
	} else {
		cfg["strategy"] = "recreate"
	}
	if v, ok := cfg["health"]; ok {
		h, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("config.health must be an object")
		}
		if err := checkHealth(h); err != nil {
			return nil, err
		}
	} else if port > 0 {
		cfg["health"] = map[string]any{"type": "tcp", "port": port}
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func checkHealth(h map[string]any) error {
	for k, v := range h {
		switch k {
		case "type":
			if s, _ := v.(string); s != "tcp" {
				return errors.New("config.health.type must be tcp in this version")
			}
		case "port":
			p, ok := asPort(v)
			if !ok {
				return errors.New("config.health.port must be a number from 1 to 65535")
			}
			h[k] = p
		case "deploy_timeout":
			n, ok := v.(float64)
			if !ok || n < 1 || n > 3600 {
				return errors.New("config.health.deploy_timeout must be a number of seconds from 1 to 3600")
			}
		default:
			return fmt.Errorf("config.health.%s is not available in this version", k)
		}
	}
	if _, ok := h["type"]; !ok {
		h["type"] = "tcp"
	}
	if _, ok := h["port"]; !ok {
		return errors.New("config.health.port is required")
	}
	return nil
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
