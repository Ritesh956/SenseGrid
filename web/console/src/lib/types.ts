// Wire types mirroring cmd/control's JSON responses (see the views in
// cmd/control/devices_handlers.go, alerts_handlers.go, rollout_handlers.go
// and internal/shadow/types.go, internal/window/metric_event.go).

export interface Device {
  id: string;
  name: string;
  type: string;
  status: string;
  registered_at: string;
  last_seen?: string;
}

export type ReportingMode = "continuous" | "batched";

export interface ShadowDesired {
  schema_version: string;
  revision?: number;
  sample_rate_hz?: number;
  enabled_sensors?: string[];
  batch_size?: number;
  flush_interval_ms?: number;
  reporting_mode?: ReportingMode;
}

export interface ShadowReported {
  schema_version: string;
  applied_revision: number;
  rejected?: boolean;
  rejected_revision?: number;
  reject_reason?: string;
  sample_rate_hz?: number;
  enabled_sensors?: string[];
  batch_size?: number;
  flush_interval_ms?: number;
  reporting_mode?: ReportingMode;
  reported_at_ms: number;
}

export interface ShadowView {
  desired: ShadowDesired;
  desired_revision: number;
  reported: ShadowReported | null;
  drift: boolean;
}

// internal/alerts.Alert has no json tags of its own (see the Phase 5 plan's
// research note) — the wire shape is Go's default capitalized field names.
export type AlertState = "firing" | "acknowledged" | "resolved";

export interface Alert {
  ID: string;
  DeviceID: string;
  SensorType: string;
  RuleName: string;
  Severity: string;
  State: AlertState;
  Detail: Record<string, unknown>;
  FiredAt: string;
  AcknowledgedAt: string | null;
  ResolvedAt: string | null;
  UpdatedAt: string;
}

export interface RolloutStage {
  percent: number;
  bake_seconds: number;
}

export interface RolloutCohort {
  device_type?: string;
  device_ids?: string[];
}

export interface RolloutHealthCriteria {
  max_error_rate: number;
  max_disconnect_rate: number;
  max_rejection_rate: number;
}

export type RolloutState = "running" | "paused" | "completed" | "aborted";

export interface Rollout {
  id: string;
  name: string;
  cohort: RolloutCohort;
  desired_config: ShadowDesired;
  stages: RolloutStage[];
  health_criteria: RolloutHealthCriteria;
  state: RolloutState;
  current_stage_index: number;
  current_stage_started_at: string;
}

export interface MetricEvent {
  schema_version: string;
  device_id: string;
  sensor_type: string;
  mean: number;
  stddev: number;
  count: number;
  ewma: number;
  window_start_ms: number;
  window_end_ms: number;
  trace_id?: string;
}
