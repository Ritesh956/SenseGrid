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
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Ritesh956/SenseGrid/internal/config"
	"github.com/Ritesh956/SenseGrid/internal/httpserver"
	"github.com/Ritesh956/SenseGrid/internal/logging"
	"github.com/Ritesh956/SenseGrid/internal/provisioning"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
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

	sampleInterval := getEnvDuration("HOSTAGENT_SAMPLE_INTERVAL", 5*time.Second)
	cfgState := &atomic.Pointer[appliedConfig]{}
	cfgState.Store(ptr(defaultAppliedConfig(int(sampleInterval.Milliseconds()))))
	intervalChanged := make(chan int, 1)

	mqttClient, err := connectMQTT(cfg, creds, caFile, logger, cfgState, intervalChanged)
	if err != nil {
		logger.Error("mqtt connect failed", "err", err)
		os.Exit(1)
	}
	defer mqttClient.Disconnect(250)

	go runSamplingLoop(ctx, mqttClient, creds.DeviceID, cfgState, intervalChanged, logger)

	srv, _ := httpserver.New(cfg.HTTPAddr)
	if err := httpserver.Run(ctx, srv, cfg.ShutdownTimeout, cfg.HTTPTLSCertFile, cfg.HTTPTLSKeyFile, logger); err != nil {
		logger.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

func connectMQTT(cfg config.Config, creds provisioning.Credentials, caFile string, logger *slog.Logger,
	cfgState *atomic.Pointer[appliedConfig], intervalChanged chan<- int) (mqtt.Client, error) {
	tlsCfg, err := tlsutil.FromCAFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("loading CA: %w", err)
	}

	deviceID := creds.DeviceID
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetClientID(deviceID).
		SetUsername(creds.MQTTUsername).
		SetPassword(creds.MQTTPassword).
		SetTLSConfig(tlsCfg).
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(true).
		SetConnectRetryInterval(2 * time.Second).
		SetMaxReconnectInterval(30 * time.Second).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Warn("mqtt connection lost, reconnecting with backoff", "err", err)
		})

	// SetOnConnectHandler, not a one-time Subscribe after Connect returns:
	// paho doesn't automatically resubscribe on reconnect (clean sessions
	// by default), so re-subscribing here is what makes "disconnect,
	// change config, reconnect — picks it up immediately" work rather than
	// only working on the very first connect.
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logger.Info("mqtt connected")
		token := c.Subscribe(telemetry.ConfigTopic(deviceID), 1, configHandler(deviceID, cfgState, intervalChanged, c, logger))
		if !token.WaitTimeout(10 * time.Second) {
			logger.Error("subscribing to config topic timed out")
			return
		}
		if err := token.Error(); err != nil {
			logger.Error("subscribing to config topic failed", "err", err)
			return
		}
		publishReported(c, deviceID, *cfgState.Load(), logger)
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

// configHandler applies (or rejects) an incoming desired-config message —
// see config.go's applyPartial for validation — swaps cfgState, nudges the
// sampling loop to pick up a new interval immediately rather than waiting
// out the old ticker, and reports back what actually took effect.
func configHandler(deviceID string, cfgState *atomic.Pointer[appliedConfig], intervalChanged chan<- int, client mqtt.Client, logger *slog.Logger) mqtt.MessageHandler {
	return func(c mqtt.Client, msg mqtt.Message) {
		var d shadow.Desired
		if err := json.Unmarshal(msg.Payload(), &d); err != nil {
			logger.Error("config: unparseable desired config, ignoring", "err", err)
			return
		}

		current := *cfgState.Load()
		next, err := applyPartial(current, d)
		if err != nil {
			logger.Warn("config: rejecting desired config", "err", err, "revision", d.Revision)
			publishReportedValue(client, deviceID, current.toRejectedReported(d.Revision, err.Error()), logger)
			return
		}

		cfgState.Store(&next)
		select {
		case intervalChanged <- next.sampleIntervalMS:
		default:
		}
		logger.Info("config: applied desired config", "revision", d.Revision, "sample_interval_ms", next.sampleIntervalMS)
		publishReported(client, deviceID, next, logger)
	}
}

func publishReported(client mqtt.Client, deviceID string, cfg appliedConfig, logger *slog.Logger) {
	publishReportedValue(client, deviceID, cfg.toReported(), logger)
}

func publishReportedValue(client mqtt.Client, deviceID string, rep shadow.Reported, logger *slog.Logger) {
	rep.ReportedAtMS = time.Now().UnixMilli()
	body, err := json.Marshal(rep)
	if err != nil {
		logger.Error("config: marshaling reported state", "err", err)
		return
	}
	client.Publish(telemetry.StateTopic(deviceID), 0, false, body)
}

func ptr[T any](v T) *T { return &v }

// runSamplingLoop ticks at the current config's sample interval,
// collecting and publishing readings — continuously (today's behavior,
// one publish per reading) or batched (buffered and sent as a burst),
// depending on the shared appliedConfig. intervalChanged lets
// configHandler shrink/grow the ticker immediately on a config update
// rather than waiting for the current tick to fire on the old interval.
func runSamplingLoop(ctx context.Context, client mqtt.Client, deviceID string, cfgState *atomic.Pointer[appliedConfig], intervalChanged <-chan int, logger *slog.Logger) {
	var seq uint64
	warnOnce := map[string]bool{}
	topic := telemetry.TelemetryTopic(deviceID)

	cfg := *cfgState.Load()
	ticker := time.NewTicker(time.Duration(cfg.sampleIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	var pending []telemetry.Reading
	lastFlush := time.Now()

	flush := func() {
		for _, payload := range pending {
			body, err := json.Marshal(payload)
			if err != nil {
				logger.Error("marshal reading", "err", err, "sensor_type", payload.SensorType)
				continue
			}
			client.Publish(topic, 1, false, body)
		}
		pending = pending[:0]
		lastFlush = time.Now()
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case ms := <-intervalChanged:
			ticker.Reset(time.Duration(ms) * time.Millisecond)
		case <-ticker.C:
			cfg = *cfgState.Load()
			if !client.IsConnected() {
				continue // dropped, not queued: the next tick tries again
			}
			for _, r := range collect(ctx, logger, warnOnce) {
				if !cfg.sensorEnabled(r.sensorType) {
					continue
				}
				seq++
				pending = append(pending, telemetry.Reading{
					SchemaVersion: telemetry.SchemaVersion,
					DeviceID:      deviceID,
					SensorType:    r.sensorType,
					Value:         r.value,
					Values:        r.values,
					DeviceTimeMS:  time.Now().UnixMilli(),
					Seq:           seq,
					TraceID:       telemetry.NewTraceID(),
				})
			}

			if cfg.mode != shadow.ReportingBatched {
				flush()
				continue
			}
			sizeHit := cfg.batchSize > 0 && len(pending) >= cfg.batchSize
			timeHit := cfg.flushIntervalMS > 0 && time.Since(lastFlush) >= time.Duration(cfg.flushIntervalMS)*time.Millisecond
			if sizeHit || timeHit {
				flush()
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
