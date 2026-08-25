// Phase 4 runtime config for the PWA — the browser-side mirror of
// cmd/hostagent/config.go's applyPartial/appliedConfig. Kept as its own
// module (not inline in app.js) for the same reason internal/shadow's Go
// types live in their own file: one place defining what a valid desired
// config looks like, reused by both the validate-and-apply path and the
// state-report path.

export const KNOWN_SENSORS = ["accel", "gyro", "orientation", "battery", "network_rtt_hint"];

export function defaultConfig(rateHz) {
  return {
    revision: 0,
    sampleRateHz: rateHz,
    enabledSensors: null, // null = all
    batchSize: 0,
    flushIntervalMs: 0,
    reportingMode: "continuous",
  };
}

// applyPartial validates a desired-config message (the same shape
// internal/shadow.Desired marshals to JSON as) against `current`,
// returning either { next } or { error } — never both. Mirrors
// cmd/hostagent/config.go's applyPartial field-by-field.
export function applyPartial(current, desired) {
  const next = { ...current, revision: desired.revision ?? current.revision };

  if (desired.sample_rate_hz !== undefined) {
    if (!(desired.sample_rate_hz > 0)) {
      return { error: `sample_rate_hz must be positive, got ${desired.sample_rate_hz}` };
    }
    next.sampleRateHz = desired.sample_rate_hz;
  }
  if (desired.enabled_sensors !== undefined) {
    if (!Array.isArray(desired.enabled_sensors) || desired.enabled_sensors.length === 0) {
      return { error: "enabled_sensors must not be empty (omit the field to enable all sensors)" };
    }
    for (const s of desired.enabled_sensors) {
      if (!KNOWN_SENSORS.includes(s)) return { error: `unknown sensor "${s}" in enabled_sensors` };
    }
    next.enabledSensors = desired.enabled_sensors;
  }
  if (desired.batch_size !== undefined) {
    if (!(Number.isInteger(desired.batch_size) && desired.batch_size >= 1)) {
      return { error: `batch_size must be >= 1, got ${desired.batch_size}` };
    }
    next.batchSize = desired.batch_size;
  }
  if (desired.flush_interval_ms !== undefined) {
    if (!(Number.isInteger(desired.flush_interval_ms) && desired.flush_interval_ms >= 0)) {
      return { error: `flush_interval_ms must be >= 0, got ${desired.flush_interval_ms}` };
    }
    next.flushIntervalMs = desired.flush_interval_ms;
  }
  if (desired.reporting_mode !== undefined) {
    if (desired.reporting_mode !== "continuous" && desired.reporting_mode !== "batched") {
      return { error: `unknown reporting_mode "${desired.reporting_mode}"` };
    }
    next.reportingMode = desired.reporting_mode;
  }
  if (next.reportingMode === "batched" && next.batchSize === 0 && next.flushIntervalMs === 0) {
    return { error: "batched reporting_mode needs batch_size and/or flush_interval_ms set" };
  }
  return { next };
}

// toReported builds the state-report payload for cfg, matching
// internal/shadow.Reported's JSON shape.
export function toReported(cfg, rejected, rejectedRevision, reason) {
  return {
    schema_version: "1.0",
    applied_revision: cfg.revision,
    rejected: !!rejected,
    rejected_revision: rejected ? rejectedRevision : undefined,
    reject_reason: rejected ? reason : undefined,
    sample_rate_hz: cfg.sampleRateHz,
    enabled_sensors: cfg.enabledSensors ?? undefined,
    batch_size: cfg.batchSize || undefined,
    flush_interval_ms: cfg.flushIntervalMs || undefined,
    reporting_mode: cfg.reportingMode,
    reported_at_ms: Date.now(),
  };
}
