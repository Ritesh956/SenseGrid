package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel/attribute"

	"github.com/Ritesh956/SenseGrid/internal/telemetry"
	"github.com/Ritesh956/SenseGrid/internal/tracing"
)

type ingestHandler struct {
	js      jetstream.JetStream
	logger  *slog.Logger
	metrics *metrics
	limiter *perDeviceLimiter
}

// handle is the MQTT message callback. Manual acks are enabled on the
// client (see main.go): a message is only ever acked once it's either
// durably in JetStream or deliberately routed to the DLQ / dropped — a
// JetStream publish failure leaves it unacked so MQTT QoS 1 redelivers it,
// rather than silently losing it.
func (h *ingestHandler) handle(_ mqtt.Client, msg mqtt.Message) {
	ctx := context.Background()
	h.metrics.messagesReceived.Inc()
	ingestTime := time.Now()

	deviceID, ok := deviceIDFromTopic(msg.Topic())
	if !ok {
		h.logger.Warn("ingest: topic didn't match sensegrid/v1/{id}/telemetry, routing to DLQ", "topic", msg.Topic())
		h.publishDLQ(ctx, msg.Topic(), msg.Payload(), "unrecognized topic")
		msg.Ack()
		return
	}

	var reading telemetry.Reading
	if err := json.Unmarshal(msg.Payload(), &reading); err != nil {
		h.metrics.validationFailures.Inc()
		h.publishDLQ(ctx, msg.Topic(), msg.Payload(), "invalid json: "+err.Error())
		msg.Ack()
		return
	}
	if err := reading.Validate(); err != nil {
		h.metrics.validationFailures.Inc()
		h.publishDLQ(ctx, msg.Topic(), msg.Payload(), "schema validation: "+err.Error())
		msg.Ack()
		return
	}
	if reading.DeviceID != deviceID {
		// The MQTT ACL already guarantees this client owns the topic it
		// published to; this catches a payload that simply lies about
		// whose reading it is, independent of that.
		h.metrics.validationFailures.Inc()
		h.publishDLQ(ctx, msg.Topic(), msg.Payload(), "payload device_id does not match topic")
		msg.Ack()
		return
	}

	if !h.limiter.Allow(deviceID) {
		h.metrics.rateLimited.Inc()
		msg.Ack() // deliberate load-shed: not redelivered
		return
	}

	spanCtx := tracing.ContextWithReadingTrace(ctx, reading.TraceID)
	_, span := tracing.Tracer("sensegrid/ingest").Start(spanCtx, "ingest.publish")
	defer span.End()
	span.SetAttributes(
		attribute.String("device_id", deviceID),
		attribute.String("sensor_type", reading.SensorType),
		attribute.Int64("seq", int64(reading.Seq)),
	)

	lag := ingestTime.Sub(time.UnixMilli(reading.DeviceTimeMS))
	h.metrics.ingestLag.Observe(lag.Seconds())

	stamped := telemetry.StampedReading{Reading: reading, IngestTimeMS: ingestTime.UnixMilli()}
	body, err := json.Marshal(stamped)
	if err != nil {
		h.logger.Error("ingest: marshaling stamped reading", "err", err, "device_id", deviceID)
		span.RecordError(err)
		return // don't ack: unexpected, let redelivery retry rather than lose it
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = h.js.Publish(pubCtx, telemetry.JetStreamSubject(deviceID), body)
	cancel()
	if err != nil {
		h.metrics.publishErrors.Inc()
		h.logger.Error("ingest: jetstream publish failed, leaving unacked for MQTT redelivery", "err", err, "device_id", deviceID)
		span.RecordError(err)
		return
	}

	h.metrics.messagesPublished.Inc()
	msg.Ack()
}

func (h *ingestHandler) publishDLQ(ctx context.Context, topic string, payload []byte, reason string) {
	rec := map[string]any{
		"topic":       topic,
		"payload":     string(payload),
		"reason":      reason,
		"received_at": time.Now().UTC(),
	}
	body, err := json.Marshal(rec)
	if err != nil {
		h.logger.Error("ingest: marshaling DLQ record", "err", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := h.js.Publish(pubCtx, telemetry.DLQSubject, body); err != nil {
		h.logger.Error("ingest: publishing to DLQ failed", "err", err, "reason", reason)
	}
}

// deviceIDFromTopic extracts {id} from "sensegrid/v1/{id}/telemetry".
func deviceIDFromTopic(topic string) (string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 4 || parts[0] != "sensegrid" || parts[1] != "v1" || parts[3] != "telemetry" {
		return "", false
	}
	return parts[2], true
}
