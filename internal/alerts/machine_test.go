package alerts

import "testing"

func TestMachine_Next(t *testing.T) {
	var m Machine

	cases := []struct {
		name    string
		current State
		event   Event
		want    State
		wantErr bool
	}{
		{"firing -> acknowledged", Firing, EventAcknowledge, Acknowledged, false},
		{"firing -> resolved (auto-clear)", Firing, EventClear, Resolved, false},
		{"acknowledged -> resolved", Acknowledged, EventClear, Resolved, false},
		{"acknowledged -> acknowledged (invalid, no double-ack)", Acknowledged, EventAcknowledge, Acknowledged, true},
		{"resolved is terminal: clear", Resolved, EventClear, Resolved, true},
		{"resolved is terminal: acknowledge", Resolved, EventAcknowledge, Resolved, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := m.Next(c.current, c.event)
			if (err != nil) != c.wantErr {
				t.Fatalf("Next(%s, %s): err = %v, wantErr = %v", c.current, c.event, err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("Next(%s, %s) = %s, want %s", c.current, c.event, got, c.want)
			}
		})
	}
}
