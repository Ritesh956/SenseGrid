// Phase 4 runtime config: hostagent subscribes to its own config topic
// (telemetry.ConfigTopic) and applies changes to the sampling loop with no
// restart — sample rate, which metrics to collect, and how publishes are
// paced (continuous vs. batched). See internal/shadow's doc comments for
// the wire schema and the reporting_mode interpretation.
package main

import (
	"fmt"

	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

// knownSensors are the sensor_type names metrics.go's readers can produce
// — the only valid values for Desired.EnabledSensors.
var knownSensors = map[string]bool{
	"cpu": true, "cpu_temp": true, "mem": true, "battery": true, "wifi_signal": true,
}

// appliedConfig is the resolved, concrete form of shadow.Desired the
// sampling loop actually reads — pointers/omitempty resolved into plain
// values, so runSamplingLoop never has to nil-check.
type appliedConfig struct {
	desired shadow.Desired // kept for AppliedRevision + echoing fields back in Reported

	sampleIntervalMS int
	enabledSensors   map[string]bool // nil/empty = all sensors enabled
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

// applyPartial validates d against current and returns the new resolved
// config — a partial update (only some fields set) leaves the rest
// unchanged, per shadow.Desired's doc comment. Returns an error (with a
// caller-presentable reason) for anything invalid, in which case the
// caller keeps `current` and reports the rejection — see main.go's config
// message handler.
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

// sensorEnabled reports whether sensorType should be collected under cfg.
func (cfg appliedConfig) sensorEnabled(sensorType string) bool {
	if len(cfg.enabledSensors) == 0 {
		return true
	}
	return cfg.enabledSensors[sensorType]
}

// toReported builds the state report published after successfully
// applying cfg — see main.go's publishReported/publishReportedValue.
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

// toRejectedReported builds the state report published when a candidate
// revision fails validation: cfg (the config still actually running)
// supplies AppliedRevision and the applied fields, rejectedRevision +
// reason describe what was attempted and why it didn't take — kept
// separate from AppliedRevision so "what's running" and "what was just
// rejected" can't be confused with each other.
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
