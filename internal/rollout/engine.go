// Engine mechanics: a rollout targets a growing percentage of a cohort
// over a sequence of stages (see cohort.go's StageTargets for how stage
// membership is computed). Creating a rollout immediately applies stage 0.
// A ticker loop (Run) periodically checks every active rollout: if its
// current stage's health has breached (EvaluateHealth), every device the
// rollout has ever targeted is reverted to its pre-rollout snapshot and
// the rollout ends aborted; otherwise, once the stage's bake duration has
// elapsed, either the next stage is applied (pushing config only to
// newly-included devices) or, if this was the last stage, the rollout
// completes.
//
// Engine depends on interfaces (RolloutStore, ShadowSetter, DeviceLister,
// ErrorSignal, EventPublisher below), not concrete internal/shadow,
// internal/devices, internal/alerts types — deliberately, so the three
// scenarios the blueprint requires ("full successful rollout, automatic
// rollback on breach, rollout resumption after restart") can be exercised
// end-to-end in engine_test.go against in-memory fakes, without a
// Postgres/NATS/MQTT test harness this repo has never had. Every exported
// method holds a single coarse mutex for its whole body, including its
// store/publish calls — simpler and safer than fine-grained per-rollout
// locking, and cheap enough at this project's scale (a handful of
// rollouts, a ~10s tick interval).
package rollout

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Ritesh956/SenseGrid/internal/devices"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

// ShadowSetter is what Engine needs from the device shadow — satisfied
// directly by *internal/shadow.Store (no adapter needed: the method set
// already matches).
type ShadowSetter interface {
	SetDesired(ctx context.Context, deviceID string, d shadow.Desired) (shadow.Desired, error)
	GetDesired(ctx context.Context, deviceID string) (shadow.Desired, uint64, error)
	GetReported(ctx context.Context, deviceID string) (*shadow.Reported, error)
}

// DevicePublisher retained-publishes desired config to a device —
// satisfied directly by *internal/shadow.Publisher.
type DevicePublisher interface {
	PublishDesired(deviceID string, d shadow.Desired) error
}

// DeviceLister is what Engine needs from the device registry — satisfied
// directly by *internal/devices.Store.
type DeviceLister interface {
	List(ctx context.Context) ([]*devices.Device, error)
}

// ErrorSignal is the "error rate" health input — satisfied directly by
// *internal/alerts.Store (FiringCountForDevices).
type ErrorSignal interface {
	FiringCountForDevices(ctx context.Context, deviceIDs []string, since time.Time) (int, error)
}

type Engine struct {
	mu     sync.Mutex
	active map[string]*Rollout

	store        RolloutStore
	shadowStore  ShadowSetter
	shadowPub    DevicePublisher
	deviceLister DeviceLister
	errorSignal  ErrorSignal
	eventPub     EventPublisher
	logger       *slog.Logger

	disconnectStaleAfter time.Duration
}

func NewEngine(store RolloutStore, shadowStore ShadowSetter, shadowPub DevicePublisher,
	deviceLister DeviceLister, errorSignal ErrorSignal, eventPub EventPublisher,
	disconnectStaleAfter time.Duration, logger *slog.Logger) *Engine {
	return &Engine{
		active:               make(map[string]*Rollout),
		store:                store,
		shadowStore:          shadowStore,
		shadowPub:            shadowPub,
		deviceLister:         deviceLister,
		errorSignal:          errorSignal,
		eventPub:             eventPub,
		disconnectStaleAfter: disconnectStaleAfter,
		logger:               logger,
	}
}

