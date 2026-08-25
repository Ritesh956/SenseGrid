// Package config loads service configuration from environment variables,
// falling back to sane defaults so every binary runs out of the box against
// the local docker-compose stack.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config holds settings shared by every SenseGrid service. Individual
// services read only the fields they need; unused fields cost nothing.
type Config struct {
	ServiceName string
	Environment string
	LogLevel    string

	HTTPAddr        string
	HTTPTLSCertFile string
	HTTPTLSKeyFile  string
	ShutdownTimeout time.Duration

	MQTTBrokerURL   string
	MQTTAdminUser   string
	MQTTAdminPass   string
	MQTTBridgeUser  string
	MQTTBridgePass  string
	MQTTControlUser string
	MQTTControlPass string
	NATSURL         string
	PostgresDSN     string
	RedisAddr       string
	TLSCAFile       string
	OTLPEndpoint    string

	// JWT settings — cmd/control (Phase 4). No login endpoint exists yet
	// (see internal/config's sibling doc in cmd/control/jwt_cli.go); tokens
	// are minted by the `control jwt create` CLI using this same signing
	// key, so the running server and the CLI must read it from the same
	// place, which Load already guarantees.
	JWTSigningKey string
	JWTIssuer     string
	JWTAccessTTL  time.Duration

	// DriftStaleAfter is how long a device can go without a state report
	// before GET /v1/devices/drift considers it drifted even if its last
	// applied_revision matched — see internal/shadow.Drift.
	DriftStaleAfter time.Duration

	RulesFile           string
	RulesReloadInterval time.Duration

	// WindowMaxCount/WindowMaxAge/WindowEWMAAlpha size the single shared
	// sliding window kept per (device, sensor) — see internal/window and
	// internal/rules' doc comment on why this is a process-wide setting
	// rather than a per-rule one.
	WindowMaxCount  int
	WindowMaxAge    time.Duration
	WindowEWMAAlpha float64
	RegistryTTL     time.Duration
	RegistrySweep   time.Duration
}

// Load builds a Config for serviceName, using defaultHTTPAddr when HTTP_ADDR
// is not set in the environment.
func Load(serviceName, defaultHTTPAddr string) Config {
	return Config{
		ServiceName: serviceName,
		Environment: getEnv("SENSEGRID_ENV", "dev"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),

		HTTPAddr:        getEnv("HTTP_ADDR", defaultHTTPAddr),
		HTTPTLSCertFile: getEnv("HTTP_TLS_CERT_FILE", ""),
		HTTPTLSKeyFile:  getEnv("HTTP_TLS_KEY_FILE", ""),
		ShutdownTimeout: getDuration("SHUTDOWN_TIMEOUT", 10*time.Second),

		MQTTBrokerURL:   getEnv("MQTT_BROKER_URL", "tls://localhost:8883"),
		MQTTAdminUser:   getEnv("MQTT_ADMIN_USERNAME", ""),
		MQTTAdminPass:   getEnv("MQTT_ADMIN_PASSWORD", ""),
		MQTTBridgeUser:  getEnv("MQTT_BRIDGE_USERNAME", "ingest-bridge"),
		MQTTBridgePass:  getEnv("MQTT_BRIDGE_PASSWORD", ""),
		MQTTControlUser: getEnv("MQTT_CONTROL_USERNAME", "control-plane"),
		MQTTControlPass: getEnv("MQTT_CONTROL_PASSWORD", ""),
		NATSURL:         getEnv("NATS_URL", "nats://localhost:4222"),
		PostgresDSN:     getEnv("POSTGRES_DSN", "postgres://sensegrid:sensegrid@localhost:5432/sensegrid?sslmode=disable"),
		RedisAddr:       getEnv("REDIS_ADDR", "localhost:6379"),
		TLSCAFile:       getEnv("TLS_CA_FILE", ""),
		OTLPEndpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),

		JWTSigningKey: getEnv("JWT_SIGNING_KEY", ""),
		JWTIssuer:     getEnv("JWT_ISSUER", "sensegrid-control"),
		JWTAccessTTL:  getDuration("JWT_ACCESS_TTL", 15*time.Minute),

		DriftStaleAfter: getDuration("SHADOW_DRIFT_STALE_AFTER", 2*time.Minute),

		RulesFile:           getEnv("RULES_FILE", "deploy/rules.yaml"),
		RulesReloadInterval: getDuration("RULES_RELOAD_INTERVAL", 5*time.Second),

		WindowMaxCount:  getInt("WINDOW_MAX_COUNT", 100),
		WindowMaxAge:    getDuration("WINDOW_MAX_AGE", 30*time.Second),
		WindowEWMAAlpha: getFloat("WINDOW_EWMA_ALPHA", 0.3),
		RegistryTTL:     getDuration("WINDOW_REGISTRY_TTL", 10*time.Minute),
		RegistrySweep:   getDuration("WINDOW_REGISTRY_SWEEP_INTERVAL", 1*time.Minute),
	}
}

// LogValue redacts the Postgres DSN (which may carry a password) when a
// Config is logged via slog, e.g. logger.Info("config", "config", cfg).
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment", c.Environment),
		slog.String("log_level", c.LogLevel),
		slog.String("http_addr", c.HTTPAddr),
		slog.Bool("http_tls_enabled", c.HTTPTLSCertFile != "" && c.HTTPTLSKeyFile != ""),
		slog.String("mqtt_broker_url", c.MQTTBrokerURL),
		slog.Bool("mqtt_admin_configured", c.MQTTAdminUser != "" && c.MQTTAdminPass != ""),
		slog.Bool("mqtt_bridge_configured", c.MQTTBridgeUser != "" && c.MQTTBridgePass != ""),
		slog.Bool("mqtt_control_configured", c.MQTTControlUser != "" && c.MQTTControlPass != ""),
		slog.String("nats_url", c.NATSURL),
		slog.String("redis_addr", c.RedisAddr),
		slog.Bool("postgres_dsn_set", c.PostgresDSN != ""),
		slog.Bool("tls_ca_file_set", c.TLSCAFile != ""),
		slog.Bool("otlp_endpoint_set", c.OTLPEndpoint != ""),
		slog.String("rules_file", c.RulesFile),
		slog.Bool("jwt_signing_key_set", c.JWTSigningKey != ""),
	)
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func getFloat(key string, fallback float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func getDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	return fallback
}
