package rollout

import "testing"

func TestEvaluateHealth(t *testing.T) {
	crit := HealthCriteria{MaxErrorRate: 0.1, MaxDisconnectRate: 0.2, MaxRejectionRate: 0.1}

	if breached, reasons := EvaluateHealth(crit, 0, 0, 0, 0); breached {
		t.Errorf("zero targeted devices must never breach, got reasons=%v", reasons)
	}

	if breached, _ := EvaluateHealth(crit, 100, 0, 0, 0); breached {
		t.Error("all healthy should not breach")
	}

	if breached, reasons := EvaluateHealth(crit, 100, 15, 0, 0); !breached {
		t.Error("15% rejection against a 10% max should breach")
	} else if len(reasons) != 1 {
		t.Errorf("expected exactly one reason, got %v", reasons)
	}

	if breached, reasons := EvaluateHealth(crit, 100, 0, 25, 0); !breached {
		t.Error("25% disconnect against a 20% max should breach")
	} else if len(reasons) != 1 {
		t.Errorf("expected exactly one reason, got %v", reasons)
	}

	if breached, reasons := EvaluateHealth(crit, 100, 15, 25, 11); !breached {
		t.Error("all three over threshold should breach")
	} else if len(reasons) != 3 {
		t.Errorf("expected three reasons, got %v", reasons)
	}

	// Exactly at threshold does not breach (strictly greater-than).
	if breached, _ := EvaluateHealth(crit, 100, 10, 0, 0); breached {
		t.Error("exactly at the 10% threshold should not breach")
	}

	// A zero-valued max (criterion not set) never breaches regardless of count.
	noLimits := HealthCriteria{}
	if breached, _ := EvaluateHealth(noLimits, 100, 100, 100, 100); breached {
		t.Error("unset (zero) criteria should never breach")
	}
}
