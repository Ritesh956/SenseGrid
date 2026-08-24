// Package logging provides the structured JSON logger every SenseGrid
// service starts with, so logs are grep/jq-able from Phase 0 onward instead
// of being retrofitted once services are already emitting free-text lines.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON slog.Logger tagged with the given service name, at the
// given level ("debug", "info", "warn", "error"; defaults to "info").
func New(serviceName, level string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
	})
	return slog.New(handler).With("service", serviceName)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
