package main

import "github.com/prometheus/client_golang/prometheus"

// windowedMetrics covers the Phase 3 windowed consumer, kept separate from
// metrics.go's persistence-consumer metrics so each file stays scoped to
// one consumer's concerns.
type windowedMetrics struct {
	messagesConsumed  prometheus.Counter
	metricsPublished  prometheus.Counter
	publishErrors     prometheus.Counter
	detectorEvals     prometheus.Counter
	alertsFired       prometheus.Counter
	alertsResolved    prometheus.Counter
	registrySize      prometheus.Gauge
	registryEvictions prometheus.Counter
}

func newWindowedMetrics() *windowedMetrics {
	m := &windowedMetrics{
		messagesConsumed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "windowed_messages_consumed_total",
			Help: "JetStream messages consumed by the windowed (Phase 3) consumer.",
		}),
		metricsPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "metrics_published_total",
			Help: "MetricEvent messages published to metrics.*.",
		}),
		publishErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "windowed_publish_errors_total",
			Help: "Failed publishes to metrics.* or alerts.*.",
		}),
		detectorEvals: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "detector_evaluations_total",
			Help: "Individual rule evaluations across all detectors.",
		}),
		alertsFired: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "alerts_fired_total",
			Help: "Alert-fire transitions.",
		}),
		alertsResolved: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "alerts_resolved_total",
			Help: "Alert-resolve transitions.",
		}),
		registrySize: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "window_registry_size",
			Help: "Live (device, sensor) windows currently tracked in memory.",
		}),
		registryEvictions: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "window_registry_evictions_total",
			Help: "Windows evicted from the registry for exceeding the silent-device TTL.",
		}),
	}
	prometheus.MustRegister(
		m.messagesConsumed, m.metricsPublished, m.publishErrors, m.detectorEvals,
		m.alertsFired, m.alertsResolved, m.registrySize, m.registryEvictions,
	)
	return m
}
