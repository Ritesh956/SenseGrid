package window

// MetricEvent is what cmd/processor's windowed consumer publishes to
// internal/telemetry.MetricsSubject on every window update — the derived
// signal a rule evaluates against, and eventually what Phase 5's console
// charts. Unlike telemetry.Reading (the raw device payload), this is
// produced by the platform, not a client, so it lives alongside the code
// that produces it rather than in internal/telemetry.
type MetricEvent struct {
	SchemaVersion string  `json:"schema_version"`
	DeviceID      string  `json:"device_id"`
	SensorType    string  `json:"sensor_type"`
	Mean          float64 `json:"mean"`
	StdDev        float64 `json:"stddev"`
	Count         int64   `json:"count"`
	EWMA          float64 `json:"ewma"`
	WindowStartMS int64   `json:"window_start_ms"`
	WindowEndMS   int64   `json:"window_end_ms"`
	TraceID       string  `json:"trace_id,omitempty"`
}

const MetricEventSchemaVersion = "1.0"
