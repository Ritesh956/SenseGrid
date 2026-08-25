// Package window is the Phase 3 windowed stage: per-device/sensor sliding
// windows of recent readings, kept incrementally (Welford's algorithm for
// mean/variance, plus an EWMA) so cmd/processor's second durable consumer
// can emit derived metrics on every window update without ever recomputing
// a window's statistics from scratch. Pure computation, no I/O — everything
// here is unit-testable in isolation from JetStream/Postgres.
package window

import "math"

// Stats is Welford's incremental mean/variance, extended with Remove so a
// value leaving a sliding window can be subtracted back out in O(1) instead
// of the window being recomputed from its remaining samples on every
// eviction. Remove is the exact algebraic inverse of Update: for any
// sequence of Updates followed by the same values Removed, Stats returns to
// its prior state (up to floating-point rounding) — this is what
// window_test.go checks it against a naive from-scratch recompute for.
type Stats struct {
	count int64
	mean  float64
	m2    float64 // sum of squared deviations from the mean
}

// Update folds x into the running mean/variance.
func (s *Stats) Update(x float64) {
	s.count++
	delta := x - s.mean
	s.mean += delta / float64(s.count)
	delta2 := x - s.mean
	s.m2 += delta * delta2
}

// Remove reverses a prior Update(x). The caller is responsible for only
// removing values it actually added (the window buffer in window.go is
// what guarantees this) — Remove has no way to verify x was ever present.
func (s *Stats) Remove(x float64) {
	if s.count <= 1 {
		s.count = 0
		s.mean = 0
		s.m2 = 0
		return
	}
	prevCount := s.count
	s.count--
	prevMean := s.mean
	s.mean = (prevMean*float64(prevCount) - x) / float64(s.count)
	s.m2 -= (x - s.mean) * (x - prevMean)
	if s.m2 < 0 {
		// Floating-point rounding can push m2 fractionally below zero when
		// count is small; variance can't be negative.
		s.m2 = 0
	}
}

func (s *Stats) Count() int64 { return s.count }
func (s *Stats) Mean() float64 {
	if s.count == 0 {
		return 0
	}
	return s.mean
}

// Variance is the population variance of the values currently folded in
// (sample variance, s.m2/(count-1), would be undefined below count==2 —
// population variance stays well-defined from count==1).
func (s *Stats) Variance() float64 {
	if s.count == 0 {
		return 0
	}
	return s.m2 / float64(s.count)
}

func (s *Stats) StdDev() float64 { return math.Sqrt(s.Variance()) }

// EWMA is an exponentially weighted moving average with a configurable
// smoothing factor Alpha in (0,1]. Higher Alpha weights recent samples more
// heavily (tracks faster, noisier); lower Alpha smooths harder.
type EWMA struct {
	Alpha  float64
	value  float64
	primed bool
}

func NewEWMA(alpha float64) *EWMA {
	return &EWMA{Alpha: alpha}
}

// Update folds x in and returns the new EWMA value. The first call seeds
// the average with x rather than 0, so a single early reading doesn't drag
// the average toward zero before it's had a chance to converge.
func (e *EWMA) Update(x float64) float64 {
	if !e.primed {
		e.value = x
		e.primed = true
		return e.value
	}
	e.value = e.Alpha*x + (1-e.Alpha)*e.value
	return e.value
}

func (e *EWMA) Value() float64 { return e.value }
