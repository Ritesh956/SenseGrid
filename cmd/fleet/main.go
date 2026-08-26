// Command fleet is the Phase 7 synthetic device simulator used for load
// and chaos testing (see the SenseGrid Blueprint, §06 phase plan, P7). It
// is not part of the primary data path — the phone PWA and cmd/hostagent
// are the real telemetry sources — so unlike every other compose service
// it starts inert (FLEET_TARGET_DEVICES defaults to 0) and does nothing
// until deliberately scaled up via POST /fleet/scale, which is what
// test/chaos's scripts do.
//
// Each virtual device is a real client: its own MQTT connection under its
// own claimed device_id, its own config-topic subscription, its own
// shadow-state reporting — see device.go. manager.go owns scaling and
// chaos partitioning; controlapi.go is what test/chaos drives over HTTP.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
)

const defaultHTTPAddr = ":8083"

func main() {
	cfg := config.Load("fleet", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	fleetCfg := loadFleetConfig()
	logger.Info("starting", "config", cfg, "fleet_config", fleetCfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	caFile := cfg.TLSCAFile
	if caFile == "" {
		caFile = "deploy/certs/ca.pem" // default for `go run` from the repo root, matching cmd/hostagent
	}
	tlsCfg, err := tlsutil.FromCAFile(caFile)
	if err != nil {
		logger.Error("loading CA for MQTT connections failed", "err", err)
		os.Exit(1)
	}

	tokens, err := loadTokenPool(fleetCfg.TokensFile)
	if err != nil {
		logger.Error("loading token pool failed", "err", err)
		os.Exit(1)
	}
	logger.Info("token pool loaded", "tokens", tokens.remaining())

	metrics := newFleetMetrics()
	manager := NewFleetManager(ctx, fleetCfg, cfg.MQTTBrokerURL, caFile, tlsCfg, tokens, metrics, logger)

	srv, mux := httpserver.New(cfg.HTTPAddr)
	mux.Handle("/metrics", promhttp.Handler())
	registerFleetAPI(mux, manager, logger)

	if fleetCfg.InitialTarget > 0 {
		go manager.Scale(fleetCfg.InitialTarget)
	}

	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
