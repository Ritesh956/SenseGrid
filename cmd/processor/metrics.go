package main

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	messagesConsumed prometheus.Counter
	batchFailures    prometheus.Counter
	batchSize        prometheus.Histogram
	dbWriteDuration  prometheus.Histogram
	dbStageLatency   prometheus.Histogram
	endToEndLatency  prometheus.Histogram
	consumerLag      prometheus.Gauge
}

func newMetrics() *metrics {
	m := &metrics{
		messagesConsumed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "messages_consumed_total",
			Help: "JetStream messages consumed from the telemetry stream.",
		}),
		batchFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "batch_failures_total",
			Help: "Batch inserts that failed and were nak'ed for JetStream redelivery.",
		}),
		batchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "batch_size",
			Help:    "Rows per flushed batch insert.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1 .. 512
		}),
		dbWriteDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "db_write_duration_seconds",
			Help:    "Time spent executing the batch INSERT itself.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}),
		dbStageLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "db_stage_latency_seconds",
			Help:    "db_commit_time - ingest_time: the JetStream+processor+DB portion of the pipeline, isolated from device clock skew and network time.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 16),
		}),
		endToEndLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "end_to_end_latency_seconds",
			Help:    "db_commit_time - device_time: the Phase 2 baseline metric, device publish to durably queryable.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 16),
		}),
		consumerLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "processor", Name: "consumer_lag",
			Help: "NumPending on the persistence consumer (\"persistence\") — messages delivered but not yet acked, polled periodically via Consumer.Info.",
		}),
	}
	prometheus.MustRegister(
		m.messagesConsumed, m.batchFailures, m.batchSize,
		m.dbWriteDuration, m.dbStageLatency, m.endToEndLatency, m.consumerLag,
	)
	return m
}
