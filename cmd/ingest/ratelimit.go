package main

import (
	"sync"

	"golang.org/x/time/rate"
)

// perDeviceLimiter is the ingest bridge's backpressure-shedding mechanism:
// a lazily-created token bucket per device_id. Deliberately simple for
// Phase 2 — no eviction of long-idle devices' limiters yet. At real-device
// scale that's a non-issue; Phase 8's load test (test/hardening/
// rate_limit_load.sh) exercises this under the synthetic fleet's device
// counts, which is the right time to decide whether eviction is actually
// needed rather than guessing now. That test also found this map being
// per-device was necessary but not sufficient for isolating a runaway
// device — see main.go's SetOrderMatters(false) for the other half.
type perDeviceLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	burst    int
}

func newPerDeviceLimiter(perSecond float64, burst int) *perDeviceLimiter {
	return &perDeviceLimiter{
		limiters: make(map[string]*rate.Limiter),
		r:        rate.Limit(perSecond),
		burst:    burst,
	}
}

func (l *perDeviceLimiter) Allow(deviceID string) bool {
	l.mu.Lock()
	lim, ok := l.limiters[deviceID]
	if !ok {
		lim = rate.NewLimiter(l.r, l.burst)
		l.limiters[deviceID] = lim
	}
	l.mu.Unlock()
	return lim.Allow()
}
