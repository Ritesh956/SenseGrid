package window

import (
	"sync"
	"time"
)

// Key identifies one (device, sensor) window in a Registry. sensor_type is
// already flattened by the time readings reach here (e.g. "accel.x"), same
// as cmd/processor's persistence consumer — see internal/telemetry and
// cmd/processor/consumer.go's flatten.
type Key struct {
	DeviceID   string
	SensorType string
}

// entry pairs a Window with the wall-clock time it was last touched, so
// Sweep can find devices that have gone quiet without depending on the
// device's own (possibly skewed) clock.
type entry struct {
	window   *Window
	lastSeen time.Time
}

// Registry holds one Window per (device_id, sensor_type) pair seen since
// startup, and evicts entries that haven't been touched within a TTL — the
// blueprint's "evict silent devices on a TTL so memory stays bounded".
// This is a coarser, longer timescale than the silence *detector* in
// internal/anomaly (which fires an alert after a much shorter gap): the
// detector answers "is this device probably broken", the registry sweep
// just reclaims memory for devices that are very likely never coming back
// in this process's lifetime.
type Registry struct {
	maxCount  int
	maxAge    time.Duration
	ewmaAlpha float64

	mu      sync.Mutex
	entries map[Key]*entry
}

func NewRegistry(maxCount int, maxAge time.Duration, ewmaAlpha float64) *Registry {
	return &Registry{
		maxCount:  maxCount,
		maxAge:    maxAge,
		ewmaAlpha: ewmaAlpha,
		entries:   make(map[Key]*entry),
	}
}

// Get returns the Window for key, creating one on first use, and marks it
// touched at now.
func (r *Registry) Get(key Key, now time.Time) *Window {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[key]
	if !ok {
		e = &entry{window: New(r.maxCount, r.maxAge, r.ewmaAlpha)}
		r.entries[key] = e
	}
	e.lastSeen = now
	return e.window
}

// LastSeen reports when key was last touched, and whether it exists at all
// (used by the silence detector, which needs the gap since last data even
// for a window that currently holds zero samples).
func (r *Registry) LastSeen(key Key) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok {
		return time.Time{}, false
	}
	return e.lastSeen, true
}

// Sweep removes every entry not touched within ttl of now, returning how
// many were removed (for the caller's metrics).
func (r *Registry) Sweep(now time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for k, e := range r.entries {
		if now.Sub(e.lastSeen) > ttl {
			delete(r.entries, k)
			removed++
		}
	}
	return removed
}

// Size returns the number of live entries, for a gauge metric.
func (r *Registry) Size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Keys returns a snapshot of every key currently tracked, for the silence
// detector's periodic sweep (it needs to check devices that have stopped
// sending anything at all, not just devices whose latest reading tripped a
// threshold).
func (r *Registry) Keys() []Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]Key, 0, len(r.entries))
	for k := range r.entries {
		keys = append(keys, k)
	}
	return keys
}
