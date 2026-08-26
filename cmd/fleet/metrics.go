package main

import "github.com/prometheus/client_golang/prometheus"

// fleetMetrics mirrors the label-less Counter/Gauge convention every other
// service's metrics.go already follows (see cmd/control/metrics.go's doc
// comment) — no *Vec here either, even though a per-device label would be
// tempting at fleet scale: 1000 devices would mean 1000 series for one
// gauge, which is exactly the cardinality blowup the convention exists to
// avoid. Per-device detail is what GET /fleet/status is for.
type fleetMetrics struct {
	devicesTarget      prometheus.Gauge
	devicesRunning     prometheus.Gauge
	devicesConnected   prometheus.Gauge
	devicesPartitioned prometheus.Gauge
	tokensRemaining    prometheus.Gauge

	publishedTotal     prometheus.Counter
	publishErrorsTotal prometheus.Counter
	malformedSentTotal prometheus.Counter
	anomaliesSentTotal prometheus.Counter
	reconnectsTotal    prometheus.Counter
}

func newFleetMetrics() *fleetMetrics {
	m := &fleetMetrics{
		devicesTarget: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "devices_target",
			Help: "Devices the fleet is currently asked to keep running (last POST /fleet/scale).",
		}),
		devicesRunning: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "devices_running",
			Help: "Devices with an active sample loop (claimed and not scaled down).",
		}),
		devicesConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "devices_connected",
			Help: "Running devices currently holding an open MQTT connection.",
		}),
		devicesPartitioned: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "devices_partitioned",
			Help: "Devices currently in a simulated network partition (POST /fleet/partition).",
		}),
		tokensRemaining: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "tokens_remaining",
			Help: "Unused registration tokens left in the bulk-issued token pool.",
		}),
		publishedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "readings_published_total",
			Help: "Telemetry readings successfully published across the whole fleet.",
		}),
		publishErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "publish_errors_total",
			Help: "Publish attempts skipped or failed (device not connected, marshal error).",
		}),
		malformedSentTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "malformed_sent_total",
			Help: "Deliberately malformed payloads sent (FLEET_MALFORMED_RATE / POST /fleet/config).",
		}),
		anomaliesSentTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "anomalies_sent_total",
			Help: "Injected anomaly spikes sent (FLEET_ANOMALY_RATE / POST /fleet/config).",
		}),
		reconnectsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "fleet", Name: "reconnects_total",
			Help: "Reconnects across the fleet: partition heals plus voluntary flaky-connection drops.",
		}),
	}
	prometheus.MustRegister(
		m.devicesTarget, m.devicesRunning, m.devicesConnected, m.devicesPartitioned, m.tokensRemaining,
		m.publishedTotal, m.publishErrorsTotal, m.malformedSentTotal, m.anomaliesSentTotal, m.reconnectsTotal,
	)
	return m
}
