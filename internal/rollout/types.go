// Package rollout is the Phase 4 Pass B staged rollout engine: push a
// shadow.Desired config to a growing percentage of a device cohort over a
// sequence of bake periods, auto-advancing while healthy and
// auto-rolling-back on breach — see engine.go's doc comment for the
// mechanics, and the SenseGrid Blueprint §P4 for the feature this
// implements.
package rollout

import (
	"time"

	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

// Cohort selects which devices a rollout targets. DeviceIDs, when
// non-empty, is used exactly as given; otherwise every claimed device of
// DeviceType is targeted (DeviceType == "" meaning every device — these
// are the only two device dimensions internal/devices.Device exposes
// today).
type Cohort struct {
	DeviceType string   `json:"device_type,omitempty"`
	DeviceIDs  []string `json:"device_ids,omitempty"`
}

// Stage is one step of a staged rollout: target Percent of the cohort
// (see cohort.go's StageTargets for how membership is computed), then bake
// for BakeDuration before either advancing to the next stage or (if
// BakeDuration has elapsed and health is fine) completing, if this is the
// last stage.
type Stage struct {
	Percent      int           `json:"percent"`
	BakeDuration time.Duration `json:"bake_duration"`
}

// HealthCriteria are the auto-rollback thresholds, each a fraction (0..1)
// of the current stage's targeted device count. See engine.go's
// evaluateStageHealth for how targeted/rejected/disconnected/errored are
// counted.
type HealthCriteria struct {
	MaxErrorRate      float64 `json:"max_error_rate"`
	MaxDisconnectRate float64 `json:"max_disconnect_rate"`
	MaxRejectionRate  float64 `json:"max_rejection_rate"`
}

// State is a rollout's lifecycle state. Running/Paused/Completed/Aborted —
// there's no separate "pending": creating a rollout starts it immediately
// at stage 0 (see engine.Create), matching the blueprint's REST table
// having no separate start endpoint.
type State string

const (
	Running   State = "running"
	Paused    State = "paused"
	Completed State = "completed"
	Aborted   State = "aborted"
)

// Rollout is one staged rollout's full state — persisted so an in-flight
// rollout survives a cmd/control restart (see store.go/engine.go's Resume).
type Rollout struct {
	ID                    string
	Name                  string
	Cohort                Cohort
	DesiredConfig         shadow.Desired
	Stages                []Stage
	HealthCriteria        HealthCriteria
	State                 State
	CurrentStageIndex     int
	CurrentStageStartedAt time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// EventType labels what a RolloutEvent describes.
type EventType string

const (
	EventStageStarted   EventType = "stage_started"
	EventStageAdvanced  EventType = "stage_advanced"
	EventHealthBreached EventType = "health_breached"
	EventPaused         EventType = "paused"
	EventResumed        EventType = "resumed"
	EventCompleted      EventType = "completed"
	EventAborted        EventType = "aborted"
)

// RolloutEvent is published to internal/telemetry.RolloutEventsSubject on
// every state transition — mirrors internal/alerts.AlertEvent's shape.
type RolloutEvent struct {
	SchemaVersion string         `json:"schema_version"`
	RolloutID     string         `json:"rollout_id"`
	Type          EventType      `json:"type"`
	StageIndex    int            `json:"stage_index"`
	Detail        map[string]any `json:"detail,omitempty"`
	TimestampMS   int64          `json:"timestamp_ms"`
}

const SchemaVersion = "1.0"
