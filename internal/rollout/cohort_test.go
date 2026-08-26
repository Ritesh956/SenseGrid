package rollout

import (
	"fmt"
	"testing"

	"github.com/Ritesh956/SenseGrid/internal/devices"
)

func idsN(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("device-%d", i)
	}
	return out
}

func TestStageTargets_Edges(t *testing.T) {
	ids := idsN(200)

	if got := StageTargets(ids, 0); got != nil {
		t.Errorf("0%% should target nothing, got %d devices", len(got))
	}
	full := StageTargets(ids, 100)
	if len(full) != len(ids) {
		t.Errorf("100%% should target every device: got %d, want %d", len(full), len(ids))
	}
	// Over 100 clamps to 100, doesn't panic or over-select.
	over := StageTargets(ids, 150)
	if len(over) != len(ids) {
		t.Errorf(">100%% should clamp to 100%%: got %d, want %d", len(over), len(ids))
	}
}

func TestStageTargets_MonotonicGrowth(t *testing.T) {
	ids := idsN(500) // large enough that bucket distribution is reasonably even

	prev := map[string]bool{}
	prevPercent := 0
	for _, p := range []int{10, 25, 50, 75, 90, 100} {
		targets := StageTargets(ids, p)
		set := map[string]bool{}
		for _, id := range targets {
			set[id] = true
		}
		for id := range prev {
			if !set[id] {
				t.Fatalf("device %s was targeted at %d%% but dropped at %d%% — stage membership must only grow", id, prevPercent, p)
			}
		}
		// Sanity: percentage roughly tracks bucket count (not an exact
		// assertion — hashing isn't perfectly uniform on any given input
		// set — just a broad bounds check that StageTargets isn't
		// returning something wildly wrong like everyone or no one).
		if p > 0 && p < 100 && len(targets) == 0 {
			t.Fatalf("expected some devices targeted at %d%% of a 500-device cohort, got 0", p)
		}
		prev = set
		prevPercent = p
	}
}

func TestStageTargets_Deterministic(t *testing.T) {
	ids := idsN(50)
	a := StageTargets(ids, 30)
	b := StageTargets(ids, 30)
	if len(a) != len(b) {
		t.Fatalf("StageTargets should be deterministic for the same input: got %d vs %d", len(a), len(b))
	}
	setA := map[string]bool{}
	for _, id := range a {
		setA[id] = true
	}
	for _, id := range b {
		if !setA[id] {
			t.Fatalf("StageTargets returned different devices on repeat calls: %s missing", id)
		}
	}
}

func TestSelectCohort(t *testing.T) {
	all := []*devices.Device{
		{ID: "d1", Type: "phone"},
		{ID: "d2", Type: "laptop"},
		{ID: "d3", Type: "phone"},
	}

	byType := SelectCohort(all, Cohort{DeviceType: "phone"})
	if len(byType) != 2 {
		t.Errorf("DeviceType=phone: got %d devices, want 2", len(byType))
	}

	everyone := SelectCohort(all, Cohort{})
	if len(everyone) != 3 {
		t.Errorf("empty cohort: got %d devices, want 3 (every device)", len(everyone))
	}

	explicit := SelectCohort(all, Cohort{DeviceIDs: []string{"d2"}})
	if len(explicit) != 1 || explicit[0] != "d2" {
		t.Errorf("explicit DeviceIDs should be used exactly, got %v", explicit)
	}
}
