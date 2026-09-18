// Package observability wires structured logging (slog), Prometheus metrics
// and OpenTelemetry tracing. All metric vectors of the server are defined in
// the Metrics struct so that label sets stay consistent across packages.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Log formats accepted by NewLogger.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// RedactedValue replaces the value of sensitive log attributes.
const RedactedValue = "[REDACTED]"

// sensitiveKeys lists attribute keys (compared case-insensitively) whose values
// are never written to logs. It is read-only after initialization.
var sensitiveKeys = map[string]struct{}{
	"password":        {},
	"token":           {},
	"secret":          {},
	"authorization":   {},
	"cookie":          {},
	"payload":         {},
	"url_credentials": {},
}

// IsSensitiveKey reports whether an attribute key is redacted by loggers
// created with NewLogger.
func IsSensitiveKey(key string) bool {
	_, ok := sensitiveKeys[strings.ToLower(key)]
	return ok
}

// ParseLevel parses "debug", "info", "warn" (or "warning") and "error",
// case-insensitively. The empty string selects info.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("observability: unknown log level %q (want debug, info, warn or error)", level)
	}
}

// NewLogger builds a slog logger writing to w (os.Stderr when nil) in the
// given format ("json", the default when empty, or "text") at the given level.
// Source locations are not added. Attributes whose key is password, token,
// secret, authorization, cookie, payload or url_credentials — and every
// attribute nested in a group with such a name — have their values replaced
// by RedactedValue.
func NewLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{
		Level:       lvl,
		AddSource:   false,
		ReplaceAttr: redactAttr,
	}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatJSON:
		h = slog.NewJSONHandler(w, opts)
	case FormatText:
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("observability: unknown log format %q (want json or text)", format)
	}
	return slog.New(h), nil
}

// redactAttr is the slog ReplaceAttr hook that masks sensitive attributes.
// Built-in attributes (time, level, msg) are top-level with fixed keys and are
// never sensitive.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, RedactedValue)
	}
	for _, g := range groups {
		if IsSensitiveKey(g) {
			return slog.String(a.Key, RedactedValue)
		}
	}
	return a
}
