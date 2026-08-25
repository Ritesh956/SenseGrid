// The Phase 3 windowed consumer: a second durable JetStream pull consumer
// of the same TELEMETRY stream the persistence consumer reads (see
// consumer.go), maintaining one sliding window per (device_id,
// sensor_type), publishing a MetricEvent on every update, running the
// configured detectors against it, and driving the alert state machine on
// fire/resolve transitions.
//
// This consumer acks messages unconditionally after processing (even if a
// detector/alert step failed and only got logged) rather than nak'ing like
// the persistence consumer does on a DB failure: windowed/alert output is
// derived, best-effort, real-time data, not the durable record (that's
// still Postgres via the persistence consumer) — getting stuck endlessly
// redelivering the same message would stall live detection for everything
// behind it, which is worse than dropping one detector evaluation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"

	"github.com/Ritesh956/SenseGrid/internal/alerts"
	"github.com/Ritesh956/SenseGrid/internal/anomaly"
	"github.com/Ritesh956/SenseGrid/internal/rules"
	"github.com/Ritesh956/SenseGrid/internal/telemetry"
	"github.com/Ritesh956/SenseGrid/internal/tracing"
	"github.com/Ritesh956/SenseGrid/internal/window"
)

const windowedConsumerName = "windowing"

type windowedConsumer struct {
	js        jetstream.JetStream
	registry  *window.Registry
	rulesW    *rules.Watcher
	evaluator *anomaly.Evaluator
	alertsSt  *alerts.Store
	alertsPub *alerts.Publisher
	logger    *slog.Logger
	metrics   *windowedMetrics

	registryTTL   time.Duration
	sweepInterval time.Duration
}

func newWindowedConsumer(js jetstream.JetStream, registry *window.Registry, rulesW *rules.Watcher,
	alertsSt *alerts.Store, alertsPub *alerts.Publisher, logger *slog.Logger, m *windowedMetrics,
	registryTTL, sweepInterval time.Duration) *windowedConsumer {
	return &windowedConsumer{
		js: js, registry: registry, rulesW: rulesW, evaluator: anomaly.NewEvaluator(),
		alertsSt: alertsSt, alertsPub: alertsPub, logger: logger, metrics: m,
		registryTTL: registryTTL, sweepInterval: sweepInterval,
	}
}

func (c *windowedConsumer) run(ctx context.Context, js jetstream.JetStream) error {
	stream, err := js.Stream(ctx, telemetry.TelemetryStreamName)
	if err != nil {
		return fmt.Errorf("looking up %s stream: %w", telemetry.TelemetryStreamName, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       windowedConsumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: "telemetry.>",
	})
	if err != nil {
		return fmt.Errorf("creating durable consumer %q: %w", windowedConsumerName, err)
	}

	consumeCtx, err := cons.Consume(c.onMessage)
	if err != nil {
		return fmt.Errorf("starting consume: %w", err)
	}
	defer consumeCtx.Stop()

	sweep := time.NewTicker(c.sweepInterval)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sweep.C:
			c.sweepSilence(ctx)
			evicted := c.registry.Sweep(time.Now(), c.registryTTL)
			c.metrics.registryEvictions.Add(float64(evicted))
			c.metrics.registrySize.Set(float64(c.registry.Size()))
		}
	}
}

func (c *windowedConsumer) onMessage(msg jetstream.Msg) {
	c.metrics.messagesConsumed.Inc()
	defer func() { _ = msg.Ack() }()

	var sr telemetry.StampedReading
	if err := json.Unmarshal(msg.Data(), &sr); err != nil {
		c.logger.Error("windowed: unparseable message, skipping", "err", err)
		return
	}

	deviceTime := time.UnixMilli(sr.DeviceTimeMS)
	ctx, span := tracing.Tracer("sensegrid/processor-windowed").Start(
		tracing.ContextWithReadingTrace(context.Background(), sr.TraceID), "processor.window")
	defer span.End()
	span.SetAttributes(attribute.String("device_id", sr.DeviceID), attribute.String("sensor_type", sr.SensorType))

	for sensorType, value := range flattenValues(sr) {
		c.processSample(ctx, sr.DeviceID, sensorType, value, deviceTime, sr.Seq, sr.TraceID)
	}
}

// flattenValues mirrors consumer.go's flatten: a scalar Reading is one
// (sensor_type, value) pair, a vector Reading is one per component
// ("accel.x", "accel.y", ...).
func flattenValues(sr telemetry.StampedReading) map[string]float64 {
	if sr.Value != nil {
		return map[string]float64{sr.SensorType: *sr.Value}
	}
	out := make(map[string]float64, len(sr.Values))
	for axis, v := range sr.Values {
		out[sr.SensorType+"."+axis] = v
	}
	return out
}

