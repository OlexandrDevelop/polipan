// SPDX-License-Identifier: Apache-2.0

// Package logging creates the structured JSON logger (REQ-CORE-034).
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type ctxKey struct{}

// secretKeys are parts of attribute names whose values are never written.
var secretKeys = []string{"password", "secret", "token", "authorization", "cookie", "private_key", "kek", "dek"}

// New creates a JSON logger that masks values of secret-looking keys.
func New(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if isSecretKey(a.Key) {
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	})
	return slog.New(h)
}

func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range secretKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// WithLogger stores a logger in the context.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the logger of the context, or the default logger.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
