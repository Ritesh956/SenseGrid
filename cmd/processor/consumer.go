// The persistence consumer: a durable JetStream pull consumer that
// batches readings into TimescaleDB. Batches flush on size or a max
// delay, whichever comes first (see batchSize/batchMaxDelay below).
// Messages are only acked after the batch's INSERT commits — a failed
// batch is nak'ed with a delay so JetStream redelivers it, never silently
// dropped. Redelivery-safety comes from the ON CONFLICT DO NOTHING in
// insertBatch, keyed to the same (device_id, sensor_type, seq, time)
// unique index Phase 2's migration created for exactly this purpose.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
	"github.com/Ritesh956/SenseGrid/internal/tracing"
)

const (
	batchSize     = 100
	batchMaxDelay = 500 * time.Millisecond
	consumerName  = "persistence"

	// consumerLagPollInterval is deliberately its own ticker, not folded
	// into batchMaxDelay's — lag polling is a Consumer.Info round trip to
	// JetStream, unrelated to (and much less latency-sensitive than) the
	// batch flush cadence.
	consumerLagPollInterval = 5 * time.Second
)

// flatRow is one row bound for the readings hypertable. A scalar Reading
// produces one flatRow; a vector Reading (e.g. accel {x,y,z}) produces one
// per component, sensor_type suffixed ("accel.x") — see flatten below for
// why the DB schema stays single-scalar-column instead of growing a jsonb
// values column.
type flatRow struct {
	time       time.Time
	deviceID   string
	sensorType string
	value      float64
	deviceTime time.Time
	ingestTime time.Time
	seq        int64
}

// pendingItem tracks one original JetStream message through a batch, for
// acking and latency metrics once the batch it's part of commits.
type pendingItem struct {
	msg        jetstream.Msg
	deviceTime time.Time
	ingestTime time.Time
	traceID    string
}

type consumer struct {
	pool    *pgxpool.Pool
	logger  *slog.Logger
	metrics *metrics

	mu    sync.Mutex
	rows  []flatRow
	items []pendingItem
}

func newConsumer(pool *pgxpool.Pool, logger *slog.Logger, m *metrics) *consumer {
	return &consumer{pool: pool, logger: logger, metrics: m}
}

// run creates (or reconnects to) the durable pull consumer and processes
// messages until ctx is cancelled.
func (c *consumer) run(ctx context.Context, js jetstream.JetStream) error {
	stream, err := js.Stream(ctx, telemetry.TelemetryStreamName)
	if err != nil {
		return fmt.Errorf("looking up %s stream: %w", telemetry.TelemetryStreamName, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: "telemetry.>",
	})
	if err != nil {
		return fmt.Errorf("creating durable consumer %q: %w", consumerName, err)
	}

	consumeCtx, err := cons.Consume(c.onMessage)
	if err != nil {
		return fmt.Errorf("starting consume: %w", err)
	}
	defer consumeCtx.Stop()

	ticker := time.NewTicker(batchMaxDelay)
	defer ticker.Stop()
	lagTicker := time.NewTicker(consumerLagPollInterval)
	defer lagTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush(context.Background()) // best-effort drain on shutdown
			return nil
		case <-ticker.C:
			c.flush(ctx)
		case <-lagTicker.C:
			c.pollLag(ctx, cons)
		}
	}
}

// pollLag sets the consumer_lag gauge from JetStream's own view of how far
// behind this consumer is (NumPending: delivered-or-deliverable messages
// not yet acked) — Phase 6's one genuinely new signal, everything else this
// phase adds is exposure of metrics that already existed.
func (c *consumer) pollLag(ctx context.Context, cons jetstream.Consumer) {
	info, err := cons.Info(ctx)
	if err != nil {
		c.logger.Warn("processor: polling consumer info for lag gauge failed", "err", err)
		return
	}
	c.metrics.consumerLag.Set(float64(info.NumPending))
}

