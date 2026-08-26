// virtualDevice is one synthetic device: its own MQTT connection, its own
// claimed identity, its own config-topic subscription and shadow-state
// reporting — a real client exercising the same code paths cmd/hostagent
// and the PWA do, not a raw publish loop. See manager.go for how N of
// these are created, scaled, and chaos-partitioned.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Ritesh956/SenseGrid/internal/provisioning"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
	"github.com/Ritesh956/SenseGrid/internal/telemetry"
)

// knownSensors mirrors cmd/hostagent/config.go's role: the only valid
// values for shadow.Desired.EnabledSensors this device type can produce.
var knownSensors = map[string]bool{"temperature": true, "humidity": true, "accel": true}

// appliedConfig is the resolved form of shadow.Desired the sampling loop
// reads — same shape and purpose as cmd/hostagent/config.go's type of the
// same name, duplicated rather than shared because the two binaries'
// sensor sets differ (knownSensors above) and sharing would need a new
// internal/ package for what's currently ~80 lines used by exactly one
// other caller.
type appliedConfig struct {
	desired shadow.Desired

	sampleIntervalMS int
	enabledSensors   map[string]bool
	batchSize        int
	flushIntervalMS  int
	mode             shadow.ReportingMode
}

func defaultAppliedConfig(sampleIntervalMS int) appliedConfig {
	return appliedConfig{
		desired:          shadow.Desired{SchemaVersion: shadow.SchemaVersion},
		sampleIntervalMS: sampleIntervalMS,
		mode:             shadow.ReportingContinuous,
	}
}

func applyPartial(current appliedConfig, d shadow.Desired) (appliedConfig, error) {
	next := current
	next.desired = d

	if d.SampleRateHz != nil {
		if *d.SampleRateHz <= 0 {
			return current, fmt.Errorf("sample_rate_hz must be positive, got %v", *d.SampleRateHz)
		}
		next.sampleIntervalMS = int(1000 / *d.SampleRateHz)
		if next.sampleIntervalMS < 1 {
			next.sampleIntervalMS = 1
		}
	}
	if d.EnabledSensors != nil {
		if len(d.EnabledSensors) == 0 {
			return current, fmt.Errorf("enabled_sensors must not be empty (omit the field to enable all sensors)")
		}
		set := make(map[string]bool, len(d.EnabledSensors))
		for _, s := range d.EnabledSensors {
			if !knownSensors[s] {
				return current, fmt.Errorf("unknown sensor %q in enabled_sensors", s)
			}
			set[s] = true
		}
		next.enabledSensors = set
	}
	if d.BatchSize != nil {
		if *d.BatchSize < 1 {
			return current, fmt.Errorf("batch_size must be >= 1, got %d", *d.BatchSize)
		}
		next.batchSize = *d.BatchSize
	}
	if d.FlushIntervalMS != nil {
		if *d.FlushIntervalMS < 0 {
			return current, fmt.Errorf("flush_interval_ms must be >= 0, got %d", *d.FlushIntervalMS)
		}
		next.flushIntervalMS = *d.FlushIntervalMS
	}
	if d.ReportingMode != nil {
		switch *d.ReportingMode {
		case shadow.ReportingContinuous, shadow.ReportingBatched:
			next.mode = *d.ReportingMode
		default:
			return current, fmt.Errorf("unknown reporting_mode %q", *d.ReportingMode)
		}
	}
	if next.mode == shadow.ReportingBatched && next.batchSize == 0 && next.flushIntervalMS == 0 {
		return current, fmt.Errorf("batched reporting_mode needs batch_size and/or flush_interval_ms set")
	}
	return next, nil
}

func (cfg appliedConfig) sensorEnabled(sensorType string) bool {
	if len(cfg.enabledSensors) == 0 {
		return true
	}
	return cfg.enabledSensors[sensorType]
}

