// SPDX-License-Identifier: Apache-2.0

package logship

import (
	"encoding/json"
	"strings"
)

// guessLevel returns debug, info, warn, error or unknown (REQ-OBS-001). It looks at the key level of a JSON
// line first, then at the start of a plain line.
func guessLevel(line string) string {
	s := strings.TrimSpace(line)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			for _, k := range []string{"level", "lvl", "severity", "levelname", "log_level"} {
				switch v := m[k].(type) {
				case string:
					if l := normalizeLevel(v); l != "" {
						return l
					}
				case float64: // pino and bunyan use numbers
					switch {
					case v >= 50:
						return "error"
					case v >= 40:
						return "warn"
					case v >= 30:
						return "info"
					case v > 0:
						return "debug"
					}
				}
			}
		}
		return "unknown"
	}
	// The level is the first word, or the word after a date and a time: "2025-01-02 10:00:00 ERROR ...".
	toks := strings.Fields(s)
	for i := 0; i < len(toks) && i < 3; i++ {
		t := strings.Trim(toks[i], "[]():<>|-")
		t = strings.TrimPrefix(t, "level=") // logfmt
		if j := strings.IndexByte(t, '['); j > 0 {
			t = t[:j] // logrus: INFO[0000]
		}
		if i > 0 && !isTimeLike(toks[i-1]) {
			break
		}
		if l := normalizeLevel(t); l != "" {
			return l
		}
		if !isTimeLike(toks[i]) {
			break
		}
	}
	return "unknown"
}

func isTimeLike(t string) bool { return t != "" && t[0] >= '0' && t[0] <= '9' }

func normalizeLevel(v string) string {
	switch strings.ToLower(strings.TrimSpace(strings.TrimSuffix(v, ":"))) {
	case "error", "err", "fatal", "panic", "critical", "crit", "severe", "emerg", "alert":
		return "error"
	case "warn", "warning":
		return "warn"
	case "info", "notice", "information":
		return "info"
	case "debug", "trace", "verbose", "dbg":
		return "debug"
	}
	return ""
}
