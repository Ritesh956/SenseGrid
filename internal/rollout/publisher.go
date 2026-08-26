package rollout

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// EnsureStream idempotently creates (or reconciles) the ROLLOUTS stream,
// mirroring internal/shadow.EnsureBucket's / cmd/ingest/jetstream.go's
// pattern. File storage, matching ALERTS: like alert events, rollout
// events are low-volume and a future console subscriber (Phase 5)
// shouldn't miss one that happened during a brief reconnect.
func EnsureStream(ctx context.Context, js jetstream.JetStream) error {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     telemetry.RolloutsStreamName,
		Subjects: []string{"rollout.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
		MaxBytes: 100 << 20,
	}); err != nil {
		return fmt.Errorf("rollout: ensuring %s stream: %w", telemetry.RolloutsStreamName, err)
	}
	return nil
}

// EventPublisher is what Engine needs to publish rollout lifecycle events
// — satisfied by *Publisher (below) and by an in-memory fake in
// engine_test.go.
type EventPublisher interface {
	Publish(ctx context.Context, evt RolloutEvent) error
}

// Publisher publishes RolloutEvents to JetStream, mirroring
// internal/alerts.Publisher's shape.
type Publisher struct {
	js jetstream.JetStream
}

func NewPublisher(js jetstream.JetStream) *Publisher {
	return &Publisher{js: js}
}

func (p *Publisher) Publish(ctx context.Context, evt RolloutEvent) error {
	evt.SchemaVersion = SchemaVersion
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("rollout: marshaling event: %w", err)
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	subject := telemetry.RolloutEventsSubject(evt.RolloutID)
	if _, err := p.js.Publish(pubCtx, subject, body); err != nil {
		return fmt.Errorf("rollout: publishing to %s: %w", subject, err)
	}
	return nil
}
