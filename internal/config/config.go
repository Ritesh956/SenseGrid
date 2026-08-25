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

	MQTTBrokerURL  string
	MQTTAdminUser  string
	MQTTAdminPass  string
	MQTTBridgeUser string
	MQTTBridgePass string
	NATSURL        string
	PostgresDSN    string
	RedisAddr      string
	TLSCAFile      string
	OTLPEndpoint   string
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

		MQTTBrokerURL:  getEnv("MQTT_BROKER_URL", "tls://localhost:8883"),
		MQTTAdminUser:  getEnv("MQTT_ADMIN_USERNAME", ""),
		MQTTAdminPass:  getEnv("MQTT_ADMIN_PASSWORD", ""),
		MQTTBridgeUser: getEnv("MQTT_BRIDGE_USERNAME", "ingest-bridge"),
		MQTTBridgePass: getEnv("MQTT_BRIDGE_PASSWORD", ""),
		NATSURL:        getEnv("NATS_URL", "nats://localhost:4222"),
		PostgresDSN:    getEnv("POSTGRES_DSN", "postgres://sensegrid:sensegrid@localhost:5432/sensegrid?sslmode=disable"),
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		TLSCAFile:      getEnv("TLS_CA_FILE", ""),
		OTLPEndpoint:   getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
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
		slog.String("nats_url", c.NATSURL),
		slog.String("redis_addr", c.RedisAddr),
		slog.Bool("postgres_dsn_set", c.PostgresDSN != ""),
		slog.Bool("tls_ca_file_set", c.TLSCAFile != ""),
		slog.Bool("otlp_endpoint_set", c.OTLPEndpoint != ""),
	)
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
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
