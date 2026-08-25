package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// ensureStreams idempotently creates (or reconciles) the JetStream streams
// this service depends on.
//
// TELEMETRY: file storage, so a processor restart or brief outage doesn't
// lose anything already accepted from MQTT. MaxAge 24h / MaxBytes 1GiB are
// safety caps, not a target retention — Postgres is the durable store
// (see deploy/migrations' 7-day retention policy); JetStream's job here is
// only to bridge the gap until the persistence consumer catches up, so a
// day of buffer against a stuck consumer is already generous for Phase 2's
// scale. Revisit both numbers against Phase 7's load test, not before.
//
// DLQ: short-lived (6h) and small (100MiB) — it exists so malformed
// payloads are inspectable, not so they accumulate indefinitely.
func ensureStreams(ctx context.Context, js jetstream.JetStream) error {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     telemetry.TelemetryStreamName,
		Subjects: []string{"telemetry.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   24 * time.Hour,
		MaxBytes: 1 << 30,
	}); err != nil {
		return fmt.Errorf("ensuring %s stream: %w", telemetry.TelemetryStreamName, err)
	}

	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     telemetry.DLQStreamName,
		Subjects: []string{"dlq.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   6 * time.Hour,
		MaxBytes: 100 << 20,
	}); err != nil {
		return fmt.Errorf("ensuring %s stream: %w", telemetry.DLQStreamName, err)
	}

	return nil
}