// ResumeAll loads every non-terminal rollout into the active set — called
// once at startup, before Run. Each rollout's remaining bake time is
// re-derived from CurrentStageStartedAt (already loaded from the store),
// not tracked separately, so a rollout picks back up exactly where it was
// without any special "resume" bookkeeping: the very next tick evaluates
// it exactly as if the process had never restarted. Distinct from the
// per-rollout Resume below (un-pausing one rollout via the API).
func (e *Engine) ResumeAll(ctx context.Context) error {
	rollouts, err := e.store.ListNonTerminal(ctx)
	if err != nil {
		return fmt.Errorf("rollout: loading non-terminal rollouts: %w", err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rollouts {
		e.active[r.ID] = r
	}
	if e.logger != nil && len(rollouts) > 0 {
		e.logger.Info("rollout: resumed in-flight rollouts", "count", len(rollouts))
	}
	return nil
}

// Run ticks every interval until ctx is cancelled, evaluating every active
// rollout on each tick.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.tickAll(ctx)
		}
	}
}

func (e *Engine) tickAll(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.active {
		e.tickOne(ctx, r)
	}
}

// tickOne evaluates one rollout: health first (a breach rolls back
// immediately, regardless of how much bake time is left), then — only if
// healthy — whether the current stage's bake period has elapsed.
func (e *Engine) tickOne(ctx context.Context, r *Rollout) {
	if r.State != Running {
		return
	}

	targets, err := e.store.ListTargets(ctx, r.ID)
	if err != nil {
		e.logf("rollout: listing targets for %s failed: %v", r.ID, err)
		return
	}
	targetIDs := make([]string, len(targets))
	byID := make(map[string]Target, len(targets))
	for i, t := range targets {
		targetIDs[i] = t.DeviceID
		byID[t.DeviceID] = t
	}

	rejected, disconnected, errored := e.evaluateStageHealth(ctx, r, targetIDs, byID)
	if breached, reasons := EvaluateHealth(r.HealthCriteria, len(targetIDs), rejected, disconnected, errored); breached {
		e.rollbackLocked(ctx, r, targets, reasons)
		return
	}

	stage := r.Stages[r.CurrentStageIndex]
	if time.Since(r.CurrentStageStartedAt) < stage.BakeDuration {
		return
	}

	if r.CurrentStageIndex == len(r.Stages)-1 {
		e.completeLocked(ctx, r)
		return
	}
	e.advanceLocked(ctx, r)
}

func (e *Engine) evaluateStageHealth(ctx context.Context, r *Rollout, targetIDs []string, byID map[string]Target) (rejected, disconnected, errored int) {
	all, err := e.deviceLister.List(ctx)
	if err != nil {
		e.logf("rollout: listing devices for health check failed: %v", err)
	}
	lastSeen := make(map[string]*time.Time, len(all))
	for _, d := range all {
		lastSeen[d.ID] = d.LastSeen
	}
	now := time.Now()
	for _, id := range targetIDs {
		ls := lastSeen[id]
		if ls == nil || now.Sub(*ls) > e.disconnectStaleAfter {
			disconnected++
		}
		rep, err := e.shadowStore.GetReported(ctx, id)
		if err == nil && rep != nil && rep.Rejected && rep.RejectedRevision == byID[id].PushedRevision {
			rejected++
		}
	}
	if e.errorSignal != nil {
		errored, err = e.errorSignal.FiringCountForDevices(ctx, targetIDs, r.CurrentStageStartedAt)
		if err != nil {
			e.logf("rollout: error-rate signal failed: %v", err)
			errored = 0
		}
	}
	return rejected, disconnected, errored
}

// Create validates r, persists it, applies stage 0 immediately, and adds
// it to the active set. There's no separate "start" step — matching the
// blueprint's REST table having none.
func (e *Engine) Create(ctx context.Context, r *Rollout) (*Rollout, error) {
	if err := validate(r); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	r.ID = uuid.NewString()
	r.State = Running
	r.CurrentStageIndex = 0
	r.CurrentStageStartedAt = time.Now()
	if err := e.store.Create(ctx, r); err != nil {
		return nil, err
	}
	e.applyStage(ctx, r)
	e.active[r.ID] = r
	e.publish(ctx, r, EventStageStarted, map[string]any{"stage_index": 0})
	return r, nil
}

