package anomaly

import "sync"

// Key identifies one rule's running state for one device/sensor.
type Key struct {
	DeviceID   string
	SensorType string
	RuleName   string
}

// Event is the edge-triggered result of feeding one observation into the
// Evaluator: most observations produce NoChange (still firing, still
// clear, or violating-but-not-yet-at-M-in-a-row). Fired/Cleared are the
// transitions the caller acts on.
type Event int

const (
	NoChange Event = iota
	Fired
	Cleared
)

type counters struct {
	violations int
	clears     int
	open       bool
}

// Evaluator adds the blueprint's "require M consecutive violations before
// firing (per-rule configurable) to suppress single-sample noise" on top
// of the pure detector functions in detectors.go. Symmetrically, it also
// requires M consecutive clears before resolving an open alert, so a
// signal oscillating right at the threshold doesn't flap fire/resolve on
// every other sample.
type Evaluator struct {
	mu    sync.Mutex
	state map[Key]*counters
}

func NewEvaluator() *Evaluator {
	return &Evaluator{state: make(map[Key]*counters)}
}

// Update feeds one observation (violating or not) for key, whose rule
// requires `consecutive` in-a-row observations to change firing state, and
// returns the transition (if any).
func (e *Evaluator) Update(key Key, violating bool, consecutive int) Event {
	if consecutive <= 0 {
		consecutive = 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	c, ok := e.state[key]
	if !ok {
		c = &counters{}
		e.state[key] = c
	}

	if violating {
		c.violations++
		c.clears = 0
		if !c.open && c.violations >= consecutive {
			c.open = true
			return Fired
		}
		return NoChange
	}

	c.clears++
	c.violations = 0
	if c.open && c.clears >= consecutive {
		c.open = false
		return Cleared
	}
	return NoChange
}

// Forget drops key's state entirely, e.g. when the device/sensor is
// evicted from the window registry for inactivity.
func (e *Evaluator) Forget(key Key) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.state, key)
}
