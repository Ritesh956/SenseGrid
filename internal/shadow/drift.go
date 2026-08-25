package shadow

import "time"

// Drift reports whether a device has drifted from its desired config:
// no report yet, its last-applied revision doesn't match the desired
// state's current revision, or its last report is older than staleAfter.
// Pure — no I/O — so GET /v1/devices/drift's logic is unit-testable
// without a KV bucket or a database.
func Drift(desiredRev uint64, reported *Reported, now time.Time, staleAfter time.Duration) bool {
	if reported == nil {
		return true
	}
	if reported.AppliedRevision != desiredRev {
		return true
	}
	reportedAt := time.UnixMilli(reported.ReportedAtMS)
	return now.Sub(reportedAt) > staleAfter
}
