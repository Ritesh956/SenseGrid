// Package shadow is the Phase 4 device shadow: desired/reported config
// state held in a NATS JetStream KV bucket (the hot path, versioned by
// JetStream's own revision number), with Postgres mirroring shadow history
// for audit/reporting only — see store.go. Desired state is pushed to
// devices as a retained MQTT publish (publisher.go); devices report back
// what they actually applied, which reconciler.go folds back into the KV
// bucket.
package shadow

const SchemaVersion = "1.0"

// ReportingMode selects how an edge client paces its telemetry publishes.
// Not specified by the blueprint beyond naming the field — defined here as
// the two modes that give batch_size/flush_interval_ms a reason to exist:
// "continuous" ignores them (today's behavior, one publish per reading),
// "batched" buffers up to batch_size readings or flush_interval_ms,
// whichever comes first, then sends them as a burst of individual PUBLISH
// packets (the telemetry wire schema itself doesn't change, so cmd/ingest
// needs no changes).
type ReportingMode string

const (
	ReportingContinuous ReportingMode = "continuous"
	ReportingBatched    ReportingMode = "batched"
)

// Desired is a device's desired configuration — the value written to the
// {device_id}.desired KV key and retained-published to
// internal/telemetry.ConfigTopic. Every field beyond SchemaVersion is a
// pointer/omitempty so a partial update ("just change sample_rate_hz")
// doesn't require the caller to already know every other current value.
// Revision is never stored inside the KV value's JSON — it's stamped onto
// this struct only when read back from the KV entry's own metadata (see
// Store.GetDesired) or right after a successful Store.SetDesired, so it's
// always sourced from JetStream itself, never duplicated state that could
// drift from it.
type Desired struct {
	SchemaVersion   string         `json:"schema_version"`
	Revision        uint64         `json:"revision,omitempty"`
	SampleRateHz    *float64       `json:"sample_rate_hz,omitempty"`
	EnabledSensors  []string       `json:"enabled_sensors,omitempty"`
	BatchSize       *int           `json:"batch_size,omitempty"`
	FlushIntervalMS *int           `json:"flush_interval_ms,omitempty"`
	ReportingMode   *ReportingMode `json:"reporting_mode,omitempty"`
}

// Reported is what a device publishes to internal/telemetry.StateTopic
// after applying (or rejecting) a Desired update, and what's stored at the
// {device_id}.reported KV key. AppliedRevision is the Desired.Revision the
// device is echoing back — comparing it to Desired's *current* KV revision
// (not to whatever Reported message happens to arrive next) is what makes
// drift detection robust to MQTT redelivery / out-of-order state reports.
type Reported struct {
	SchemaVersion    string         `json:"schema_version"`
	AppliedRevision  uint64         `json:"applied_revision"`
	Rejected         bool           `json:"rejected,omitempty"`
	RejectedRevision uint64         `json:"rejected_revision,omitempty"`
	RejectReason     string         `json:"reject_reason,omitempty"`
	SampleRateHz     *float64       `json:"sample_rate_hz,omitempty"`
	EnabledSensors   []string       `json:"enabled_sensors,omitempty"`
	BatchSize        *int           `json:"batch_size,omitempty"`
	FlushIntervalMS  *int           `json:"flush_interval_ms,omitempty"`
	ReportingMode    *ReportingMode `json:"reporting_mode,omitempty"`
	ReportedAtMS     int64          `json:"reported_at_ms"`
}