func (c *windowedConsumer) processSample(ctx context.Context, deviceID, sensorType string, value float64, deviceTime time.Time, seq uint64, traceID string) {
	key := window.Key{DeviceID: deviceID, SensorType: sensorType}
	w := c.registry.Get(key, time.Now())

	if !w.Insert(deviceTime, value, seq) {
		return // redelivered duplicate, already folded into the window
	}

	c.publishMetric(ctx, deviceID, sensorType, w, traceID)

	cfg := c.rulesW.Current()
	for _, rule := range cfg.ForSensor(sensorType) {
		if rule.Type == rules.Silence {
			continue // evaluated on the periodic sweep, not per-message — see sweepSilence
		}
		c.metrics.detectorEvals.Inc()

		var violating bool
		var score float64
		switch rule.Type {
		case rules.ZScore:
			violating, score = anomaly.EvaluateZScore(value, w, rule)
		case rules.RateOfChange:
			violating, score = anomaly.EvaluateRateOfChange(w, rule)
		default:
			c.logger.Warn("windowed: unknown detector type, skipping rule", "rule", rule.Name, "type", rule.Type)
			continue
		}

		c.applyEvaluation(ctx, deviceID, sensorType, rule, violating, score)
	}
}

// sweepSilence runs the silence detector over every device/sensor the
// registry currently knows about, since (unlike zscore/rate_of_change) it
// must fire on the *absence* of a message, not in response to one.
func (c *windowedConsumer) sweepSilence(ctx context.Context) {
	cfg := c.rulesW.Current()
	now := time.Now()
	for _, key := range c.registry.Keys() {
		lastSeen, ok := c.registry.LastSeen(key)
		if !ok {
			continue
		}
		for _, rule := range cfg.ForSensor(key.SensorType) {
			if rule.Type != rules.Silence {
				continue
			}
			c.metrics.detectorEvals.Inc()
			violating, gap := anomaly.EvaluateSilence(lastSeen, now, rule)
			c.applyEvaluation(ctx, key.DeviceID, key.SensorType, rule, violating, gap.Seconds())
		}
	}
}

func (c *windowedConsumer) applyEvaluation(ctx context.Context, deviceID, sensorType string, rule rules.Rule, violating bool, score float64) {
	evalKey := anomaly.Key{DeviceID: deviceID, SensorType: sensorType, RuleName: rule.Name}
	switch c.evaluator.Update(evalKey, violating, rule.ConsecutiveOrDefault()) {
	case anomaly.Fired:
		c.fire(ctx, deviceID, sensorType, rule, score)
	case anomaly.Cleared:
		c.resolve(ctx, deviceID, sensorType, rule)
	}
}

func (c *windowedConsumer) fire(ctx context.Context, deviceID, sensorType string, rule rules.Rule, score float64) {
	detail := map[string]any{"score": score, "threshold": rule.Threshold, "detector": string(rule.Type)}
	a, opened, err := c.alertsSt.Open(ctx, uuid.NewString(), deviceID, sensorType, rule.Name, rule.Severity, detail, time.Now())
	if err != nil {
		c.logger.Error("windowed: opening alert failed", "err", err, "device_id", deviceID, "sensor_type", sensorType, "rule", rule.Name)
		return
	}
	if opened {
		c.metrics.alertsFired.Inc()
		c.logger.Info("windowed: alert fired", "device_id", deviceID, "sensor_type", sensorType, "rule", rule.Name, "score", score)
	}
	if err := c.alertsPub.Publish(ctx, a, time.Now()); err != nil {
		c.metrics.publishErrors.Inc()
		c.logger.Error("windowed: publishing alert event failed", "err", err)
	}
}

func (c *windowedConsumer) resolve(ctx context.Context, deviceID, sensorType string, rule rules.Rule) {
	a, err := c.alertsSt.Resolve(ctx, deviceID, sensorType, rule.Name, time.Now())
	if err != nil {
		c.logger.Error("windowed: resolving alert failed", "err", err, "device_id", deviceID, "sensor_type", sensorType, "rule", rule.Name)
		return
	}
	if a == nil {
		return // nothing was open; nothing to publish
	}
	c.metrics.alertsResolved.Inc()
	c.logger.Info("windowed: alert resolved", "device_id", deviceID, "sensor_type", sensorType, "rule", rule.Name)
	if err := c.alertsPub.Publish(ctx, a, time.Now()); err != nil {
		c.metrics.publishErrors.Inc()
		c.logger.Error("windowed: publishing alert event failed", "err", err)
	}
}

func (c *windowedConsumer) publishMetric(ctx context.Context, deviceID, sensorType string, w *window.Window, traceID string) {
	evt := window.MetricEvent{
		SchemaVersion: window.MetricEventSchemaVersion,
		DeviceID:      deviceID,
		SensorType:    sensorType,
		Mean:          w.Mean(),
		StdDev:        w.StdDev(),
		Count:         w.Count(),
		EWMA:          w.EWMA(),
		WindowStartMS: w.OldestTime().UnixMilli(),
		WindowEndMS:   w.NewestTime().UnixMilli(),
		TraceID:       traceID,
	}
	body, err := json.Marshal(evt)
	if err != nil {
		c.logger.Error("windowed: marshaling metric event", "err", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.js.Publish(pubCtx, telemetry.MetricsSubject(deviceID), body); err != nil {
		c.metrics.publishErrors.Inc()
		c.logger.Error("windowed: publishing metric event failed", "err", err, "device_id", deviceID)
		return
	}
	c.metrics.metricsPublished.Inc()
}
