package main

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	messagesReceived   prometheus.Counter
	messagesPublished  prometheus.Counter
	validationFailures prometheus.Counter
	publishErrors      prometheus.Counter
	rateLimited        prometheus.Counter
	ingestLag          prometheus.Histogram
}

func newMetrics() *metrics {
	m := &metrics{
		messagesReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "messages_received_total",
			Help: "MQTT telemetry messages received.",
		}),
		messagesPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "messages_published_total",
			Help: "Messages successfully published to JetStream.",
		}),
		validationFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "validation_failures_total",
			Help: "Messages routed to the DLQ for failing schema validation.",
		}),
		publishErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "publish_errors_total",
			Help: "JetStream publish failures; the MQTT message is left unacked for redelivery.",
		}),
		rateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "rate_limited_total",
			Help: "Messages dropped by the per-device rate limiter (load-shed, not redelivered).",
		}),
		ingestLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "sensegrid", Subsystem: "ingest", Name: "lag_seconds",
			Help:    "broker_receive_time - device_time for each accepted reading.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 14), // 10ms .. ~82s
		}),
	}
	prometheus.MustRegister(
		m.messagesReceived, m.messagesPublished, m.validationFailures,
		m.publishErrors, m.rateLimited, m.ingestLag,
	)
	return m
}