func validate(r *Rollout) error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Stages) == 0 {
		return fmt.Errorf("at least one stage is required")
	}
	prev := 0
	for i, s := range r.Stages {
		if s.Percent <= prev || s.Percent > 100 {
			return fmt.Errorf("stage %d: percent must be increasing and at most 100 (got %d after %d)", i, s.Percent, prev)
		}
		prev = s.Percent
	}
	return nil
}

// applyStage pushes r's desired config to every device the current
// stage's target set includes that hasn't already been targeted by an
// earlier stage of this same rollout (RecordTarget is a no-op for a
// device already recorded, so calling this on every stage transition is
// safe and simple rather than needing to diff target sets by hand).
func (e *Engine) applyStage(ctx context.Context, r *Rollout) {
	all, err := e.deviceLister.List(ctx)
	if err != nil {
		e.logf("rollout: listing devices for %s failed: %v", r.ID, err)
		return
	}
	cohortIDs := SelectCohort(all, r.Cohort)
	targets := StageTargets(cohortIDs, r.Stages[r.CurrentStageIndex].Percent)

	existing, err := e.store.ListTargets(ctx, r.ID)
	if err != nil {
		e.logf("rollout: listing existing targets for %s failed: %v", r.ID, err)
		return
	}
	already := make(map[string]bool, len(existing))
	for _, t := range existing {
		already[t.DeviceID] = true
	}

	for _, id := range targets {
		if already[id] {
			continue
		}
		preDesired, preRev, err := e.shadowStore.GetDesired(ctx, id)
		if err != nil {
			e.logf("rollout: reading pre-rollout desired for %s failed: %v", id, err)
			continue
		}
		var preDesiredPtr *shadow.Desired
		if preRev > 0 {
			preDesiredPtr = &preDesired
		}

		pushed, err := e.shadowStore.SetDesired(ctx, id, r.DesiredConfig)
		if err != nil {
			e.logf("rollout: setting desired for %s failed: %v", id, err)
			continue
		}
		if err := e.store.RecordTarget(ctx, r.ID, Target{
			DeviceID: id, IncludedAtStage: r.CurrentStageIndex,
			PreDesired: preDesiredPtr, PreRevision: preRev, PushedRevision: pushed.Revision,
		}); err != nil {
			e.logf("rollout: recording target %s for %s failed: %v", id, r.ID, err)
		}
		e.publishDesired(id, pushed)
	}
}

// publishDesired guards every DevicePublisher call in this file: shadowPub
// can be nil (cmd/control degrades gracefully when its MQTT connection
// fails at startup, same as PUT /v1/devices/{id}/shadow/desired already
// does — see main.go), and a nil check here is what actually catches that,
// since main.go is careful to pass a genuinely nil interface rather than a
// typed-nil pointer for exactly this reason.
func (e *Engine) publishDesired(deviceID string, d shadow.Desired) {
	if e.shadowPub == nil {
		e.logf("rollout: no MQTT publisher available, desired config for %s saved but not pushed", deviceID)
		return
	}
	if err := e.shadowPub.PublishDesired(deviceID, d); err != nil {
		e.logf("rollout: publishing desired to %s failed: %v", deviceID, err)
	}
}

func (e *Engine) advanceLocked(ctx context.Context, r *Rollout) {
	r.CurrentStageIndex++
	r.CurrentStageStartedAt = time.Now()
	if err := e.store.AdvanceStage(ctx, r.ID, r.CurrentStageIndex, r.CurrentStageStartedAt); err != nil {
		e.logf("rollout: persisting stage advance for %s failed: %v", r.ID, err)
	}
	e.applyStage(ctx, r)
	e.publish(ctx, r, EventStageAdvanced, map[string]any{"stage_index": r.CurrentStageIndex})
}

func (e *Engine) completeLocked(ctx context.Context, r *Rollout) {
	r.State = Completed
	if err := e.store.UpdateState(ctx, r.ID, Completed); err != nil {
		e.logf("rollout: persisting completion for %s failed: %v", r.ID, err)
	}
	delete(e.active, r.ID)
	e.publish(ctx, r, EventCompleted, nil)
}

