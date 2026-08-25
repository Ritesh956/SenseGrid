package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

const AlertEventSchemaVersion = "1.0"

// AlertEvent is what Publisher publishes to internal/telemetry.AlertsSubject
// on every state transition — the wire shape Phase 5's console (and anyone
// else subscribing to alerts.*) will consume.
type AlertEvent struct {
	SchemaVersion string         `json:"schema_version"`
	AlertID       string         `json:"alert_id"`
	DeviceID      string         `json:"device_id"`
	SensorType    string         `json:"sensor_type"`
	RuleName      string         `json:"rule_name"`
	Severity      string         `json:"severity"`
	State         State          `json:"state"`
	Detail        map[string]any `json:"detail,omitempty"`
	TimestampMS   int64          `json:"timestamp_ms"`
}

// Publisher publishes alert lifecycle events to JetStream.
type Publisher struct {
	js jetstream.JetStream
}

func NewPublisher(js jetstream.JetStream) *Publisher {
	return &Publisher{js: js}
}

func (p *Publisher) Publish(ctx context.Context, a *Alert, at time.Time) error {
	evt := AlertEvent{
		SchemaVersion: AlertEventSchemaVersion,
		AlertID:       a.ID,
		DeviceID:      a.DeviceID,
		SensorType:    a.SensorType,
		RuleName:      a.RuleName,
		Severity:      a.Severity,
		State:         a.State,
		Detail:        a.Detail,
		TimestampMS:   at.UnixMilli(),
	}
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("alerts: marshaling event: %w", err)
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := p.js.Publish(pubCtx, telemetry.AlertsSubject(a.DeviceID), body); err != nil {
		return fmt.Errorf("alerts: publishing to %s: %w", telemetry.AlertsSubject(a.DeviceID), err)
	}
	return nil
}
