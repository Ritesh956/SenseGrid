package window

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// naiveStats recomputes mean/variance from scratch over vals, as the
// reference oracle Stats' incremental Update/Remove is checked against.
func naiveStats(vals []float64) (mean, variance float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	mean = sum / float64(len(vals))
	sq := 0.0
	for _, v := range vals {
		sq += (v - mean) * (v - mean)
	}
	return mean, sq / float64(len(vals))
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

// TestStats_IncrementalVsNaive drives Stats through a random sequence of
// Update/Remove calls, checking after every step that its incremental
// mean/variance matches a naive from-scratch recompute over whatever
// values are "currently in" — this is the "incremental stats vs. a naive
// reference implementation" unit test the blueprint calls for.
func TestStats_IncrementalVsNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	var s Stats
	var present []float64

	for i := 0; i < 2000; i++ {
		if len(present) > 0 && rng.Intn(3) == 0 {
			idx := rng.Intn(len(present))
			v := present[idx]
			present = append(present[:idx], present[idx+1:]...)
			s.Remove(v)
		} else {
			v := rng.NormFloat64() * 10
			present = append(present, v)
			s.Update(v)
		}

		wantMean, wantVar := naiveStats(present)
		if int64(len(present)) != s.Count() {
			t.Fatalf("step %d: count = %d, want %d", i, s.Count(), len(present))
		}
		if !almostEqual(s.Mean(), wantMean) {
			t.Fatalf("step %d: mean = %v, want %v", i, s.Mean(), wantMean)
		}
		if !almostEqual(s.Variance(), wantVar) {
			t.Fatalf("step %d: variance = %v, want %v", i, s.Variance(), wantVar)
		}
	}
}

func TestWindow_CountBoundEviction(t *testing.T) {
	w := New(3, 0, 0.3)
	base := time.Unix(1000, 0)
	for i, v := range []float64{1, 2, 3, 4, 5} {
		w.Insert(base.Add(time.Duration(i)*time.Second), v, uint64(i))
	}
	if w.Count() != 3 {
		t.Fatalf("count = %d, want 3", w.Count())
	}
	wantMean, _ := naiveStats([]float64{3, 4, 5})
	if !almostEqual(w.Mean(), wantMean) {
		t.Fatalf("mean = %v, want %v", w.Mean(), wantMean)
	}
}

func TestWindow_TimeBoundEviction(t *testing.T) {
	w := New(0, 10*time.Second, 0.3)
	base := time.Unix(1000, 0)
	w.Insert(base, 1, 0)
	w.Insert(base.Add(5*time.Second), 2, 1)
	w.Insert(base.Add(25*time.Second), 3, 2) // more than 10s after the newest-so-far minus itself

	// Newest sample is at +25s; anything older than (25-10)=15s from base is evicted.
	if w.Count() != 1 {
		t.Fatalf("count = %d, want 1 (only the +25s sample should remain)", w.Count())
	}
	if !almostEqual(w.Mean(), 3) {
		t.Fatalf("mean = %v, want 3", w.Mean())
	}
}

// TestWindow_OutOfOrderArrival checks that inserting the same set of
// samples out of device_time order produces the same window contents (and
// therefore the same stats) as inserting them in order — insertion order
// must not matter, only device_time.
func TestWindow_OutOfOrderArrival(t *testing.T) {
	base := time.Unix(1000, 0)
	type s struct {
		offset time.Duration
		value  float64
	}
	samples := []s{
		{0 * time.Second, 10},
		{1 * time.Second, 20},
		{2 * time.Second, 30},
		{3 * time.Second, 40},
		{4 * time.Second, 50},
	}

	inOrder := New(0, time.Hour, 0.3)
	for i, sm := range samples {
		inOrder.Insert(base.Add(sm.offset), sm.value, uint64(i))
	}

	outOfOrder := New(0, time.Hour, 0.3)
	perm := []int{2, 0, 4, 1, 3} // arrival order scrambled relative to device_time
	for _, i := range perm {
		outOfOrder.Insert(base.Add(samples[i].offset), samples[i].value, uint64(i))
	}

	if !almostEqual(inOrder.Mean(), outOfOrder.Mean()) {
		t.Fatalf("mean diverges: in-order=%v out-of-order=%v", inOrder.Mean(), outOfOrder.Mean())
	}
	if !almostEqual(inOrder.StdDev(), outOfOrder.StdDev()) {
		t.Fatalf("stddev diverges: in-order=%v out-of-order=%v", inOrder.StdDev(), outOfOrder.StdDev())
	}
	if inOrder.Count() != outOfOrder.Count() {
		t.Fatalf("count diverges: in-order=%d out-of-order=%d", inOrder.Count(), outOfOrder.Count())
	}
	if !inOrder.NewestTime().Equal(outOfOrder.NewestTime()) {
		t.Fatalf("newest time diverges: in-order=%v out-of-order=%v", inOrder.NewestTime(), outOfOrder.NewestTime())
	}
	if !inOrder.OldestTime().Equal(outOfOrder.OldestTime()) {
		t.Fatalf("oldest time diverges: in-order=%v out-of-order=%v", inOrder.OldestTime(), outOfOrder.OldestTime())
	}
}

func TestWindow_DuplicateSeqIgnored(t *testing.T) {
	w := New(0, time.Hour, 0.3)
	base := time.Unix(1000, 0)
	if ok := w.Insert(base, 1, 42); !ok {
		t.Fatal("first insert should succeed")
	}
	if ok := w.Insert(base, 999, 42); ok {
		t.Fatal("duplicate seq should be rejected")
	}
	if w.Count() != 1 {
		t.Fatalf("count = %d, want 1 (duplicate must not be folded in)", w.Count())
	}
	if !almostEqual(w.Mean(), 1) {
		t.Fatalf("mean = %v, want 1 (duplicate must not have changed it)", w.Mean())
	}
}
