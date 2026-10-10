// SPDX-License-Identifier: Apache-2.0

// Package logging creates the structured JSON logger of the agent (REQ-CORE-034).
// Log lines of a command carry the command_id.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// secretKeys are parts of attribute names whose values are never written.
var secretKeys = []string{"password", "secret", "token", "private_key", "key_pem", "csr"}

// New creates a JSON logger that masks values of secret-looking keys.
func New(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			k := strings.ToLower(a.Key)
			for _, s := range secretKeys {
				if strings.Contains(k, s) {
					return slog.String(a.Key, "[redacted]")
				}
			}
			return a
		},
	}))
}

// ForCommand returns a logger that adds the command id to every line.
func ForCommand(l *slog.Logger, commandID string) *slog.Logger {
	return l.With("command_id", commandID)
}
