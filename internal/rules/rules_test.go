package rules

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleYAML = `
rules:
  - name: accel-spike
    sensor_type: "accel.*"
    type: zscore
    threshold: 3.5
    consecutive: 3
    severity: warning
  - name: cpu-step
    sensor_type: cpu
    type: rate_of_change
    threshold: 25
    consecutive: 2
    severity: warning
  - name: device-silent
    sensor_type: "*"
    type: silence
    silence_timeout: 60s
    severity: critical
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(cfg.Rules))
	}
	if cfg.Rules[2].SilenceTimeout != 60*time.Second {
		t.Errorf("silence_timeout = %v, want 60s", cfg.Rules[2].SilenceTimeout)
	}
}

func TestLoad_RejectsMissingFields(t *testing.T) {
	path := writeTemp(t, "rules:\n  - type: zscore\n    threshold: 1\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a rule with no name")
	}
}

func TestRule_Matches(t *testing.T) {
	cases := []struct {
		ruleSensor string
		reading    string
		want       bool
	}{
		{"*", "anything", true},
		{"accel.*", "accel.x", true},
		{"accel.*", "accelerometer.x", false}, // must not partial-match past the dot
		{"accel.*", "gyro.x", false},
		{"cpu", "cpu", true},
		{"cpu", "cpu.load", false},
	}
	for _, c := range cases {
		r := Rule{SensorType: c.ruleSensor}
		if got := r.Matches(c.reading); got != c.want {
			t.Errorf("Rule{%q}.Matches(%q) = %v, want %v", c.ruleSensor, c.reading, got, c.want)
		}
	}
}

func TestConfig_ForSensor(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	matched := cfg.ForSensor("accel.x")
	if len(matched) != 2 { // accel-spike + device-silent ("*")
		t.Fatalf("got %d matches for accel.x, want 2: %+v", len(matched), matched)
	}
}

func TestWatcher_ReloadsOnChange(t *testing.T) {
	path := writeTemp(t, sampleYAML)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	w, err := NewWatcher(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Current().Rules) != 3 {
		t.Fatalf("initial load: got %d rules, want 3", len(w.Current().Rules))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, 10*time.Millisecond)

	// Ensure the new mtime is observably later than the original write,
	// then replace the file with a single-rule config.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte("rules:\n  - name: only-one\n    sensor_type: \"*\"\n    type: silence\n    silence_timeout: 5s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(w.Current().Rules) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watcher did not pick up the change within the deadline; still have %d rules", len(w.Current().Rules))
}
