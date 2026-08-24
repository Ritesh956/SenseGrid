// Command hostagent will become the laptop telemetry publisher (Phase 1):
// CPU, memory, battery, and WiFi RSSI, published over MQTT using the same
// payload schema as the phone PWA. It is meant to run natively on the
// developer's machine, not inside a container, so it can read real host
// metrics — see deploy/docker-compose.yml for why it is not a compose
// service. For now it establishes the service skeleton: config,
// structured logging, a /healthz endpoint, and graceful shutdown.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
)

const defaultHTTPAddr = ":8084"

func main() {
	cfg := config.Load("hostagent", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv, _ := httpserver.New(cfg.HTTPAddr)
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
