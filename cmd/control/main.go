// Command control is the control plane. Phase 1-3 gave it device claiming
// (CLI-issued tokens + POST /v1/devices/claim) and the PWA's static files.
// Phase 4 adds: JWT-gated REST endpoints (auth.go), the device shadow —
// JetStream KV-backed desired/reported config, retained-published to
// devices and reconciled from their state reports (internal/shadow) —
// alert ack/resolve (reusing internal/alerts, built for cmd/processor in
// Phase 3), and a staged rollout engine (internal/rollout) that pushes
// shadow config to a growing percentage of a device cohort over a
// sequence of bake periods, auto-advancing while healthy and
// auto-rolling-back on breach.
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

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ritesh956/SenseGrid/internal/alerts"
	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/devices"
	"github.com/Ritesh956/SenseGrid/internal/devicestore"
	"github.com/Ritesh956/SenseGrid/internal/dynsec"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/migrations"
	"github.com/Ritesh956/SenseGrid/internal/rollout"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
	"github.com/Ritesh956/SenseGrid/internal/users"
)

const (
	defaultHTTPAddr = ":8080"
	deviceRoleName  = "device"
	bridgeRoleName  = "bridge"
	controlRoleName = "control"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "token" {
		if err := runTokenCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "jwt" {
		if err := runJWTCLI(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "user" {
		if err := runUserCLI(os.Args[2:]); err != nil {
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

	tokens := devicestore.New(cfg.RedisAddr)
	defer tokens.Close()

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
	deviceStore := devices.New(pool)
	userStore := users.New(pool)

	if cfg.JWTSigningKey == "" {
		logger.Error("JWT_SIGNING_KEY is not set — required for Phase 4's auth-gated endpoints")
		os.Exit(1)
	}
	signingKey := []byte(cfg.JWTSigningKey)

	dynsecClient := connectDynsec(ctx, cfg, logger)
	if dynsecClient != nil {
		defer dynsecClient.Disconnect()
	}

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

	kv, err := shadow.EnsureBucket(ctx, js)
	if err != nil {
		logger.Error("shadow: ensuring bucket", "err", err)
		os.Exit(1)
	}
	shadowStore := shadow.NewStore(kv, pool)
	alertsStore := alerts.NewStore(pool)
	alertsPub := alerts.NewPublisher(js)

	if err := rollout.EnsureStream(ctx, js); err != nil {
		logger.Error("rollout: ensuring stream", "err", err)
		os.Exit(1)
	}
	rolloutStore := rollout.NewStore(pool)
	rolloutPub := rollout.NewPublisher(js)

	reconciler := shadow.NewReconciler(shadowStore, deviceStore, logger)
	controlMQTT, err := connectControlMQTT(cfg, logger, reconciler)
	var shadowPub *shadow.Publisher
	if err != nil {
		logger.Error("mqtt: control identity connect failed, shadow publish/reconcile unavailable until this recovers", "err", err)
	} else {
		defer controlMQTT.Disconnect(250)
		shadowPub = shadow.NewPublisher(controlMQTT)
	}

	// shadowPub is a *shadow.Publisher that may be nil (MQTT connect
	// failed above); assigning it directly to the DevicePublisher
	// interface parameter would produce a non-nil interface wrapping a
	// nil pointer (the classic Go typed-nil gotcha), which
	// rollout.Engine's own nil check couldn't catch. Only assign the
	// interface variable when the pointer is genuinely non-nil, so a
	// failed MQTT connect degrades the rollout engine the same way it
	// already degrades PUT /v1/devices/{id}/shadow/desired.
	var rolloutDevicePub rollout.DevicePublisher
	if shadowPub != nil {
		rolloutDevicePub = shadowPub
	}
	rolloutEngine := rollout.NewEngine(rolloutStore, shadowStore, rolloutDevicePub, deviceStore, alertsStore, rolloutPub, cfg.RolloutDisconnectStaleAfter, logger)
	if err := rolloutEngine.ResumeAll(ctx); err != nil {
		logger.Error("rollout: resuming in-flight rollouts failed", "err", err)
	}
	go rolloutEngine.Run(ctx, cfg.RolloutTickInterval)

	srv, mux := httpserver.New(cfg.HTTPAddr)
	registerClaimHandler(mux, logger, tokens, deviceStore, dynsecClient)
	registerDeviceHandlers(mux, logger, deviceStore, shadowStore, shadowPub, cfg.DriftStaleAfter, cfg.JWTIssuer, signingKey)
	registerAlertHandlers(mux, logger, alertsStore, alertsPub, cfg.JWTIssuer, signingKey)
	registerRolloutHandlers(mux, logger, rolloutEngine, cfg.JWTIssuer, signingKey)
	registerAuthHandlers(mux, logger, userStore, cfg.JWTIssuer, signingKey, cfg.JWTConsoleTTL)
	registerWSHandler(mux, logger, nc, cfg.JWTIssuer, signingKey)
	registerStaticFiles(mux)

	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

// connectControlMQTT connects cmd/control's own MQTT identity — the
// "control" role, wildcard publish on every device's config topic and
// wildcard subscribe on every device's state topic (see connectDynsec) —
// and starts reconciler on every successful (re)connect, matching
// cmd/hostagent's connectMQTT pattern for why that has to be an
// OnConnectHandler and not a one-time post-Connect call.
func connectControlMQTT(cfg config.Config, logger *slog.Logger, reconciler *shadow.Reconciler) (mqtt.Client, error) {
	if cfg.MQTTControlUser == "" || cfg.MQTTControlPass == "" {
		return nil, fmt.Errorf("MQTT_CONTROL_USERNAME/MQTT_CONTROL_PASSWORD not set")
	}
	caFile := cfg.TLSCAFile
	if caFile == "" {
		caFile = "deploy/certs/ca.pem"
	}
	tlsCfg, err := tlsutil.FromCAFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetClientID("control-plane").
		SetUsername(cfg.MQTTControlUser).
		SetPassword(cfg.MQTTControlPass).
		SetTLSConfig(tlsCfg).
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt: control identity connection lost, reconnecting with backoff", "err", err)
		})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logger.Info("mqtt: control identity connected")
		if err := reconciler.Start(c); err != nil {
			logger.Error("shadow: subscribing to state topic failed", "err", err)
		}
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return nil, fmt.Errorf("connect timed out")
	}
	if err := token.Error(); err != nil {
		return nil, err
	}
	return client, nil
}

// connectDynsec connects to the broker's dynamic-security control channel
// as admin and ensures the shared "device" role exists, plus the "bridge"
// role and cmd/ingest's own service account (a device is scoped to its own
// topic via %c substitution; the ingest bridge instead needs to subscribe
// across every device's telemetry, which needs its own role and its own
// long-lived credential — see deploy/docker-compose.yml's MQTT_BRIDGE_*
// vars, shared between control, which provisions this account, and
// ingest, which connects as it).
//
// A failure here is logged, not fatal: /healthz still succeeds so the
// container isn't killed over a broker hiccup, but the claim endpoint
// returns 503 until it recovers (see claim.go).
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

	if cfg.MQTTBridgeUser == "" || cfg.MQTTBridgePass == "" {
		logger.Warn("MQTT_BRIDGE_USERNAME/MQTT_BRIDGE_PASSWORD not set, skipping bridge role/account bootstrap")
		return client
	}
	// subscribeLiteral, not subscribePattern: the bridge subscribes with
	// the exact wildcard filter below (a fixed string, no %c to
	// substitute), so a literal match of that filter is both correct and
	// simpler than pattern matching.
	if err := client.EnsureRole(bootstrapCtx, bridgeRoleName, "SenseGrid ingest bridge", []dynsec.ACL{
		{ACLType: "subscribeLiteral", Topic: "sensegrid/v1/+/telemetry", Priority: -1, Allow: true},
		{ACLType: "publishClientReceive", Topic: "sensegrid/v1/+/telemetry", Priority: -1, Allow: true},
	}); err != nil {
		logger.Error("dynsec: bootstrap bridge role failed", "err", err)
		return client
	}
	if err := client.CreateClient(bootstrapCtx, cfg.MQTTBridgeUser, cfg.MQTTBridgePass, "ingest bridge service account", bridgeRoleName); err != nil {
		logger.Error("dynsec: bootstrap bridge account failed", "err", err)
		return client
	}
	logger.Info("dynsec: bridge role and account ready")

	if cfg.MQTTControlUser == "" || cfg.MQTTControlPass == "" {
		logger.Warn("MQTT_CONTROL_USERNAME/MQTT_CONTROL_PASSWORD not set, skipping control role/account bootstrap")
		return client
	}
	// Phase 4: cmd/control needs its own broker identity, distinct from
	// bridge, to retained-publish desired config to any device's config
	// topic and to receive every device's reported state — a device's own
	// "device" role ACL only lets *it* subscribe/publish its own topics
	// (see deviceRoleName's ACLs above), not push to or read from others.
	if err := client.EnsureRole(bootstrapCtx, controlRoleName, "SenseGrid control plane", []dynsec.ACL{
		{ACLType: "publishClientSend", Topic: "sensegrid/v1/+/config", Priority: -1, Allow: true},
		{ACLType: "subscribeLiteral", Topic: "sensegrid/v1/+/state", Priority: -1, Allow: true},
		{ACLType: "publishClientReceive", Topic: "sensegrid/v1/+/state", Priority: -1, Allow: true},
	}); err != nil {
		logger.Error("dynsec: bootstrap control role failed", "err", err)
		return client
	}
	if err := client.CreateClient(bootstrapCtx, cfg.MQTTControlUser, cfg.MQTTControlPass, "control plane service account", controlRoleName); err != nil {
		logger.Error("dynsec: bootstrap control account failed", "err", err)
		return client
	}
	logger.Info("dynsec: control role and account ready")
	return client
}

func registerStaticFiles(mux *http.ServeMux) {
	webroot := os.Getenv("WEBROOT")
	if webroot == "" {
		webroot = "web/sensor-client"
	}
	mux.Handle("/", http.FileServer(http.Dir(webroot)))
}
