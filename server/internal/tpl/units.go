// SPDX-License-Identifier: Apache-2.0

package tpl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var durRe = regexp.MustCompile(`^([0-9]+)(ms|s|m|h|d)$`)

// Seconds reads a duration like 30s, 5m, 24h or 7d as whole seconds (07 section 4).
func Seconds(s string) (int, error) {
	m := durRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a duration like 30s, 5m or 24h", s)
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "ms":
		return (n + 999) / 1000, nil
	case "m":
		return n * 60, nil
	case "h":
		return n * 3600, nil
	case "d":
		return n * 86400, nil
	}
	return n, nil
}

var sizeRe = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti)$`)

// Size reads 512Mi like sizes as bytes.
func Size(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a size like 512Mi or 2Gi", s)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	mult := map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}[m[2]]
	return n * mult, nil
}

// healthConfig maps a polipan.yaml healthcheck to the config of a service (units to whole seconds).
func healthConfig(h map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range h {
		switch k {
		case "type", "path":
			out[k] = v
		case "port":
			out[k] = toInt(v)
		case "host_header":
			out["host"] = v
		case "command":
			out[k] = v
		case "expected_status":
			s := fmt.Sprint(v)
			lo, hi, ok := strings.Cut(s, "-")
			l, _ := strconv.Atoi(lo)
			if !ok {
				out[k] = []any{l}
			} else {
				h, _ := strconv.Atoi(hi)
				out[k] = []any{l, h}
			}
		case "interval", "timeout", "start_period", "deploy_timeout":
			secs, err := Seconds(fmt.Sprint(v))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = secs
		case "healthy_threshold", "unhealthy_threshold":
			out[k] = toInt(v)
		case "restart_on_unhealthy":
			out[k] = v
		default:
			return nil, fmt.Errorf("%s is not a healthcheck key", k)
		}
	}
	return out, nil
}

// resourcesConfig maps cpu "500m" or 0.5 and memory "512Mi" to cpu_millis and memory_bytes.
func resourcesConfig(r map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k, v := range r {
		switch k {
		case "cpu":
			s := fmt.Sprint(v)
			if n, ok := strings.CutSuffix(s, "m"); ok {
				ms, err := strconv.Atoi(n)
				if err != nil {
					return nil, fmt.Errorf("cpu %q is not valid", s)
				}
				out["cpu_millis"] = ms
			} else {
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return nil, fmt.Errorf("cpu %q is not valid", s)
				}
				out["cpu_millis"] = int(f * 1000)
			}
		case "memory":
			n, err := Size(fmt.Sprint(v))
			if err != nil {
				return nil, err
			}
			out["memory_bytes"] = n
		case "memory_reservation":
		default:
			return nil, fmt.Errorf("%s is not a resources key", k)
		}
	}
	return out, nil
}

// deployConfig maps the deploy block into cfg.
func deployConfig(cfg map[string]any, d map[string]any) error {
	opts := map[string]any{}
	for k, v := range d {
		switch k {
		case "strategy":
			if s := fmt.Sprint(v); s == "recreate" || s == "blue_green" {
				cfg["strategy"] = s
			} else {
				return fmt.Errorf("strategy %s is not available in this version", s)
			}
		case "drain_seconds", "allow_overlap", "allow_downtime_on_low_capacity":
			opts[k] = v
		case "retain_stopped":
			cfg[k] = toInt(v)
		case "pre_deploy", "post_deploy":
			cfg[k] = v
		default:
			return fmt.Errorf("deploy.%s is not available in templates", k)
		}
	}
	if len(opts) > 0 {
		cfg["strategy_options"] = opts
	}
	return nil
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	n, _ := strconv.Atoi(fmt.Sprint(v))
	return n
}
