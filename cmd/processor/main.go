// Command processor runs two independent durable JetStream consumers of
// the TELEMETRY stream: the persistence consumer (consumer.go), batching
// readings into TimescaleDB, and the Phase 3 windowed consumer
// (windowed.go), maintaining sliding windows, publishing derived metrics,
// and running anomaly detection. It also runs the schema migrations at
// startup — both control and processor do this independently and
// idempotently rather than one depending on the other's startup order; see
// internal/migrations for how the (rare, cold-start-only) race between
// them is made harmless.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ritesh956/SenseGrid/internal/alerts"
	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/migrations"
	"github.com/Ritesh956/SenseGrid/internal/rules"
	"github.com/Ritesh956/SenseGrid/internal/tracing"
	"github.com/Ritesh956/SenseGrid/internal/window"
)

const defaultHTTPAddr = ":8082"

func main() {
	cfg := config.Load("processor", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	shutdownTracing, err := tracing.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		logger.Warn("tracing: init failed, continuing without traces", "err", err)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres: connecting", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	migrationsDir := os.Getenv("MIGRATIONS_DIR")
	if migrationsDir == "" {
		migrationsDir = "deploy/migrations"
	}
	migrateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = migrations.Run(migrateCtx, pool, migrationsDir)
	cancel()
	if err != nil {
		logger.Error("postgres: applying migrations", "err", err, "dir", migrationsDir)
		os.Exit(1)
	}
	logger.Info("postgres: migrations up to date")

	nc, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		logger.Error("nats: connect", "err", err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		logger.Error("jetstream: init", "err", err)
		os.Exit(1)
	}

	c := newConsumer(pool, logger, newMetrics())
	go func() {
		if err := c.run(ctx, js); err != nil {
			logger.Error("processor: consumer exited with error", "err", err)
			os.Exit(1)
		}
	}()

	if err := ensureStreams(ctx, js); err != nil {
		logger.Error("processor: ensuring METRICS/ALERTS streams", "err", err)
		os.Exit(1)
	}

	rulesWatcher, err := rules.NewWatcher(cfg.RulesFile, logger)
	if err != nil {
		logger.Error("processor: loading rules file", "err", err, "path", cfg.RulesFile)
		os.Exit(1)
	}
	go rulesWatcher.Run(ctx, cfg.RulesReloadInterval)

	registry := window.NewRegistry(cfg.WindowMaxCount, cfg.WindowMaxAge, cfg.WindowEWMAAlpha)
	alertsStore := alerts.NewStore(pool)
	alertsPub := alerts.NewPublisher(js)

	wc := newWindowedConsumer(js, registry, rulesWatcher, alertsStore, alertsPub, logger, newWindowedMetrics(),
		cfg.RegistryTTL, cfg.RegistrySweep)
	go func() {
		if err := wc.run(ctx, js); err != nil {
			logger.Error("processor: windowed consumer exited with error", "err", err)
			os.Exit(1)
		}
	}()

	srv, mux := httpserver.New(cfg.HTTPAddr)
	mux.Handle("/metrics", promhttp.Handler())
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
