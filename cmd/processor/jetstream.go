package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// ensureStreams idempotently creates (or reconciles) the JetStream streams
// the Phase 3 windowed consumer produces to. cmd/ingest owns TELEMETRY/DLQ
// (it's their producer); processor owns METRICS/ALERTS for the same reason.
//
// METRICS: memory storage, 10-minute MaxAge. This is derived, high-frequency
// data (one message per window update, potentially every reading) with no
// durability requirement of its own — nothing before Phase 5's console
// subscribes to it, and the console will want it live, not replayed.
// Revisit if a later phase needs metrics replay; file storage would be the
// one-line change.
//
// ALERTS: file storage, matching TELEMETRY, because Postgres is the
// durable system of record for alerts (see internal/alerts.Store) but the
// JetStream copy still needs to survive a processor restart so an
// in-flight subscriber (Phase 5's console) doesn't miss a transition that
// happened while it briefly reconnected.
func ensureStreams(ctx context.Context, js jetstream.JetStream) error {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     telemetry.MetricsStreamName,
		Subjects: []string{"metrics.>"},
		Storage:  jetstream.MemoryStorage,
		MaxAge:   10 * time.Minute,
		MaxBytes: 100 << 20,
	}); err != nil {
		return fmt.Errorf("ensuring %s stream: %w", telemetry.MetricsStreamName, err)
	}

	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     telemetry.AlertsStreamName,
		Subjects: []string{"alerts.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
		MaxBytes: 100 << 20,
	}); err != nil {
		return fmt.Errorf("ensuring %s stream: %w", telemetry.AlertsStreamName, err)
	}

	return nil
}
