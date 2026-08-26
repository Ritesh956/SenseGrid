package main

// runtimeConfig is the fleet-wide misbehavior/signal-shape dial every
// virtual device reads once per sample tick. It's shared via
// atomic.Pointer[runtimeConfig] (manager.go) rather than passed at
// construction so POST /fleet/config (controlapi.go) can change it live,
// across every running device, without a restart — the same
// swap-a-pointer approach cmd/hostagent uses for its own applied config.
type runtimeConfig struct {
	MalformedRate     float64
	LatencyJitterMS   int
	ClockSkewJitterMS int
	DisconnectRate    float64
	StepRate          float64
	AnomalyRate       float64
}

func runtimeConfigFromFleetConfig(cfg fleetConfig) runtimeConfig {
	return runtimeConfig{
		MalformedRate:     cfg.MalformedRate,
		LatencyJitterMS:   cfg.LatencyJitterMS,
		ClockSkewJitterMS: cfg.ClockSkewJitterMS,
		DisconnectRate:    cfg.DisconnectRate,
		StepRate:          cfg.StepRate,
		AnomalyRate:       cfg.AnomalyRate,
	}
}

// configPatch is the POST /fleet/config request body — every field is a
// pointer so a partial update ("just set malformed_rate") leaves the rest
// of the live runtimeConfig untouched, mirroring shadow.Desired's own
// partial-update convention (internal/shadow/types.go).
type configPatch struct {
	MalformedRate     *float64 `json:"malformed_rate,omitempty"`
	LatencyJitterMS   *int     `json:"latency_jitter_ms,omitempty"`
	ClockSkewJitterMS *int     `json:"clock_skew_jitter_ms,omitempty"`
	DisconnectRate    *float64 `json:"disconnect_rate,omitempty"`
	StepRate          *float64 `json:"step_rate,omitempty"`
	AnomalyRate       *float64 `json:"anomaly_rate,omitempty"`
}

func (p configPatch) apply(cur runtimeConfig) runtimeConfig {
	next := cur
	if p.MalformedRate != nil {
		next.MalformedRate = *p.MalformedRate
	}
	if p.LatencyJitterMS != nil {
		next.LatencyJitterMS = *p.LatencyJitterMS
	}
	if p.ClockSkewJitterMS != nil {
		next.ClockSkewJitterMS = *p.ClockSkewJitterMS
	}
	if p.DisconnectRate != nil {
		next.DisconnectRate = *p.DisconnectRate
	}
	if p.StepRate != nil {
		next.StepRate = *p.StepRate
	}
	if p.AnomalyRate != nil {
		next.AnomalyRate = *p.AnomalyRate
	}
	return next
}
