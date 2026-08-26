// FleetManager owns the set of virtual devices and everything that scales
// or chaos-tests them: claiming new devices from the bulk-issued token
// pool, ramping the running count up/down, and simulating a network
// partition on a cohort — the operations test/chaos's scripts drive
// through controlapi.go.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/provisioning"
)

// tokenPool is the bulk-issued registration tokens (see cmd/control/token.go's
// -count flag) a fleet run claims new device identities from, one token per
// device, consumed in order. Thread-safe: manager.startSlot calls take()
// concurrently across a scale-up's worker pool.
type tokenPool struct {
	mu     sync.Mutex
	tokens []string
	next   int
}

func loadTokenPool(path string) (*tokenPool, error) {
	if path == "" {
		return &tokenPool{}, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		// Not fatal: the fleet starts inert (FLEET_TARGET_DEVICES=0) on a
		// fresh `docker compose up`, before anyone has run `control token
		// create -count N` yet — see deploy/docker-compose.yml's fleet
		// service comment. A later POST /fleet/scale with no tokens left
		// simply fails to claim new devices, logged per-slot.
		return &tokenPool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fleet: opening tokens file: %w", err)
	}
	defer f.Close()

	var toks []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		toks = append(toks, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &tokenPool{tokens: toks}, nil
}

func (p *tokenPool) take() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.next >= len(p.tokens) {
		return "", false
	}
	t := p.tokens[p.next]
	p.next++
	return t, true
}

func (p *tokenPool) remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tokens) - p.next
}

// deviceSlot is one position in the fleet, 0-indexed and stable for the
// life of the process. slot.dev survives a scale-down (running=false) so a
// later scale-up reuses the same claimed identity instead of burning
// another token — devices don't get "re-provisioned" any more than a real
// device would just because it was powered off for a while.
type deviceSlot struct {
	dev     *virtualDevice
	cancel  context.CancelFunc
	running bool
}

type FleetManager struct {
	mu      sync.Mutex
	baseCtx context.Context
	devices []*deviceSlot
	target  int

	cfg        fleetConfig
	tokens     *tokenPool
	brokerURL  string
	caFile     string
	tlsCfg     *tls.Config
	controlURL string

	rtCfg   *atomic.Pointer[runtimeConfig]
	metrics *fleetMetrics
	logger  *slog.Logger
}

func NewFleetManager(baseCtx context.Context, cfg fleetConfig, brokerURL, caFile string, tlsCfg *tls.Config, tokens *tokenPool, metrics *fleetMetrics, logger *slog.Logger) *FleetManager {
	rtCfg := &atomic.Pointer[runtimeConfig]{}
	rtCfg.Store(ptr(runtimeConfigFromFleetConfig(cfg)))
	return &FleetManager{
		baseCtx:    baseCtx,
		cfg:        cfg,
		tokens:     tokens,
		brokerURL:  brokerURL,
		caFile:     caFile,
		tlsCfg:     tlsCfg,
		controlURL: cfg.ControlURL,
		rtCfg:      rtCfg,
		metrics:    metrics,
		logger:     logger,
	}
}

// Scale brings the fleet to exactly target running devices: starting
// (claiming if needed) new ones, stopping the excess. New connections are
// staggered across cfg.RampWindow so a big jump doesn't open hundreds of
// TCP handshakes in the same instant — per-step pacing (10, 50, 200...)
// is test/chaos/ramp.sh's job, this is just the jitter within one step.
func (m *FleetManager) Scale(target int) {
	if target < 0 {
		target = 0
	}

	m.mu.Lock()
	for len(m.devices) < target {
		m.devices = append(m.devices, &deviceSlot{})
	}
	m.target = target

	type action struct {
		idx  int
		slot *deviceSlot
	}
	var toStart, toStop []action
	for i, slot := range m.devices {
		want := i < target
		if want && !slot.running {
			toStart = append(toStart, action{i, slot})
		} else if !want && slot.running {
			toStop = append(toStop, action{i, slot})
		}
	}
	m.mu.Unlock()

	for _, a := range toStop {
		m.mu.Lock()
		if a.slot.cancel != nil {
			a.slot.cancel()
		}
		a.slot.running = false
		m.mu.Unlock()
	}

	if len(toStart) > 0 {
		stagger := m.cfg.RampWindow / time.Duration(len(toStart))
		if stagger < 2*time.Millisecond {
			stagger = 2 * time.Millisecond
		}
		if stagger > 200*time.Millisecond {
			stagger = 200 * time.Millisecond
		}

		sem := make(chan struct{}, 20) // bounds concurrent claim HTTP calls against control
		var wg sync.WaitGroup
		for k, a := range toStart {
			wg.Add(1)
			sem <- struct{}{}
			go func(k int, a action) {
				defer wg.Done()
				defer func() { <-sem }()
				m.startSlot(a.idx, a.slot, time.Duration(k)*stagger)
			}(k, a)
		}
		wg.Wait()
	}

	m.updateMetrics()
}