func (cfg appliedConfig) toReported() shadow.Reported {
	rate := 1000.0 / float64(cfg.sampleIntervalMS)
	var sensors []string
	if len(cfg.enabledSensors) > 0 {
		for s := range cfg.enabledSensors {
			sensors = append(sensors, s)
		}
	}
	mode := cfg.mode
	return shadow.Reported{
		SchemaVersion:   shadow.SchemaVersion,
		AppliedRevision: cfg.desired.Revision,
		SampleRateHz:    &rate,
		EnabledSensors:  sensors,
		BatchSize:       nonZeroIntPtr(cfg.batchSize),
		FlushIntervalMS: nonZeroIntPtr(cfg.flushIntervalMS),
		ReportingMode:   &mode,
	}
}

func (cfg appliedConfig) toRejectedReported(rejectedRevision uint64, reason string) shadow.Reported {
	rep := cfg.toReported()
	rep.Rejected = true
	rep.RejectedRevision = rejectedRevision
	rep.RejectReason = reason
	return rep
}

func nonZeroIntPtr(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// deviceStats are the counters manager.go/controlapi.go read for GET
// /fleet/status and the Prometheus gauges — atomics because they're
// written by this device's own run() goroutine and read concurrently from
// HTTP handler goroutines.
type deviceStats struct {
	connected     atomic.Bool
	partitioned   atomic.Bool
	published     atomic.Uint64
	publishErrors atomic.Uint64
	malformedSent atomic.Uint64
	anomaliesSent atomic.Uint64
	reconnects    atomic.Uint64
	lastSeq       atomic.Uint64
}

type virtualDevice struct {
	idx       int
	creds     provisioning.Credentials
	brokerURL string
	tlsCfg    *tls.Config
	rtCfg     *atomic.Pointer[runtimeConfig]
	metrics   *fleetMetrics
	logger    *slog.Logger

	client   mqtt.Client
	cfgState *atomic.Pointer[appliedConfig]

	stats deviceStats

	temperature *sensorSignal
	humidity    *sensorSignal
	accelX      *sensorSignal
	accelY      *sensorSignal
	accelZ      *sensorSignal

	partitionCh chan time.Duration
}

func newVirtualDevice(idx int, creds provisioning.Credentials, brokerURL string, tlsCfg *tls.Config, sampleIntervalMS int, rtCfg *atomic.Pointer[runtimeConfig], metrics *fleetMetrics, logger *slog.Logger) *virtualDevice {
	cfgState := &atomic.Pointer[appliedConfig]{}
	cfgState.Store(ptr(defaultAppliedConfig(sampleIntervalMS)))

	seed := int64(idx)
	return &virtualDevice{
		idx:       idx,
		creds:     creds,
		brokerURL: brokerURL,
		tlsCfg:    tlsCfg,
		rtCfg:     rtCfg,
		metrics:   metrics,
		logger:    logger.With("device_id", creds.DeviceID, "fleet_idx", idx),
		cfgState:  cfgState,

		temperature: newSensorSignal(seed*10+1, 22, 3, 60_000, 0.05, 0.3),
		humidity:    newSensorSignal(seed*10+2, 45, 10, 90_000, -0.02, 1.0),
		accelX:      newSensorSignal(seed*10+3, 0, 0.3, 5_000, 0, 0.05),
		accelY:      newSensorSignal(seed*10+4, 0, 0.3, 6_000, 0, 0.05),
		accelZ:      newSensorSignal(seed*10+5, 9.8, 0.1, 7_000, 0, 0.03),

		partitionCh: make(chan time.Duration, 1),
	}
}

func ptr[T any](v T) *T { return &v }

// run drives one device's whole lifecycle until ctx is cancelled: an
// optional startDelay (manager.go's ramp staggering), connect-with-retry,
// then the sampling loop. It never returns until ctx is done, retrying
// connect failures with backoff the way a real device would rather than
// giving up.
func (d *virtualDevice) run(ctx context.Context, startDelay time.Duration) {
	if startDelay > 0 {
		t := time.NewTimer(startDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}

	backoff := time.Second
	for {
		if err := d.connect(); err != nil {
			d.logger.Warn("connect failed, retrying", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		break
	}
	defer func() {
		// stats.connected otherwise only ever goes false via
		// ConnectionLostHandler (an *unexpected* loss) — a graceful stop
		// (manager.go's Scale() scaling down, or process shutdown) exits
		// through here instead, and GET /fleet/status needs to reflect
		// that just as accurately.
		d.stats.connected.Store(false)
		if d.client != nil && d.client.IsConnected() {
			d.client.Disconnect(250)
		}
	}()

	d.sampleLoop(ctx)
}

// connect establishes (or re-establishes, after a simulated partition) the
// MQTT connection: subscribe to the config topic, publish current reported
// state, exactly mirroring cmd/hostagent's connect sequence so the fleet
// exercises the same broker-side code paths a real device would.
func (d *virtualDevice) connect() error {
	deviceID := d.creds.DeviceID
	opts := mqtt.NewClientOptions().
		AddBroker(d.brokerURL).
		SetClientID(deviceID).
		SetUsername(d.creds.MQTTUsername).
		SetPassword(d.creds.MQTTPassword).
		SetTLSConfig(d.tlsCfg).
		SetConnectTimeout(10 * time.Second).
		SetAutoReconnect(false). // sampleLoop drives reconnection explicitly: backoff on involuntary loss, timed heal on simulated partition
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			d.stats.connected.Store(false)
			d.logger.Warn("mqtt connection lost", "err", err)
		})

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		token := c.Subscribe(telemetry.ConfigTopic(deviceID), 1, d.configHandler(c))
		if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
			d.logger.Error("subscribing to config topic failed", "err", token.Error())
			return
		}
		d.publishReported(c, *d.cfgState.Load())
		d.stats.connected.Store(true)
	})

	if d.client == nil {
		d.client = mqtt.NewClient(opts)
	} else {
		d.client = mqtt.NewClient(opts) // fresh client per (re)connect; paho clients aren't meant to be reconfigured in place
	}

	token := d.client.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("connect timed out")
	}
	return token.Error()
}

