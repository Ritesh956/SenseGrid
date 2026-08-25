// Package tracing wires up OpenTelemetry for every hop that touches a
// telemetry.Reading (cmd/ingest, cmd/processor). It exists so a single
// reading's journey — device publish → broker receive → JetStream
// publish → processor consume → DB commit — can be pulled up as one
// trace, by the trace_id already carried in the payload (see
// internal/telemetry), rather than reconstructed after the fact from
// separate services' logs.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
)

// Init wires the global TracerProvider to export to an OTLP/gRPC endpoint
// (Jaeger, in this repo — see deploy/docker-compose.yml). An empty
// endpoint makes tracing a safe no-op, so every service can call Init
// unconditionally regardless of whether OTEL_EXPORTER_OTLP_ENDPOINT is set.
func Init(ctx context.Context, serviceName, endpoint string) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if endpoint == "" {
		return noop, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return noop, fmt.Errorf("tracing: creating OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", serviceName),
	))
	if err != nil {
		return noop, fmt.Errorf("tracing: building resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// Tracer is a small convenience wrapper so callers don't need to import
// go.opentelemetry.io/otel directly just to get a tracer.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// ContextWithReadingTrace returns a context carrying a synthetic remote
// span context derived from a telemetry.Reading's trace_id, so a span
// started from the returned context joins that same trace rather than
// starting a new one.
//
// There's no real span behind this: the PWA and hostagent don't run an
// OTel SDK, they just mint a random 32-hex trace_id and stamp it on every
// reading (MQTT 3.1.1 has no headers to carry a proper W3C traceparent).
// The synthetic span id is the trace_id's own last 16 hex characters —
// deterministic, always valid, and good enough as an anchor: what matters
// is that every hop's span shares the trace ID, not that the synthetic
// "device publish" span is a real one.
func ContextWithReadingTrace(ctx context.Context, traceIDHex string) context.Context {
	if len(traceIDHex) != 32 {
		return ctx
	}
	tid, err := trace.TraceIDFromHex(traceIDHex)
	if err != nil {
		return ctx
	}
	sid, err := trace.SpanIDFromHex(traceIDHex[16:])
	if err != nil {
		return ctx
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}
