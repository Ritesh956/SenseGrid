// Package alerts is the Phase 3 alert state machine, its Postgres
// persistence, and its JetStream publish — firing / acknowledged /
// resolved, per the blueprint. The HTTP surface for the operator-driven
// acknowledge/resolve actions (POST /v1/alerts/{id}/ack, /resolve) is
// explicitly Phase 4 work in cmd/control; this package only exposes the
// Machine transition Acknowledge will need then, so Phase 4 doesn't have
// to touch this file to add it.
package alerts

import "fmt"

// State is one of an alert's three lifecycle states.
type State string

const (
	Firing       State = "firing"
	Acknowledged State = "acknowledged"
	Resolved     State = "resolved"
)

// Machine is the pure firing/acknowledged/resolved transition table, kept
// separate from Store's Postgres I/O so it's unit-testable without a
// database — this repo has no test-DB harness yet (internal/devices.Store
// isn't unit tested either, for the same reason).
type Machine struct{}

// Next returns the state after applying event to current, or an error if
// the transition isn't valid. Resolved is terminal: a resolved alert
// doesn't transition again — a fresh violation opens a new alert row
// instead (see Store.Open), which is also what keeps "never re-fire an
// already-firing alert" true at the DB level via the partial unique index
// on open alerts.
func (Machine) Next(current State, event Event) (State, error) {
	switch current {
	case Firing:
		switch event {
		case EventAcknowledge:
			return Acknowledged, nil
		case EventClear:
			return Resolved, nil
		}
	case Acknowledged:
		switch event {
		case EventClear:
			return Resolved, nil
		}
	case Resolved:
		// terminal
	}
	return current, fmt.Errorf("alerts: invalid transition %s -> %s", current, event)
}

// Event is what drives a Machine transition.
type Event string

const (
	EventAcknowledge Event = "acknowledge"
	EventClear       Event = "clear" // the underlying detector condition cleared
)
