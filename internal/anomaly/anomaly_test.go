package anomaly

import (
	"testing"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/rules"
	"github.com/Ritesh956/SenseGrid/internal/window"
)

func TestEvaluateZScore(t *testing.T) {
	w := window.New(0, time.Hour, 0.3)
	base := time.Unix(1000, 0)
	for i, v := range []float64{10, 10, 10, 10, 10} {
		w.Insert(base.Add(time.Duration(i)*time.Second), v, uint64(i))
	}
	rule := rules.Rule{Threshold: 3}

	if violating, _ := EvaluateZScore(10, w, rule); violating {
		t.Error("value equal to a zero-variance mean should never violate")
	}

	w2 := window.New(0, time.Hour, 0.3)
	for i, v := range []float64{9, 10, 11, 10, 9, 11, 10} {
		w2.Insert(base.Add(time.Duration(i)*time.Second), v, uint64(i))
	}
	if violating, score := EvaluateZScore(100, w2, rule); !violating {
		t.Errorf("value=100 far outside the window's spread should violate, score=%v", score)
	}
}

func TestEvaluateRateOfChange(t *testing.T) {
	w := window.New(0, time.Hour, 1.0) // alpha=1 makes EWMA track the raw value exactly
	base := time.Unix(1000, 0)
	rule := rules.Rule{Threshold: 5}

	w.Insert(base, 10, 0)
	if violating, _ := EvaluateRateOfChange(w, rule); violating {
		t.Error("first sample has no prior EWMA to compare against, must not violate")
	}

	w.Insert(base.Add(time.Second), 12, 1)
	if violating, delta := EvaluateRateOfChange(w, rule); violating {
		t.Errorf("small step (delta=%v) under threshold should not violate", delta)
	}

	w.Insert(base.Add(2*time.Second), 50, 2)
	if violating, delta := EvaluateRateOfChange(w, rule); !violating {
		t.Errorf("large step (delta=%v) over threshold should violate", delta)
	}
}

func TestEvaluateSilence(t *testing.T) {
	rule := rules.Rule{SilenceTimeout: 30 * time.Second}
	now := time.Unix(2000, 0)

	if violating, _ := EvaluateSilence(now.Add(-10*time.Second), now, rule); violating {
		t.Error("10s gap under a 30s timeout should not violate")
	}
	if violating, _ := EvaluateSilence(now.Add(-60*time.Second), now, rule); !violating {
		t.Error("60s gap over a 30s timeout should violate")
	}
	if violating, _ := EvaluateSilence(time.Time{}, now, rule); violating {
		t.Error("a zero lastSeen (never seen) should not violate — nothing to be silent about yet")
	}
}

func TestEvaluator_ConsecutiveViolationsBeforeFiring(t *testing.T) {
	e := NewEvaluator()
	key := Key{DeviceID: "d1", SensorType: "cpu", RuleName: "r1"}
	const M = 3

	if ev := e.Update(key, true, M); ev != NoChange {
		t.Fatalf("violation 1/%d: got %v, want NoChange", M, ev)
	}
	if ev := e.Update(key, true, M); ev != NoChange {
		t.Fatalf("violation 2/%d: got %v, want NoChange", M, ev)
	}
	if ev := e.Update(key, true, M); ev != Fired {
		t.Fatalf("violation 3/%d: got %v, want Fired", M, ev)
	}
	if ev := e.Update(key, true, M); ev != NoChange {
		t.Fatalf("already-firing key must not fire again: got %v, want NoChange", ev)
	}
}

func TestEvaluator_SingleSampleNoiseSuppressed(t *testing.T) {
	e := NewEvaluator()
	key := Key{DeviceID: "d1", SensorType: "cpu", RuleName: "r1"}
	const M = 3

	e.Update(key, true, M)
	e.Update(key, true, M)
	if ev := e.Update(key, false, M); ev != NoChange {
		t.Fatalf("a single clear resetting the streak must not fire, got %v", ev)
	}
	// streak must have reset to zero, not continued
	e.Update(key, true, M)
	if ev := e.Update(key, true, M); ev != NoChange {
		t.Fatalf("only 2 violations since the reset, must not fire yet: got %v", ev)
	}
}

func TestEvaluator_ConsecutiveClearsBeforeResolving(t *testing.T) {
	e := NewEvaluator()
	key := Key{DeviceID: "d1", SensorType: "cpu", RuleName: "r1"}
	const M = 2

	e.Update(key, true, M)
	if ev := e.Update(key, true, M); ev != Fired {
		t.Fatal("expected Fired after M violations")
	}
	if ev := e.Update(key, false, M); ev != NoChange {
		t.Fatalf("1/%d clears: got %v, want NoChange", M, ev)
	}
	if ev := e.Update(key, false, M); ev != Cleared {
		t.Fatalf("2/%d clears: got %v, want Cleared", M, ev)
	}
}
