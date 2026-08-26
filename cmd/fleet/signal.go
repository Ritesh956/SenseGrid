package main

import (
	"math"
	"math/rand"
)

// sensorSignal generates one synthetic sensor's time series: a sine wave
// (base + amplitude*sin, standing in for a diurnal/periodic cycle — periods
// here are minutes, not hours, so the cycle is actually visible over the
// lifetime of a demo or chaos run instead of taking a real day), a slow
// linear drift, gaussian noise, occasional persistent step changes, and
// rate-gated anomaly spikes for exercising internal/anomaly's detectors.
// One instance per (device, sensor) — internal/window and internal/rules
// key their state the same way, so this mirrors how the rest of the
// pipeline already thinks about a "series."
type sensorSignal struct {
	rnd *rand.Rand

	base         float64
	amplitude    float64
	periodMS     float64
	phase        float64
	driftPerHour float64
	noiseStdDev  float64

	startMS    int64
	stepOffset float64
}

// newSensorSignal seeds its own rand.Rand from seed (derived from the
// device index and sensor name — see device.go) so every device/sensor
// pair gets a distinct, reproducible phase and noise sequence instead of
// N devices publishing an identical waveform.
func newSensorSignal(seed int64, base, amplitude, periodMS, driftPerHour, noiseStdDev float64) *sensorSignal {
	rnd := rand.New(rand.NewSource(seed))
	return &sensorSignal{
		rnd:          rnd,
		base:         base,
		amplitude:    amplitude,
		periodMS:     periodMS,
		phase:        rnd.Float64() * 2 * math.Pi,
		driftPerHour: driftPerHour,
		noiseStdDev:  noiseStdDev,
	}
}

// sample returns the next value for nowMS. stepRate/anomalyRate are
// per-sample probabilities (0..1), read from the fleet's live runtimeConfig
// each tick rather than baked in at construction, so a chaos script can
// dial anomaly injection up or down without restarting devices.
func (s *sensorSignal) sample(nowMS int64, stepRate, anomalyRate float64) (value float64, isAnomaly bool) {
	if s.startMS == 0 {
		s.startMS = nowMS
	}
	elapsedMS := float64(nowMS - s.startMS)
	elapsedHours := elapsedMS / 3_600_000.0
	angle := 2*math.Pi*elapsedMS/s.periodMS + s.phase

	if stepRate > 0 && s.rnd.Float64() < stepRate {
		s.stepOffset += (s.rnd.Float64()*2 - 1) * s.amplitude * 0.5
	}

	v := s.base + s.amplitude*math.Sin(angle) + s.driftPerHour*elapsedHours + s.rnd.NormFloat64()*s.noiseStdDev + s.stepOffset

	if anomalyRate > 0 && s.rnd.Float64() < anomalyRate {
		spike := s.amplitude * (3 + s.rnd.Float64()*4)
		if s.rnd.Float64() < 0.5 {
			spike = -spike
		}
		v += spike
		isAnomaly = true
	}
	return v, isAnomaly
}
