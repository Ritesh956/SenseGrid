// Package anomaly is the Phase 3 detector layer: pure functions that
// evaluate a window.Window's current statistics (or a silence gap) against
// a rules.Rule, plus an Evaluator that adds the "M consecutive violations
// before firing" hysteresis the blueprint calls for. No I/O — the caller
// (cmd/processor's windowed consumer) is what turns a firing/clearing
// Evaluator result into an alerts.Store call.
package anomaly

import (
	"math"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/rules"
	"github.com/Ritesh956/SenseGrid/internal/window"
)

// EvaluateZScore reports whether value is more than rule.Threshold
// standard deviations from the window's current mean. Needs at least 2
// samples in the window (stddev is undefined/zero below that) — with
// fewer, it never fires rather than false-positiving on an empty
// baseline.
func EvaluateZScore(value float64, w *window.Window, rule rules.Rule) (violating bool, score float64) {
	if w.Count() < 2 {
		return false, 0
	}
	stddev := w.StdDev()
	if stddev == 0 {
		return false, 0
	}
	z := math.Abs(value-w.Mean()) / stddev
	return z > rule.Threshold, z
}

// EvaluateRateOfChange reports whether the window's EWMA moved by more
// than rule.Threshold since the previous window update — a step change in
// the smoothed signal, not raw sample-to-sample jitter. Needs at least 2
// samples so PrevEWMA reflects a real prior value rather than the EWMA's
// zero-value seed.
func EvaluateRateOfChange(w *window.Window, rule rules.Rule) (violating bool, delta float64) {
	if w.Count() < 2 {
		return false, 0
	}
	d := math.Abs(w.EWMA() - w.PrevEWMA())
	return d > rule.Threshold, d
}

// EvaluateSilence reports whether the gap since lastSeen exceeds
// rule.SilenceTimeout.
func EvaluateSilence(lastSeen, now time.Time, rule rules.Rule) (violating bool, gap time.Duration) {
	if lastSeen.IsZero() {
		return false, 0
	}
	gap = now.Sub(lastSeen)
	return gap > rule.SilenceTimeout, gap
}
