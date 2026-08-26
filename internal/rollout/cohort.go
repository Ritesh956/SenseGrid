package rollout

import (
	"hash/fnv"

	"github.com/Ritesh956/SenseGrid/internal/devices"
)

// SelectCohort filters every claimed device down to c's cohort: DeviceIDs
// exactly, when set, else every device of DeviceType (empty = every
// device).
func SelectCohort(all []*devices.Device, c Cohort) []string {
	if len(c.DeviceIDs) > 0 {
		out := make([]string, len(c.DeviceIDs))
		copy(out, c.DeviceIDs)
		return out
	}
	var out []string
	for _, d := range all {
		if c.DeviceType == "" || d.Type == c.DeviceType {
			out = append(out, d.ID)
		}
	}
	return out
}

// StageTargets returns which of cohortDeviceIDs stage `percent` includes.
// Each device is deterministically bucketed 0-99 by hashing its ID
// (fnv-32a — stable across process restarts, no shared state needed), so
// a device included at 10% stays included at every later, larger
// percentage in the same rollout: stage membership only ever grows,
// device-by-device, exactly what "staged" rollout requires. Pure — no
// dependency on any store or on which stage index this actually is.
func StageTargets(cohortDeviceIDs []string, percent int) []string {
	if percent <= 0 {
		return nil
	}
	if percent > 100 {
		percent = 100
	}
	var out []string
	for _, id := range cohortDeviceIDs {
		if bucket(id) < percent {
			out = append(out, id)
		}
	}
	return out
}

func bucket(deviceID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceID))
	return int(h.Sum32() % 100)
}
