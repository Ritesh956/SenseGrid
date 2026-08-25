// Package rules loads and hot-reloads the Phase 3 detector rules from a
// YAML file (deploy/rules.yaml by default — see internal/config's
// RulesFile), rather than hardcoding thresholds. Reload is a plain mtime
// poll (see Watcher in watch.go), not fsnotify: this repo already prefers
// small hand-rolled solutions over pulling in a library for one narrow use
// (see internal/migrations' doc comment on the same tradeoff), and a poll
// avoids fsnotify's platform quirks on the Windows dev environment this
// project targets.
package rules

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DetectorType is which of Phase 3's three detectors a Rule configures.
type DetectorType string

const (
	ZScore       DetectorType = "zscore"
	RateOfChange DetectorType = "rate_of_change"
	Silence      DetectorType = "silence"
)

// Rule is one detector configured against a sensor_type match. Threshold
// and consecutive-violation counts are per-rule, not hardcoded, per the
// blueprint's "hot-reloadable YAML, not hardcoded thresholds". The window
// itself (count/age bound, EWMA α) is deliberately *not* a per-rule
// setting: the blueprint describes the windowed stage and the detectors as
// two separate concerns ("windowed stage... Detectors: rolling z-score,
// rate-of-change..."), and in this implementation there is exactly one
// shared window per (device_id, sensor_type) — see internal/window.Registry
// and cmd/config's Window* settings — that every rule matching that sensor
// evaluates against, rather than each rule getting its own window sized
// independently (which would mean re-computing multiple windows per
// reading for no benefit this project needs).
type Rule struct {
	Name       string       `yaml:"name"`
	SensorType string       `yaml:"sensor_type"` // exact match, "*" (any), or "prefix.*"
	Type       DetectorType `yaml:"type"`
	Severity   string       `yaml:"severity"`

	// zscore: fire when |value - window.Mean()| / window.StdDev() exceeds Threshold.
	// rate_of_change: fire when |window.EWMA() - window.PrevEWMA()| exceeds Threshold.
	Threshold float64 `yaml:"threshold"`

	// silence: fire when the gap since the sensor's last reading exceeds SilenceTimeout.
	SilenceTimeout time.Duration `yaml:"silence_timeout"`

	// Consecutive is M: how many consecutive violations must occur before
	// firing (and, symmetrically, how many consecutive clears before
	// resolving) — suppresses single-sample noise. Defaults to 1 if unset.
	Consecutive int `yaml:"consecutive"`
}

// Matches reports whether the rule applies to sensorType. "*" matches
// anything; a trailing ".*" matches by prefix (e.g. "accel.*" matches
// "accel.x", "accel.y", "accel.z" — the flattened vector sensor_types
// cmd/processor already produces, see internal/telemetry); anything else
// must match exactly.
func (r Rule) Matches(sensorType string) bool {
	switch {
	case r.SensorType == "*":
		return true
	case strings.HasSuffix(r.SensorType, ".*"):
		return strings.HasPrefix(sensorType, strings.TrimSuffix(r.SensorType, "*"))
	default:
		return r.SensorType == sensorType
	}
}

// ConsecutiveOrDefault returns Consecutive, defaulting to 1 (fire/resolve
// immediately) when unset.
func (r Rule) ConsecutiveOrDefault() int {
	if r.Consecutive <= 0 {
		return 1
	}
	return r.Consecutive
}

// Config is the top-level shape of the rules YAML file.
type Config struct {
	Rules []Rule `yaml:"rules"`
}

// ForSensor returns every rule that matches sensorType.
func (c *Config) ForSensor(sensorType string) []Rule {
	if c == nil {
		return nil
	}
	var matched []Rule
	for _, r := range c.Rules {
		if r.Matches(sensorType) {
			matched = append(matched, r)
		}
	}
	return matched
}

// Load parses path into a Config.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rules: reading %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("rules: parsing %s: %w", path, err)
	}
	for i, r := range cfg.Rules {
		if r.Name == "" {
			return nil, fmt.Errorf("rules: rule at index %d has no name", i)
		}
		if r.SensorType == "" {
			return nil, fmt.Errorf("rules: rule %q has no sensor_type", r.Name)
		}
	}
	return &cfg, nil
}
