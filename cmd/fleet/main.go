// Command fleet will become the synthetic device simulator used for load
// and chaos testing (Phase 7). For now it establishes the service
// skeleton: config, structured logging, a /healthz endpoint, and graceful
// shutdown.
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

const defaultHTTPAddr = ":8083"

func main() {
	cfg := config.Load("fleet", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	srv, _ := httpserver.New(cfg.HTTPAddr)
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
