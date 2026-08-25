// Package telemetry is the v1 payload schema (see the SenseGrid Blueprint,
// §04 Data contracts) shared by every native Go publisher — cmd/hostagent
// now, cmd/fleet and cmd/ingest's validator in later phases — so the wire
// format has exactly one definition instead of drifting per service. The
// PWA sensor client (JavaScript, no shared module system with Go) mirrors
// this shape by hand in web/sensor-client/app.js.
package telemetry

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const SchemaVersion = "1.0"

// JetStream stream/subject names, shared by cmd/ingest (which creates the
// streams and publishes) and cmd/processor (which consumes them) so the
// naming lives in exactly one place.
const (
	TelemetryStreamName = "TELEMETRY"
	DLQStreamName       = "DLQ"
	DLQSubject          = "dlq.telemetry"

	// MetricsStreamName and AlertsStreamName are created by cmd/processor
	// (Phase 3), which is both their producer and — for alerts — also a
	// consumer of its own alert-clearing logic. See cmd/processor/jetstream.go.
	MetricsStreamName = "METRICS"
	AlertsStreamName  = "ALERTS"
)

// Reading is one sensor sample. Use Value for a scalar reading (e.g. CPU
// percent) or Values for a vector reading (e.g. accelerometer x/y/z);
// exactly one should be set.
type Reading struct {
	SchemaVersion string             `json:"schema_version"`
	DeviceID      string             `json:"device_id"`
	SensorType    string             `json:"sensor_type"`
	Value         *float64           `json:"value,omitempty"`
	Values        map[string]float64 `json:"values,omitempty"`
	DeviceTimeMS  int64              `json:"device_time_ms"`
	Seq           uint64             `json:"seq"`
	TraceID       string             `json:"trace_id"`
}

// Validate reports whether r is well-formed enough to persist. It does not
// check that DeviceID matches the MQTT topic it arrived on — the caller
// (cmd/ingest) does that separately, since Validate has no notion of the
// topic a reading came in on.
func (r Reading) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %q", r.SchemaVersion)
	}
	if _, err := uuid.Parse(r.DeviceID); err != nil {
		return fmt.Errorf("invalid device_id: %w", err)
	}
	if r.SensorType == "" {
		return errors.New("sensor_type is required")
	}
	if r.Value == nil && r.Values == nil {
		return errors.New("exactly one of value or values must be set")
	}
	if r.Value != nil && r.Values != nil {
		return errors.New("exactly one of value or values must be set, not both")
	}
	if r.DeviceTimeMS <= 0 {
		return errors.New("device_time_ms must be a positive epoch-millisecond timestamp")
	}
	if len(r.TraceID) != 32 {
		return errors.New("trace_id must be 32 hex characters")
	}
	return nil
}

// StampedReading is what the ingest bridge publishes to JetStream: a
// Reading plus the broker-receive timestamp, so the persistence consumer
// has both device_time and ingest_time without a second lookup.
type StampedReading struct {
	Reading
	IngestTimeMS int64 `json:"ingest_time_ms"`
}

// TelemetryTopic returns the MQTT topic a device publishes readings to.
func TelemetryTopic(deviceID string) string {
	return "sensegrid/v1/" + deviceID + "/telemetry"
}

// JetStreamSubject returns the JetStream subject the ingest bridge
// publishes a device's readings to, and the persistence consumer's
// filter subject reads from.
func JetStreamSubject(deviceID string) string {
	return "telemetry." + deviceID
}

// MetricsSubject returns the JetStream subject the Phase 3 windowed
// consumer publishes derived per-window metrics to.
func MetricsSubject(deviceID string) string {
	return "metrics." + deviceID
}

// AlertsSubject returns the JetStream subject alert fire/acknowledge/
// resolve events are published to for a device.
func AlertsSubject(deviceID string) string {
	return "alerts." + deviceID
}

// NewTraceID returns a W3C trace-id shaped correlation id: 32 lowercase
// hex characters, no dashes.
func NewTraceID() string {
	id := uuid.New()
	return hex.EncodeToString(id[:])
}
