package rollout

import "fmt"

// EvaluateHealth reports whether a stage has breached its HealthCriteria,
// given how many of its `targeted` devices are rejected/disconnected/
// errored (see engine.go's evaluateStageHealth for how those counts are
// gathered). Pure — no I/O — so the threshold math is unit-testable in
// isolation. targeted == 0 never breaches (nothing to evaluate yet, e.g. a
// stage whose bake period just started and no reports have arrived).
func EvaluateHealth(criteria HealthCriteria, targeted, rejected, disconnected, errored int) (breached bool, reasons []string) {
	if targeted == 0 {
		return false, nil
	}
	check := func(count int, max float64, label string) {
		rate := float64(count) / float64(targeted)
		if max > 0 && rate > max {
			breached = true
			reasons = append(reasons, fmt.Sprintf("%s rate %.0f%% exceeds max %.0f%% (%d/%d)", label, rate*100, max*100, count, targeted))
		}
	}
	check(rejected, criteria.MaxRejectionRate, "rejection")
	check(disconnected, criteria.MaxDisconnectRate, "disconnect")
	check(errored, criteria.MaxErrorRate, "error")
	return breached, reasons
}
