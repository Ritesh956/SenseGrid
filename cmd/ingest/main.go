// Command ingest is the MQTT-to-NATS-JetStream bridge: it subscribes to
// every device's telemetry, validates each reading against the v1
// schema (internal/telemetry), stamps a broker-receive timestamp, and
// republishes onto JetStream for cmd/processor to persist. Malformed
// payloads go to a dead-letter subject instead of being dropped or
// crashing the service; a per-device token bucket sheds load from a
// runaway or misbehaving client rather than starving everyone else — see
// connectMQTT's SetOrderMatters(false) call for the part of that isolation
// that isn't the token bucket itself (Phase 8).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/telemetry"
	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
	"github.com/Ritesh956/SenseGrid/internal/tracing"
)

const (
	defaultHTTPAddr    = ":8081"
	telemetryFilter    = "sensegrid/v1/+/telemetry"
	rateLimitPerSecond = 100.0
	rateLimitBurst     = 200
)

func main() {
	cfg := config.Load("ingest", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	shutdownTracing, err := tracing.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		logger.Warn("tracing: init failed, continuing without traces", "err", err)
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

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
	if err := ensureStreams(ctx, js); err != nil {
		logger.Error("jetstream: ensuring streams", "err", err)
		os.Exit(1)
	}
	logger.Info("jetstream: streams ready", "streams", []string{telemetry.TelemetryStreamName, telemetry.DLQStreamName})

	h := &ingestHandler{
		js:      js,
		logger:  logger,
		metrics: newMetrics(),
		limiter: newPerDeviceLimiter(rateLimitPerSecond, rateLimitBurst),
	}

	mqttClient, err := connectMQTT(cfg, logger, h.handle)
	if err != nil {
		logger.Error("mqtt: connect", "err", err)
		os.Exit(1)
	}
	defer mqttClient.Disconnect(250)

	srv, mux := httpserver.New(cfg.HTTPAddr)
	mux.Handle("/metrics", promhttp.Handler())
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func connectMQTT(cfg config.Config, logger *slog.Logger, handler mqtt.MessageHandler) (mqtt.Client, error) {
	tlsCfg, err := tlsutil.FromCAFile(cfg.TLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetClientID(cfg.MQTTBridgeUser). // must match the clientid dynsec pinned this account to
		SetUsername(cfg.MQTTBridgeUser).
		SetPassword(cfg.MQTTBridgePass).
		SetTLSConfig(tlsCfg).
		SetAutoAckDisabled(true). // we ack explicitly once a reading is durably in JetStream (or deliberately dropped)
		// Order defaults to true, which routes every subscription's
		// messages through a single goroutine so callbacks fire in
		// receive order. Found live in Phase 8's rate-limiter load test:
		// with Order true, one device flooding the shared telemetry
		// filter monopolizes that single goroutine (JSON unmarshal +
		// validation run before ratelimit.go's per-device Allow() check
		// even rejects it), so every other device's messages queue up
		// behind it — the per-device token bucket can't help because the
		// messages never reach it in time. handle() doesn't depend on
		// cross-device or even same-device ordering (JetStream publish
		// is idempotent per (device_id, sensor_type, seq, time) — see
		// deploy/migrations/0002_readings.sql — and cmd/processor
		// tolerates out-of-order arrival), so disabling it and letting
		// paho dispatch each message on its own goroutine is what
		// actually gives the rate limiter room to isolate a runaway
		// device. See test/hardening/rate_limit_load.sh for the
		// before/after measurement.
		SetOrderMatters(false).
		SetConnectTimeout(10 * time.Second).
		// SetConnectRetry (distinct from SetAutoReconnect) is what makes the
		// *first* Connect() retry instead of failing outright — needed here
		// because ingest's bridge account doesn't exist until control's own
		// dynsec bootstrap has run, and container start order gives no
		// guarantee that's already happened.
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt: connection lost, reconnecting with backoff", "err", err)
		}).
		SetOnConnectHandler(func(c mqtt.Client) {
			// Re-subscribe on every (re)connect: a clean MQTT session
			// (the default) doesn't remember subscriptions across
			// reconnects.
			token := c.Subscribe(telemetryFilter, 1, handler)
			token.Wait()
			if err := token.Error(); err != nil {
				logger.Error("mqtt: subscribe failed", "err", err, "filter", telemetryFilter)
				return
			}
			logger.Info("mqtt: subscribed", "filter", telemetryFilter)
		})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	// With SetConnectRetry(true), this token only completes once connected
	// or the wait itself times out — give it real room to outlast
	// control's bootstrap sequence rather than a snappy timeout that
	// mostly just tests network latency.
	if !token.WaitTimeout(60 * time.Second) {
		return nil, fmt.Errorf("connect timed out after retries")
	}
	if err := token.Error(); err != nil {
		return nil, err
	}
	return client, nil
}
