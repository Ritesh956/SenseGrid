// Phase 7 fleet-specific configuration, layered on top of internal/config's
// shared Config the same way cmd/hostagent's config.go layers runtime
// config on top of shadow.Desired — separate because these knobs (device
// count, misbehavior rates, token pool location) exist only for the
// synthetic fleet and would be dead fields on every other service's Config.
package main

import (
	"os"
	"strconv"
	"time"
)

// fleetConfig is loaded once at startup. Everything here except the
// bootstrap fields (TokensFile/StateDir/ControlURL/InitialTarget) is also
// reachable live via POST /fleet/config (see controlapi.go) — the startup
// value is just the initial value of the same shared runtimeConfig.
type fleetConfig struct {
	// InitialTarget is how many devices to bring up immediately at startup
	// (0 by default — see main.go's doc comment on why the fleet is inert
	// until deliberately scaled up, unlike every other compose service).
	InitialTarget int
	// TokensFile holds one single-use registration token per line, bulk-
	// issued by `control token create -count N` (cmd/control/token.go) —
	// this is what makes claiming hundreds of devices practical instead of
	// running the CLI hundreds of times.
	TokensFile string
	// StateDir caches each claimed device's credentials as
	// {idx}.json (internal/provisioning's format), so a fleet container
	// restart doesn't burn through the token pool re-claiming devices it
	// already claimed — same idea as hostagent's single state file, one per
	// virtual device instead of one for the whole process.
	StateDir string
	// ControlURL defaults to the in-network hostname (see
	// deploy/docker-compose.yml's CONTROL_API_URL for the same pattern from
	// the console's BFF) since the fleet normally runs as a compose
	// service, not natively like hostagent.
	ControlURL string

	SampleInterval time.Duration

	// RampWindow is how long a single Scale() call spreads its new
	// connection attempts over, so ramping 0->1000 doesn't open 1000 TCP
	// handshakes in the same instant. Per-step pacing (10, 50, 200, 1000...)
	// is the chaos script's job (test/chaos/ramp.sh); this is just the
	// jitter within one step.
	RampWindow time.Duration

	// Misbehavior defaults — all live-tunable afterward via POST
	// /fleet/config. Zero values mean "well-behaved," so a fleet started
	// with no overrides produces clean signal until a chaos script asks for
	// otherwise.
	MalformedRate     float64
	LatencyJitterMS   int
	ClockSkewJitterMS int
	DisconnectRate    float64
	StepRate          float64
	AnomalyRate       float64
}

func loadFleetConfig() fleetConfig {
	return fleetConfig{
		InitialTarget:     getEnvInt("FLEET_TARGET_DEVICES", 0),
		TokensFile:        getEnvStr("FLEET_TOKENS_FILE", ""),
		StateDir:          getEnvStr("FLEET_STATE_DIR", "/data/fleet-devices"),
		ControlURL:        getEnvStr("CONTROL_URL", "https://control:8080"),
		SampleInterval:    getEnvDuration("FLEET_SAMPLE_INTERVAL", 2*time.Second),
		RampWindow:        getEnvDuration("FLEET_RAMP_WINDOW", 3*time.Second),
		MalformedRate:     getEnvFloat("FLEET_MALFORMED_RATE", 0),
		LatencyJitterMS:   getEnvInt("FLEET_LATENCY_JITTER_MS", 0),
		ClockSkewJitterMS: getEnvInt("FLEET_CLOCK_SKEW_JITTER_MS", 0),
		DisconnectRate:    getEnvFloat("FLEET_DISCONNECT_RATE", 0),
		StepRate:          getEnvFloat("FLEET_STEP_RATE", 0.0005),
		AnomalyRate:       getEnvFloat("FLEET_ANOMALY_RATE", 0),
	}
}

func getEnvStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
