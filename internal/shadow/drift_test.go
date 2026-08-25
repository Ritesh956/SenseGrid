package shadow

import (
	"testing"
	"time"
)

func TestDrift(t *testing.T) {
	now := time.Unix(2000, 0)
	const staleAfter = 30 * time.Second

	if !Drift(5, nil, now, staleAfter) {
		t.Error("no report at all must count as drifted")
	}

	stale := &Reported{AppliedRevision: 5, ReportedAtMS: now.Add(-10 * time.Second).UnixMilli()}
	if Drift(5, stale, now, staleAfter) {
		t.Error("matching revision, within staleness window, must not be drifted")
	}

	wrongRev := &Reported{AppliedRevision: 4, ReportedAtMS: now.Add(-1 * time.Second).UnixMilli()}
	if !Drift(5, wrongRev, now, staleAfter) {
		t.Error("applied_revision behind the current desired revision must be drifted")
	}

	tooOld := &Reported{AppliedRevision: 5, ReportedAtMS: now.Add(-60 * time.Second).UnixMilli()}
	if !Drift(5, tooOld, now, staleAfter) {
		t.Error("matching revision but a stale report must still be drifted")
	}
}
