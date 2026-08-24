// Command hostagent publishes real laptop telemetry — CPU usage and
// temperature, memory, battery, Wi-Fi signal — over MQTT using the same
// v1 payload schema as the PWA sensor client (internal/telemetry). It runs
// natively on the developer's machine, not in a container, so it can read
// real host metrics; see deploy/docker-compose.yml for why it isn't a
// compose service.
//
// On first run it needs a one-time claim token (HOSTAGENT_CLAIM_TOKEN,
// from `control token create`); after that its broker credentials are
// cached locally (HOSTAGENT_STATE_FILE) and the token is no longer
// needed — see internal/provisioning.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/provisioning"
	"github.com/Ritesh956/SenseGrid/internal/telemetry"
	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
)

const defaultHTTPAddr = ":8084"

func main() {
	cfg := config.Load("hostagent", defaultHTTPAddr)
	logger := logging.New(cfg.ServiceName, cfg.LogLevel)
	logger.Info("starting", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	caFile := cfg.TLSCAFile
	if caFile == "" {
		caFile = "deploy/certs/ca.pem" // default for `go run` from the repo root
	}
	stateFile := getEnv("HOSTAGENT_STATE_FILE", defaultStateFile())
	controlURL := getEnv("CONTROL_URL", "https://localhost:8090")
	claimToken := os.Getenv("HOSTAGENT_CLAIM_TOKEN")

	claimCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	creds, err := provisioning.LoadOrClaim(claimCtx, stateFile, controlURL, claimToken, caFile)
	cancel()
	if err != nil {
		logger.Error("provisioning failed", "err", err, "state_file", stateFile)
		os.Exit(1)
	}
	logger.Info("provisioned", "device_id", creds.DeviceID, "name", creds.Name)

	mqttClient, err := connectMQTT(cfg, creds, caFile, logger)
	if err != nil {
		logger.Error("mqtt connect failed", "err", err)
		os.Exit(1)
	}
	defer mqttClient.Disconnect(250)

	sampleInterval := getEnvDuration("HOSTAGENT_SAMPLE_INTERVAL", 5*time.Second)
	go runSamplingLoop(ctx, mqttClient, creds.DeviceID, sampleInterval, logger)

	srv, _ := httpserver.New(cfg.HTTPAddr)
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func connectMQTT(cfg config.Config, creds provisioning.Credentials, caFile string, logger *slog.Logger) (mqtt.Client, error) {
	tlsCfg, err := tlsutil.FromCAFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetClientID(creds.DeviceID).
		SetUsername(creds.MQTTUsername).
		SetPassword(creds.MQTTPassword).
		SetTLSConfig(tlsCfg).
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetOnConnectHandler(func(mqtt.Client) {
			logger.Info("mqtt connected")
		}).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt connection lost, reconnecting with backoff", "err", err)
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

func runSamplingLoop(ctx context.Context, client mqtt.Client, deviceID string, interval time.Duration, logger *slog.Logger) {
	var seq uint64
	warnOnce := map[string]bool{}
	topic := telemetry.TelemetryTopic(deviceID)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !client.IsConnected() {
				continue // dropped, not queued: the next tick tries again
			}
			for _, r := range collect(ctx, logger, warnOnce) {
				seq++
				payload := telemetry.Reading{
					SchemaVersion: telemetry.SchemaVersion,
					DeviceID:      deviceID,
					SensorType:    r.sensorType,
					Value:         r.value,
					Values:        r.values,
					DeviceTimeMS:  time.Now().UnixMilli(),
					Seq:           seq,
					TraceID:       telemetry.NewTraceID(),
				}
				body, err := json.Marshal(payload)
				if err != nil {
					logger.Error("marshal reading", "err", err, "sensor_type", r.sensorType)
					continue
				}
				client.Publish(topic, 1, false, body)
			}
		}
	}
}

func defaultStateFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".sensegrid", "hostagent-device.json")
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
