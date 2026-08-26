package rollout

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/devices"
	"github.com/Ritesh956/SenseGrid/internal/shadow"
)

// ---- in-memory fakes standing in for Postgres/NATS/MQTT — see engine.go's
// doc comment for why the Engine is built against interfaces specifically
// so these are possible without a real test-DB/broker harness. ----

type fakeRolloutStore struct {
	mu       sync.Mutex
	rollouts map[string]*Rollout
	targets  map[string][]Target
}

func newFakeRolloutStore() *fakeRolloutStore {
	return &fakeRolloutStore{rollouts: map[string]*Rollout{}, targets: map[string][]Target{}}
}

func (f *fakeRolloutStore) Create(_ context.Context, r *Rollout) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *r
	f.rollouts[r.ID] = &cp
	return nil
}

func (f *fakeRolloutStore) Get(_ context.Context, id string) (*Rollout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rollouts[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (f *fakeRolloutStore) List(_ context.Context) ([]*Rollout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Rollout
	for _, r := range f.rollouts {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeRolloutStore) ListNonTerminal(_ context.Context) ([]*Rollout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Rollout
	for _, r := range f.rollouts {
		if r.State == Running || r.State == Paused {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeRolloutStore) UpdateState(_ context.Context, id string, state State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rollouts[id]; ok {
		r.State = state
	}
	return nil
}

func (f *fakeRolloutStore) AdvanceStage(_ context.Context, id string, stageIndex int, startedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rollouts[id]; ok {
		r.CurrentStageIndex = stageIndex
		r.CurrentStageStartedAt = startedAt
	}
	return nil
}

func (f *fakeRolloutStore) RecordTarget(_ context.Context, rolloutID string, t Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.targets[rolloutID] {
		if existing.DeviceID == t.DeviceID {
			return nil // ON CONFLICT DO NOTHING, matching store.go's real behavior
		}
	}
	f.targets[rolloutID] = append(f.targets[rolloutID], t)
	return nil
}

func (f *fakeRolloutStore) ListTargets(_ context.Context, rolloutID string) ([]Target, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Target, len(f.targets[rolloutID]))
	copy(out, f.targets[rolloutID])
	return out, nil
}

type fakeShadow struct {
	mu       sync.Mutex
	desired  map[string]shadow.Desired
	revision map[string]uint64
	reported map[string]*shadow.Reported
}

func newFakeShadow() *fakeShadow {
	return &fakeShadow{desired: map[string]shadow.Desired{}, revision: map[string]uint64{}, reported: map[string]*shadow.Reported{}}
}

func (f *fakeShadow) SetDesired(_ context.Context, deviceID string, d shadow.Desired) (shadow.Desired, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revision[deviceID]++
	d.Revision = f.revision[deviceID]
	f.desired[deviceID] = d
	return d, nil
}

func (f *fakeShadow) GetDesired(_ context.Context, deviceID string) (shadow.Desired, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.desired[deviceID]
	if !ok {
		return shadow.Desired{}, 0, nil
	}
	return d, f.revision[deviceID], nil
}

func (f *fakeShadow) GetReported(_ context.Context, deviceID string) (*shadow.Reported, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reported[deviceID], nil
}

func (f *fakeShadow) setReported(deviceID string, r *shadow.Reported) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported[deviceID] = r
}

type fakeDevicePublisher struct {
	mu        sync.Mutex
	published map[string]shadow.Desired
}

func newFakeDevicePublisher() *fakeDevicePublisher {
	return &fakeDevicePublisher{published: map[string]shadow.Desired{}}
}

func (f *fakeDevicePublisher) PublishDesired(deviceID string, d shadow.Desired) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published[deviceID] = d
	return nil
}

func (f *fakeDevicePublisher) get(deviceID string) (shadow.Desired, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.published[deviceID]
	return d, ok
}

func (f *fakeDevicePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

type fakeDeviceLister struct {
	mu   sync.Mutex
	devs []*devices.Device
}

func (f *fakeDeviceLister) List(_ context.Context) ([]*devices.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*devices.Device, len(f.devs))
	copy(out, f.devs)
	return out, nil
}

type fakeErrorSignal struct{ count int }

func (f *fakeErrorSignal) FiringCountForDevices(_ context.Context, _ []string, _ time.Time) (int, error) {
	return f.count, nil
}

type fakeEventPublisher struct {
	mu     sync.Mutex
	events []RolloutEvent
}

func (f *fakeEventPublisher) Publish(_ context.Context, evt RolloutEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
	return nil
}

func makeDevices(n int) []*devices.Device {
	out := make([]*devices.Device, n)
	now := time.Now()
	for i := range out {
		out[i] = &devices.Device{ID: idsN(n)[i], Type: "phone", LastSeen: &now}
	}
	return out
}

func f64(v float64) *float64 { return &v }

// TestEngine_FullSuccessfulRollout exercises the blueprint's first
// required scenario: a two-stage rollout with no health issues reaches
// every cohort device and ends Completed.
func TestEngine_FullSuccessfulRollout(t *testing.T) {
	ctx := context.Background()
	devs := makeDevices(20)
	deviceIDs := make([]string, len(devs))
	for i, d := range devs {
		deviceIDs[i] = d.ID
	}

	store := newFakeRolloutStore()
	sh := newFakeShadow()
	pub := newFakeDevicePublisher()
	lister := &fakeDeviceLister{devs: devs}
	events := &fakeEventPublisher{}

	engine := NewEngine(store, sh, pub, lister, &fakeErrorSignal{}, events, time.Hour, nil)

	r := &Rollout{
		Name:          "bump rate",
		DesiredConfig: shadow.Desired{SampleRateHz: f64(5)},
		Stages: []Stage{
			{Percent: 50, BakeDuration: 15 * time.Millisecond},
			{Percent: 100, BakeDuration: 15 * time.Millisecond},
		},
		HealthCriteria: HealthCriteria{}, // no limits set — never breaches
	}
	created, err := engine.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	stage0Targets := StageTargets(deviceIDs, 50)
	if pub.count() != len(stage0Targets) {
		t.Fatalf("after stage 0: published to %d devices, want %d", pub.count(), len(stage0Targets))
	}

	time.Sleep(25 * time.Millisecond)
	engine.tickAll(ctx)
	got, _ := engine.Get(ctx, created.ID)
	if got.State != Running || got.CurrentStageIndex != 1 {
		t.Fatalf("after stage 0 bake: state=%s stage=%d, want running/1", got.State, got.CurrentStageIndex)
	}

	time.Sleep(25 * time.Millisecond)
	engine.tickAll(ctx)
	got, _ = engine.Get(ctx, created.ID)
	if got.State != Completed {
		t.Fatalf("after stage 1 bake: state=%s, want completed", got.State)
	}
	if pub.count() != len(deviceIDs) {
		t.Fatalf("after completion: published to %d devices, want all %d", pub.count(), len(deviceIDs))
	}
	for _, id := range deviceIDs {
		d, ok := pub.get(id)
		if !ok || d.SampleRateHz == nil || *d.SampleRateHz != 5 {
			t.Errorf("device %s: expected sample_rate_hz=5 published, got %+v (ok=%v)", id, d, ok)
		}
	}
}

// TestEngine_AutoRollbackOnBreach exercises the blueprint's second
// required scenario: a rejection past the configured threshold rolls back
// every targeted device to its pre-rollout config, before the stage's
// (long) bake period would have naturally elapsed.
func TestEngine_AutoRollbackOnBreach(t *testing.T) {
	ctx := context.Background()
	devs := makeDevices(10)
	deviceIDs := make([]string, len(devs))
	for i, d := range devs {
		deviceIDs[i] = d.ID
	}

	store := newFakeRolloutStore()
	sh := newFakeShadow()
	pub := newFakeDevicePublisher()
	lister := &fakeDeviceLister{devs: devs}
	events := &fakeEventPublisher{}

	// Every device already has a desired config before the rollout —
	// this is what rollback restores each targeted device to.
	for _, id := range deviceIDs {
		if _, err := sh.SetDesired(ctx, id, shadow.Desired{SampleRateHz: f64(1)}); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewEngine(store, sh, pub, lister, &fakeErrorSignal{}, events, time.Hour, nil)

	r := &Rollout{
		Name:           "risky change",
		DesiredConfig:  shadow.Desired{SampleRateHz: f64(50)},
		Stages:         []Stage{{Percent: 100, BakeDuration: time.Hour}}, // long enough that only a breach can end this
		HealthCriteria: HealthCriteria{MaxRejectionRate: 0.1},
	}
	created, err := engine.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}

	targeted := StageTargets(deviceIDs, 100)
	if len(targeted) < 2 {
		t.Fatal("expected at least two targeted devices at 100%")
	}
	// Reject 2 of the (10) targeted devices — 20%, clearly over the 10%
	// threshold (rejecting exactly 1/10 == 10% would not breach: the
	// threshold check is strictly-greater-than, see EvaluateHealth).
	for _, id := range targeted[:2] {
		pushed, _ := pub.get(id)
		sh.setReported(id, &shadow.Reported{Rejected: true, RejectedRevision: pushed.Revision})
	}

	engine.tickAll(ctx) // no sleep: this must trip on health alone, well before the 1h bake

	got, _ := engine.Get(ctx, created.ID)
	if got.State != Aborted {
		t.Fatalf("state = %s, want aborted", got.State)
	}
	for _, id := range targeted {
		d, _, err := sh.GetDesired(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.SampleRateHz == nil || *d.SampleRateHz != 1 {
			t.Errorf("device %s: expected rollback to sample_rate_hz=1, got %+v", id, d)
		}
	}
}

// TestEngine_ResumptionAfterRestart exercises the blueprint's third
// required scenario: a fresh Engine, backed by the same (fake) store a
// prior Engine instance wrote to, picks an in-flight rollout back up and
// carries it to completion — modeling a cmd/control process restart,
// where only the Go process is new and Postgres/NATS/the devices
// themselves are unchanged.
func TestEngine_ResumptionAfterRestart(t *testing.T) {
	ctx := context.Background()
	devs := makeDevices(20)
	deviceIDs := make([]string, len(devs))
	for i, d := range devs {
		deviceIDs[i] = d.ID
	}

	store := newFakeRolloutStore() // shared across both "process" instances
	sh := newFakeShadow()
	pub := newFakeDevicePublisher()
	lister := &fakeDeviceLister{devs: devs}

	engineA := NewEngine(store, sh, pub, lister, &fakeErrorSignal{}, &fakeEventPublisher{}, time.Hour, nil)
	r := &Rollout{
		Name:          "resumable",
		DesiredConfig: shadow.Desired{SampleRateHz: f64(10)},
		Stages: []Stage{
			{Percent: 50, BakeDuration: 15 * time.Millisecond},
			{Percent: 100, BakeDuration: 15 * time.Millisecond},
		},
	}
	created, err := engineA.Create(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	// engineA is discarded here without ever ticking — simulating a crash
	// immediately after stage 0 was applied and persisted.

	engineB := NewEngine(store, sh, pub, lister, &fakeErrorSignal{}, &fakeEventPublisher{}, time.Hour, nil)
	if err := engineB.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}

	time.Sleep(25 * time.Millisecond)
	engineB.tickAll(ctx)
	got, err := engineB.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("engineB does not know about the rollout engineA created — resumption failed")
	}
	if got.State != Running || got.CurrentStageIndex != 1 {
		t.Fatalf("after resuming + one bake period: state=%s stage=%d, want running/1", got.State, got.CurrentStageIndex)
	}

	time.Sleep(25 * time.Millisecond)
	engineB.tickAll(ctx)
	got, _ = engineB.Get(ctx, created.ID)
	if got.State != Completed {
		t.Fatalf("state = %s, want completed", got.State)
	}
	if pub.count() != len(deviceIDs) {
		t.Fatalf("published to %d devices, want all %d", pub.count(), len(deviceIDs))
	}
}
