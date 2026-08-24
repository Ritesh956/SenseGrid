// Command control is the control plane. In Phase 1 it does two things:
// issue one-time device registration tokens (CLI), and serve the device
// claim API plus the PWA sensor client's static files (HTTP). Device
// shadow, staged rollouts, and JWT-gated admin endpoints land in Phase 4.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/devicestore"
	"github.com/Ritesh956/SenseGrid/internal/dynsec"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
)

const (
	defaultHTTPAddr = ":8080"
	deviceRoleName  = "device"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "token" {
		if err := runTokenCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	runServer()
}

func runServer() {
	cfg := config.Load("control", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	store := devicestore.New(cfg.RedisAddr)
	defer store.Close()

	dynsecClient := connectDynsec(ctx, cfg, logger)
	if dynsecClient != nil {
		defer dynsecClient.Disconnect()
	}

	srv, mux := httpserver.New(cfg.HTTPAddr)
	registerClaimHandler(mux, logger, store, dynsecClient)
	registerStaticFiles(mux)

	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

// connectDynsec connects to the broker's dynamic-security control channel
// as admin and ensures the shared "device" role exists. A failure here is
// logged, not fatal: /healthz still succeeds so the container isn't killed
// over a broker hiccup, but the claim endpoint returns 503 until it
// recovers (see claim.go).
func connectDynsec(ctx context.Context, cfg config.Config, logger *slog.Logger) *dynsec.Client {
	if cfg.MQTTAdminUser == "" || cfg.MQTTAdminPass == "" {
		logger.Warn("MQTT_ADMIN_USERNAME/MQTT_ADMIN_PASSWORD not set, device claim endpoint will stay disabled")
		return nil
	}
	client, err := dynsec.Connect(dynsec.Config{
		BrokerURL: cfg.MQTTBrokerURL,
		CAFile:    cfg.TLSCAFile,
		Username:  cfg.MQTTAdminUser,
		Password:  cfg.MQTTAdminPass,
	})
	if err != nil {
		logger.Error("dynsec: connect failed, device claims unavailable until this recovers", "err", err)
		return nil
	}

	bootstrapCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.EnsureRole(bootstrapCtx, deviceRoleName, "SenseGrid provisioned device", []dynsec.ACL{
		{ACLType: "publishClientSend", Topic: "sensegrid/v1/%c/telemetry", Priority: -1, Allow: true},
		{ACLType: "publishClientSend", Topic: "sensegrid/v1/%c/state", Priority: -1, Allow: true},
		{ACLType: "subscribePattern", Topic: "sensegrid/v1/%c/config", Priority: -1, Allow: true},
		{ACLType: "publishClientReceive", Topic: "sensegrid/v1/%c/config", Priority: -1, Allow: true},
	}); err != nil {
		logger.Error("dynsec: bootstrap device role failed", "err", err)
		client.Disconnect()
		return nil
	}
	logger.Info("dynsec: device role ready")
	return client
}

func registerStaticFiles(mux *http.ServeMux) {
	webroot := os.Getenv("WEBROOT")
	if webroot == "" {
		webroot = "web/sensor-client"
	}
	mux.Handle("/", http.FileServer(http.Dir(webroot)))
}