func (d *virtualDevice) configHandler(client mqtt.Client) mqtt.MessageHandler {
	return func(c mqtt.Client, msg mqtt.Message) {
		var desired shadow.Desired
		if err := json.Unmarshal(msg.Payload(), &desired); err != nil {
			d.logger.Error("config: unparseable desired config, ignoring", "err", err)
			return
		}

		current := *d.cfgState.Load()
		next, err := applyPartial(current, desired)
		if err != nil {
			d.logger.Warn("config: rejecting desired config", "err", err, "revision", desired.Revision)
			d.publishReportedValue(client, current.toRejectedReported(desired.Revision, err.Error()))
			return
		}
		d.cfgState.Store(&next)
		d.logger.Info("config: applied desired config", "revision", desired.Revision, "sample_interval_ms", next.sampleIntervalMS)
		d.publishReported(client, next)
	}
}

func (d *virtualDevice) publishReported(client mqtt.Client, cfg appliedConfig) {
	d.publishReportedValue(client, cfg.toReported())
}

func (d *virtualDevice) publishReportedValue(client mqtt.Client, rep shadow.Reported) {
	rep.ReportedAtMS = time.Now().UnixMilli()
	body, err := json.Marshal(rep)
	if err != nil {
		return
	}
	client.Publish(telemetry.StateTopic(d.creds.DeviceID), 0, false, body)
}

