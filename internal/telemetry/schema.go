// Package telemetry is the v1 payload schema (see the SenseGrid Blueprint,
// §04 Data contracts) shared by every native Go publisher — cmd/hostagent
// now, cmd/fleet and cmd/ingest's validator in later phases — so the wire
// format has exactly one definition instead of drifting per service. The
// PWA sensor client (JavaScript, no shared module system with Go) mirrors
// this shape by hand in web/sensor-client/app.js.
package telemetry

import (
	"encoding/hex"

	"github.com/google/uuid"
)

const SchemaVersion = "1.0"

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

// TelemetryTopic returns the MQTT topic a device publishes readings to.
func TelemetryTopic(deviceID string) string {
	return "sensegrid/v1/" + deviceID + "/telemetry"
}

// NewTraceID returns a W3C trace-id shaped correlation id: 32 lowercase
// hex characters, no dashes.
func NewTraceID() string {
	id := uuid.New()
	return hex.EncodeToString(id[:])
}