func (c *consumer) onMessage(msg jetstream.Msg) {
	c.metrics.messagesConsumed.Inc()

	var sr telemetry.StampedReading
	if err := json.Unmarshal(msg.Data(), &sr); err != nil {
		// The ingest bridge already validated this before publishing it —
		// getting here means either a bug or data corruption in transit,
		// neither of which redelivery fixes. Terminate rather than nak so
		// it doesn't loop forever.
		c.logger.Error("processor: unparseable message from jetstream, terminating", "err", err)
		_ = msg.Term()
		return
	}

	deviceTime := time.UnixMilli(sr.DeviceTimeMS)
	ingestTime := time.UnixMilli(sr.IngestTimeMS)

	c.mu.Lock()
	c.rows = append(c.rows, flatten(sr, ingestTime)...)
	c.items = append(c.items, pendingItem{msg: msg, deviceTime: deviceTime, ingestTime: ingestTime, traceID: sr.TraceID})
	shouldFlush := len(c.rows) >= batchSize
	c.mu.Unlock()

	if shouldFlush {
		c.flush(context.Background())
	}
}

func (c *consumer) flush(ctx context.Context) {
	c.mu.Lock()
	rows := c.rows
	items := c.items
	c.rows = nil
	c.items = nil
	c.mu.Unlock()

	if len(rows) == 0 {
		return
	}

	start := time.Now()
	err := insertBatch(ctx, c.pool, rows)
	c.metrics.batchSize.Observe(float64(len(rows)))
	c.metrics.dbWriteDuration.Observe(time.Since(start).Seconds())

	if err != nil {
		c.metrics.batchFailures.Inc()
		c.logger.Error("processor: batch insert failed, nak'ing for redelivery", "err", err, "rows", len(rows), "messages", len(items))
		for _, item := range items {
			_ = item.msg.NakWithDelay(2 * time.Second)
		}
		return
	}

	commitTime := time.Now()
	for _, item := range items {
		_, span := tracing.Tracer("sensegrid/processor").Start(
			tracing.ContextWithReadingTrace(ctx, item.traceID), "processor.persist")
		span.SetAttributes(attribute.Int64("db_write_duration_ms", commitTime.Sub(start).Milliseconds()))
		span.End()

		c.metrics.endToEndLatency.Observe(commitTime.Sub(item.deviceTime).Seconds())
		c.metrics.dbStageLatency.Observe(commitTime.Sub(item.ingestTime).Seconds())
		_ = item.msg.Ack()
	}
}

// flatten expands a StampedReading into one or more flatRows. Vector
// readings (Values, e.g. accelerometer {x,y,z}) become one row per
// component with sensor_type suffixed ("accel.x") rather than the DB
// schema growing a second, jsonb-typed value column: every sensor_type
// then aggregates identically (avg/min/max on one double precision
// column), which keeps the continuous aggregates in 0003_continuous_
// aggregates.sql uniform instead of special-casing vector sensors.
func flatten(sr telemetry.StampedReading, t time.Time) []flatRow {
	deviceTime := time.UnixMilli(sr.DeviceTimeMS)
	seq := int64(sr.Seq)

	if sr.Value != nil {
		return []flatRow{{
			time: t, deviceID: sr.DeviceID, sensorType: sr.SensorType,
			value: *sr.Value, deviceTime: deviceTime, ingestTime: t, seq: seq,
		}}
	}
	rows := make([]flatRow, 0, len(sr.Values))
	for axis, v := range sr.Values {
		rows = append(rows, flatRow{
			time: t, deviceID: sr.DeviceID, sensorType: sr.SensorType + "." + axis,
			value: v, deviceTime: deviceTime, ingestTime: t, seq: seq,
		})
	}
	return rows
}

func insertBatch(ctx context.Context, pool *pgxpool.Pool, rows []flatRow) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO readings (time, device_id, sensor_type, value, device_time, ingest_time, seq) VALUES ")
	args := make([]any, 0, len(rows)*7)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		base := i * 7
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5, base+6, base+7)
		args = append(args, r.time, r.deviceID, r.sensorType, r.value, r.deviceTime, r.ingestTime, r.seq)
	}
	sb.WriteString(" ON CONFLICT (device_id, sensor_type, seq, time) DO NOTHING")

	_, err := pool.Exec(ctx, sb.String(), args...)
	return err
}