func (m *FleetManager) startSlot(idx int, slot *deviceSlot, startDelay time.Duration) {
	m.mu.Lock()
	var creds provisioning.Credentials
	haveCreds := slot.dev != nil
	if haveCreds {
		creds = slot.dev.creds
	}
	m.mu.Unlock()

	if !haveCreds {
		stateFile := filepath.Join(m.cfg.StateDir, fmt.Sprintf("device-%05d.json", idx))
		// Only spend a token if there's no cached credential file to load
		// instead — otherwise every fleet container restart (not just a
		// fresh claim) would burn through the pool re-"claiming" devices
		// it already has, since this in-memory manager has no record of
		// last run's token usage across a restart.
		var token string
		if _, err := os.Stat(stateFile); err != nil {
			token, _ = m.tokens.take()
		}
		claimCtx, cancel := context.WithTimeout(m.baseCtx, 20*time.Second)
		c, err := provisioning.LoadOrClaim(claimCtx, stateFile, m.controlURL, token, m.caFile)
		cancel()
		if err != nil {
			m.logger.Error("fleet: claiming device failed", "idx", idx, "err", err)
			return
		}
		creds = c
	}

	dev := newVirtualDevice(idx, creds, m.brokerURL, m.tlsCfg, int(m.cfg.SampleInterval.Milliseconds()), m.rtCfg, m.metrics, m.logger)
	devCtx, cancel := context.WithCancel(m.baseCtx)

	m.mu.Lock()
	slot.dev = dev
	slot.cancel = cancel
	slot.running = true
	m.mu.Unlock()

	go dev.run(devCtx, startDelay)
}

// Partition simulates a network split for count currently-connected
// devices for duration, returning the device IDs picked so a chaos script
// can act on exactly that cohort (e.g. push a config change to it via
// cmd/control's shadow API) before healing. Each device heals itself
// independently once duration elapses (device.go's sampleLoop) — Partition
// itself doesn't block or track healing.
func (m *FleetManager) Partition(count int, duration time.Duration) []string {
	m.mu.Lock()
	var candidates []*virtualDevice
	for _, slot := range m.devices {
		if slot.running && slot.dev != nil && !slot.dev.stats.partitioned.Load() {
			candidates = append(candidates, slot.dev)
		}
	}
	m.mu.Unlock()

	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if count > len(candidates) {
		count = len(candidates)
	}

	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		dev := candidates[i]
		select {
		case dev.partitionCh <- duration:
			ids = append(ids, dev.creds.DeviceID)
		default:
		}
	}
	return ids
}

// UpdateRuntimeConfig applies a partial patch to the shared runtimeConfig
// every device reads each tick and returns the resulting value.
func (m *FleetManager) UpdateRuntimeConfig(patch configPatch) runtimeConfig {
	cur := *m.rtCfg.Load()
	next := patch.apply(cur)
	m.rtCfg.Store(&next)
	return next
}

type deviceStatus struct {
	DeviceID    string `json:"device_id"`
	Connected   bool   `json:"connected"`
	Partitioned bool   `json:"partitioned"`
	Seq         uint64 `json:"seq"`
}

type fleetStatus struct {
	Target             int            `json:"target"`
	Running            int            `json:"running"`
	Connected          int            `json:"connected"`
	Partitioned        int            `json:"partitioned"`
	ClaimedTotal       int            `json:"claimed_total"`
	TokensRemaining    int            `json:"tokens_remaining"`
	PublishedTotal     uint64         `json:"published_total"`
	PublishErrorsTotal uint64         `json:"publish_errors_total"`
	MalformedSentTotal uint64         `json:"malformed_sent_total"`
	AnomaliesSentTotal uint64         `json:"anomalies_sent_total"`
	ReconnectsTotal    uint64         `json:"reconnects_total"`
	RuntimeConfig      runtimeConfig  `json:"runtime_config"`
	Devices            []deviceStatus `json:"devices"`
}

func (m *FleetManager) Status() fleetStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := fleetStatus{
		Target:          m.target,
		RuntimeConfig:   *m.rtCfg.Load(),
		TokensRemaining: m.tokens.remaining(),
	}
	for _, slot := range m.devices {
		if slot.dev == nil {
			continue
		}
		st.ClaimedTotal++
		d := slot.dev
		connected := d.stats.connected.Load()
		partitioned := d.stats.partitioned.Load()
		if slot.running {
			st.Running++
		}
		if connected {
			st.Connected++
		}
		if partitioned {
			st.Partitioned++
		}
		st.PublishedTotal += d.stats.published.Load()
		st.PublishErrorsTotal += d.stats.publishErrors.Load()
		st.MalformedSentTotal += d.stats.malformedSent.Load()
		st.AnomaliesSentTotal += d.stats.anomaliesSent.Load()
		st.ReconnectsTotal += d.stats.reconnects.Load()
		st.Devices = append(st.Devices, deviceStatus{
			DeviceID: d.creds.DeviceID, Connected: connected, Partitioned: partitioned, Seq: d.stats.lastSeq.Load(),
		})
	}
	return st
}

func (m *FleetManager) updateMetrics() {
	if m.metrics == nil {
		return
	}
	st := m.Status()
	m.metrics.devicesTarget.Set(float64(st.Target))
	m.metrics.devicesRunning.Set(float64(st.Running))
	m.metrics.devicesConnected.Set(float64(st.Connected))
	m.metrics.devicesPartitioned.Set(float64(st.Partitioned))
	m.metrics.tokensRemaining.Set(float64(st.TokensRemaining))
}