// sampleLoop is the core of the simulation: one tick per sample interval,
// generating readings from the sensor signals, applying whatever
// misbehavior runtimeConfig currently calls for (clock skew, latency
// jitter, malformed payloads, voluntary disconnects), and handling
// partition commands from manager.go's chaos API.
func (d *virtualDevice) sampleLoop(ctx context.Context) {
	deviceID := d.creds.DeviceID
	topic := telemetry.TelemetryTopic(deviceID)
	rnd := rand.New(rand.NewSource(int64(d.idx)*7919 + 13))
	var seq uint64

	cfg := *d.cfgState.Load()
	ticker := time.NewTicker(time.Duration(cfg.sampleIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	var pending []telemetry.Reading
	lastFlush := time.Now()
	var partitioned bool
	var healAt time.Time
	var lastReconnectAttempt time.Time
	const reconnectBackoff = 3 * time.Second

	flush := func() {
		rt := d.rtCfg.Load()
		for _, payload := range pending {
			d.publishOne(topic, payload, rt, rnd)
		}
		pending = pending[:0]
		lastFlush = time.Now()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case dur := <-d.partitionCh:
			if partitioned {
				continue
			}
			partitioned = true
			healAt = time.Now().Add(dur)
			d.stats.partitioned.Store(true)
			d.stats.connected.Store(false)
			if d.client != nil {
				d.client.Disconnect(100)
			}
			d.logger.Info("partitioned", "duration", dur)

		case <-ticker.C:
			cfg = *d.cfgState.Load()
			ticker.Reset(time.Duration(cfg.sampleIntervalMS) * time.Millisecond)
			rt := d.rtCfg.Load()

			if partitioned {
				if time.Now().Before(healAt) {
					continue
				}
				if err := d.connect(); err != nil {
					d.logger.Warn("heal reconnect failed, retrying next tick", "err", err)
					healAt = time.Now().Add(time.Second)
					continue
				}
				partitioned = false
				d.stats.partitioned.Store(false)
				d.stats.reconnects.Add(1)
				if d.metrics != nil {
					d.metrics.reconnectsTotal.Inc()
				}
				d.logger.Info("healed")
				continue // resume publishing on the following tick, once reconnect settles
			}

			if rt.DisconnectRate > 0 && rnd.Float64() < rt.DisconnectRate {
				d.stats.connected.Store(false)
				if d.client != nil {
					d.client.Disconnect(100)
				}
				if err := d.connect(); err != nil {
					d.logger.Warn("flaky reconnect failed", "err", err)
					continue
				}
				d.stats.reconnects.Add(1)
				if d.metrics != nil {
					d.metrics.reconnectsTotal.Inc()
				}
			}

			if !d.stats.connected.Load() {
				// Involuntary loss (broker restart, network blip) —
				// SetAutoReconnect is off (manager.go/partition need
				// explicit control over reconnection), so this is the only
				// path that recovers from it. Backoff-gated so a sustained
				// outage doesn't retry every single tick.
				if time.Since(lastReconnectAttempt) >= reconnectBackoff {
					lastReconnectAttempt = time.Now()
					if err := d.connect(); err != nil {
						d.logger.Warn("reconnect failed, retrying later", "err", err)
					} else {
						d.stats.reconnects.Add(1)
						if d.metrics != nil {
							d.metrics.reconnectsTotal.Inc()
						}
					}
				}
				continue // dropped, not queued: next tick tries again once reconnected
			}

			nowMS := time.Now().UnixMilli()
			if rt.ClockSkewJitterMS > 0 {
				nowMS += int64(rnd.Intn(2*rt.ClockSkewJitterMS) - rt.ClockSkewJitterMS)
			}

			for _, r := range d.collect(nowMS, cfg, rt) {
				seq++
				r.Seq = seq
				pending = append(pending, r)
			}
			d.stats.lastSeq.Store(seq)

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

// collect samples every enabled sensor once, in telemetry.Reading form.
// Seq is left at zero here — sampleLoop assigns the real value, one
// increment per Reading in the returned slice (so a tick that produces
// temperature+humidity+accel advances seq by 3, not 1), matching
// cmd/hostagent's identical loop. test/chaos's gap-detection queries key
// off exactly this: a device's total published Reading count should equal
// its last seq with no missing values in between.
func (d *virtualDevice) collect(nowMS int64, cfg appliedConfig, rt *runtimeConfig) []telemetry.Reading {
	var out []telemetry.Reading
	deviceID := d.creds.DeviceID

	if cfg.sensorEnabled("temperature") {
		v, anomaly := d.temperature.sample(nowMS, rt.StepRate, rt.AnomalyRate)
		if anomaly {
			d.recordAnomaly()
		}
		out = append(out, telemetry.Reading{
			SchemaVersion: telemetry.SchemaVersion, DeviceID: deviceID, SensorType: "temperature",
			Value: &v, DeviceTimeMS: nowMS, TraceID: telemetry.NewTraceID(),
		})
	}
	if cfg.sensorEnabled("humidity") {
		v, anomaly := d.humidity.sample(nowMS, rt.StepRate, rt.AnomalyRate)
		if anomaly {
			d.recordAnomaly()
		}
		out = append(out, telemetry.Reading{
			SchemaVersion: telemetry.SchemaVersion, DeviceID: deviceID, SensorType: "humidity",
			Value: &v, DeviceTimeMS: nowMS, TraceID: telemetry.NewTraceID(),
		})
	}
	if cfg.sensorEnabled("accel") {
		x, ax := d.accelX.sample(nowMS, rt.StepRate, rt.AnomalyRate)
		y, ay := d.accelY.sample(nowMS, rt.StepRate, rt.AnomalyRate)
		z, az := d.accelZ.sample(nowMS, rt.StepRate, rt.AnomalyRate)
		if ax || ay || az {
			d.recordAnomaly()
		}
		out = append(out, telemetry.Reading{
			SchemaVersion: telemetry.SchemaVersion, DeviceID: deviceID, SensorType: "accel",
			Values: map[string]float64{"x": x, "y": y, "z": z}, DeviceTimeMS: nowMS, TraceID: telemetry.NewTraceID(),
		})
	}
	return out
}

func (d *virtualDevice) recordAnomaly() {
	d.stats.anomaliesSent.Add(1)
	if d.metrics != nil {
		d.metrics.anomaliesSentTotal.Inc()
	}
}

func (d *virtualDevice) recordPublishError() {
	d.stats.publishErrors.Add(1)
	if d.metrics != nil {
		d.metrics.publishErrorsTotal.Inc()
	}
}

// publishOne sends one reading, occasionally corrupting it into a
// malformed payload (exercising cmd/ingest's DLQ path) and adding latency
// jitter, both gated by rt so a chaos script can dial them from zero.
func (d *virtualDevice) publishOne(topic string, r telemetry.Reading, rt *runtimeConfig, rnd *rand.Rand) {
	if rt.LatencyJitterMS > 0 {
		time.Sleep(time.Duration(rnd.Intn(rt.LatencyJitterMS+1)) * time.Millisecond)
	}

	var body []byte
	if rt.MalformedRate > 0 && rnd.Float64() < rt.MalformedRate {
		body = malformedPayload(r, rnd)
		d.stats.malformedSent.Add(1)
		if d.metrics != nil {
			d.metrics.malformedSentTotal.Inc()
		}
	} else {
		var err error
		body, err = json.Marshal(r)
		if err != nil {
			d.recordPublishError()
			return
		}
	}

	if !d.client.IsConnected() {
		d.recordPublishError()
		return
	}
	d.client.Publish(topic, 1, false, body)
	d.stats.published.Add(1)
	if d.metrics != nil {
		d.metrics.publishedTotal.Inc()
	}
}

// malformedPayload returns one of a few realistic ways a real device might
// send garbage: truncated/invalid JSON, or well-formed JSON that fails
// internal/telemetry.Reading.Validate() — both land in cmd/ingest's DLQ,
// never silently dropped, which is exactly what this exists to verify.
func malformedPayload(r telemetry.Reading, rnd *rand.Rand) []byte {
	switch rnd.Intn(3) {
	case 0:
		body, _ := json.Marshal(r)
		if len(body) > 10 {
			return body[:len(body)-10] // truncated JSON
		}
		return []byte("{")
	case 1:
		r.SchemaVersion = "0.9" // unsupported schema version
		body, _ := json.Marshal(r)
		return body
	default:
		r.DeviceID = "" // fails uuid.Parse in Validate
		body, _ := json.Marshal(r)
		return body
	}
}
