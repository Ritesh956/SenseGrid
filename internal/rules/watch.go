package rules

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// Watcher polls a rules file's mtime and reloads it into an atomic
// snapshot whenever it changes, so cmd/processor's windowed consumer
// always evaluates against a consistent Config without holding a lock —
// readers just call Current().
type Watcher struct {
	path    string
	current atomic.Pointer[Config]
	logger  *slog.Logger
}

// NewWatcher does the first Load synchronously (a rules file that doesn't
// parse at startup should fail fast, not silently run with zero rules)
// and returns a Watcher ready for Current(); call Run in a goroutine to
// start polling for changes.
func NewWatcher(path string, logger *slog.Logger) (*Watcher, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	w := &Watcher{path: path, logger: logger}
	w.current.Store(cfg)
	return w, nil
}

// Current returns the most recently successfully loaded Config.
func (w *Watcher) Current() *Config {
	return w.current.Load()
}

// Run polls the rules file's mtime every interval until ctx is cancelled,
// reloading and swapping Current on change. A reload that fails to parse
// (e.g. mid-write, or a genuine YAML error) logs and keeps the previous
// Config rather than tearing anything down — a bad edit shouldn't stop
// detection running on the last-known-good rules.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastMod := modTime(w.path)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mod := modTime(w.path)
			if mod.IsZero() || !mod.After(lastMod) {
				continue
			}
			lastMod = mod
			cfg, err := Load(w.path)
			if err != nil {
				w.logger.Warn("rules: reload failed, keeping previous rules", "path", w.path, "err", err)
				continue
			}
			w.current.Store(cfg)
			w.logger.Info("rules: reloaded", "path", w.path, "rules", len(cfg.Rules))
		}
	}
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
