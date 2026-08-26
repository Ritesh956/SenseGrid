package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Ritesh956/SenseGrid/internal/devices"
)

// metrics is Phase 6's instrumentation for cmd/control, mirroring
// cmd/ingest/metrics.go's shape exactly (a plain struct of label-less
// Counters/Gauges, one prometheus.MustRegister call) — the first
// Prometheus wiring cmd/control has had; Phases 1-5 built the REST/WS
// surface this now observes.
type metrics struct {
	wsClientsConnected prometheus.Gauge
	activeDevices      prometheus.Gauge
}

func newMetrics() *metrics {
	m := &metrics{
		wsClientsConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "control", Name: "ws_clients_connected",
			Help: "Console WebSocket connections currently open (GET /v1/ws).",
		}),
		activeDevices: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "sensegrid", Subsystem: "control", Name: "active_devices",
			Help: "Claimed devices whose last_seen is within the shadow-drift staleness threshold.",
		}),
	}
	prometheus.MustRegister(m.wsClientsConnected, m.activeDevices)
	return m
}

// runActiveDevicesGauge periodically recomputes activeDevices from the
// device registry until ctx is cancelled — the same shape as
// rollout.Engine.Run's own tick loop (main.go's `go rolloutEngine.Run(...)`),
// just for a metric instead of rollout state. "Active" reuses
// staleAfter (cfg.DriftStaleAfter, the same threshold internal/shadow.Drift
// already uses to decide a device has gone quiet) rather than introducing
// a second, competing definition of "still connected".
func runActiveDevicesGauge(ctx context.Context, deviceStore *devices.Store, m *metrics, staleAfter, tickInterval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			list, err := deviceStore.List(ctx)
			if err != nil {
				logger.Error("metrics: listing devices for active-devices gauge failed", "err", err)
				continue
			}
			now := time.Now()
			var active int
			for _, d := range list {
				if d.LastSeen != nil && now.Sub(*d.LastSeen) <= staleAfter {
					active++
				}
			}
			m.activeDevices.Set(float64(active))
		}
	}
}