// rollbackLocked reverts every device this rollout has ever targeted to
// its pre-rollout snapshot. A device with no PreDesired (it had no
// desired config before this rollout) is left as-is — there's no "delete
// desired config" operation in internal/shadow to revert *to* nothing,
// and in practice rollback matters most for cohorts of already-configured
// devices, not brand-new ones.
func (e *Engine) rollbackLocked(ctx context.Context, r *Rollout, targets []Target, reasons []string) {
	for _, t := range targets {
		if t.PreDesired == nil {
			continue
		}
		pushed, err := e.shadowStore.SetDesired(ctx, t.DeviceID, *t.PreDesired)
		if err != nil {
			e.logf("rollout: rollback SetDesired for %s failed: %v", t.DeviceID, err)
			continue
		}
		e.publishDesired(t.DeviceID, pushed)
	}
	r.State = Aborted
	if err := e.store.UpdateState(ctx, r.ID, Aborted); err != nil {
		e.logf("rollout: persisting rollback for %s failed: %v", r.ID, err)
	}
	delete(e.active, r.ID)
	e.publish(ctx, r, EventHealthBreached, map[string]any{"reasons": reasons})
	e.publish(ctx, r, EventAborted, map[string]any{"reasons": reasons})
}

// Pause halts a running rollout's ticking (no advance, no health checks)
// without reverting anything already applied.
func (e *Engine) Pause(ctx context.Context, id string) (*Rollout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.active[id]
	if !ok || r.State != Running {
		return nil, fmt.Errorf("rollout: %s is not running", id)
	}
	r.State = Paused
	if err := e.store.UpdateState(ctx, id, Paused); err != nil {
		return nil, err
	}
	e.publish(ctx, r, EventPaused, nil)
	return r, nil
}

// Resume un-pauses a rollout — its bake clock isn't adjusted for time
// spent paused, matching how a process restart (Resume, the startup
// method above) also just picks up from CurrentStageStartedAt as-is.
func (e *Engine) Resume(ctx context.Context, id string) (*Rollout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.active[id]
	if !ok || r.State != Paused {
		return nil, fmt.Errorf("rollout: %s is not paused", id)
	}
	r.State = Running
	if err := e.store.UpdateState(ctx, id, Running); err != nil {
		return nil, err
	}
	e.publish(ctx, r, EventResumed, nil)
	return r, nil
}

// Abort immediately rolls back a running or paused rollout, regardless of
// health.
func (e *Engine) Abort(ctx context.Context, id string) (*Rollout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.active[id]
	if !ok {
		return nil, fmt.Errorf("rollout: %s is not active", id)
	}
	targets, err := e.store.ListTargets(ctx, id)
	if err != nil {
		return nil, err
	}
	e.rollbackLocked(ctx, r, targets, []string{"manually aborted"})
	return r, nil
}

func (e *Engine) Get(ctx context.Context, id string) (*Rollout, error) {
	e.mu.Lock()
	if r, ok := e.active[id]; ok {
		e.mu.Unlock()
		return r, nil
	}
	e.mu.Unlock()
	return e.store.Get(ctx, id)
}

func (e *Engine) List(ctx context.Context) ([]*Rollout, error) {
	return e.store.List(ctx)
}

func (e *Engine) publish(ctx context.Context, r *Rollout, evtType EventType, detail map[string]any) {
	if e.eventPub == nil {
		return
	}
	evt := RolloutEvent{RolloutID: r.ID, Type: evtType, StageIndex: r.CurrentStageIndex, Detail: detail, TimestampMS: time.Now().UnixMilli()}
	if err := e.eventPub.Publish(ctx, evt); err != nil {
		e.logf("rollout: publishing event for %s failed: %v", r.ID, err)
	}
}

func (e *Engine) logf(format string, args ...any) {
	if e.logger != nil {
		e.logger.Error(fmt.Sprintf(format, args...))
	}
}
