// Package logging configures structured logging for the operator and
// provides the Secret type that keeps sensitive values out of all output.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Setup builds a slog.Logger writing to w with the given level
// (debug|info|warn|error) and format (text|json).
func Setup(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "", "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown log level %q (want debug|info|warn|error)", level)
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	switch strings.ToLower(format) {
	case "", "text":
		handler = slog.NewTextHandler(w, opts)
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		return nil, fmt.Errorf("unknown log format %q (want text|json)", format)
	}
	return slog.New(handler), nil
}
