// Real host telemetry: CPU, memory, battery, CPU temperature, and Wi-Fi
// signal. Each reader is independently best-effort — a metric that isn't
// available on this OS/hardware is skipped (logged once, not on every
// tick) rather than failing the whole sample.
package main

import (
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"github.com/distatus/battery"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
)

// reading is a single collected value or vector, sensor-type-tagged, ready
// to be handed to telemetry.Reading by the caller.
type reading struct {
	sensorType string
	value      *float64
	values     map[string]float64
}

func f(v float64) *float64 { return &v }

func readCPUPercent(ctx context.Context) (*reading, error) {
	percents, err := cpu.PercentWithContext(ctx, time.Second, false)
	if err != nil || len(percents) == 0 {
		return nil, err
	}
	return &reading{sensorType: "cpu", value: f(percents[0])}, nil
}

func readCPUTemperature(ctx context.Context) (*reading, error) {
	temps, err := host.SensorsTemperaturesWithContext(ctx)
	if err != nil || len(temps) == 0 {
		return nil, err
	}
	// Multiple sensors are common; report the highest reading as a single
	// scalar rather than modeling every sensor as its own series.
	max := temps[0].Temperature
	for _, t := range temps[1:] {
		if t.Temperature > max {
			max = t.Temperature
		}
	}
	if max <= 0 {
		return nil, nil
	}
	return &reading{sensorType: "cpu_temp", value: f(max)}, nil
}

func readMemory(ctx context.Context) (*reading, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return &reading{sensorType: "mem", value: f(vm.UsedPercent)}, nil
}

func readBattery() (*reading, error) {
	batteries, err := battery.GetAll()
	if err != nil || len(batteries) == 0 {
		return nil, err
	}
	b := batteries[0]
	if b.Full <= 0 {
		return nil, nil
	}
	charging := 0.0
	if b.State.String() == "Charging" || b.State.String() == "Full" {
		charging = 1.0
	}
	return &reading{
		sensorType: "battery",
		values: map[string]float64{
			"level":    b.Current / b.Full,
			"charging": charging,
		},
	}, nil
}

var netshSignalRE = regexp.MustCompile(`Signal\s*:\s*(\d+)%`)

// readWiFiRSSI is Windows-only (shells out to `netsh wlan show interfaces`,
// which reports signal quality as a percentage, not raw dBm — that's what
// Windows exposes without extra tooling). Returns nil, nil on any other
// OS or when no Wi-Fi adapter is present, matching every other reader's
// "unavailable metrics are skipped, not fatal" contract.
func readWiFiRSSI(ctx context.Context) (*reading, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return nil, nil // no adapter, Wi-Fi off, or netsh unavailable — not an error
	}
	m := netshSignalRE.FindSubmatch(out)
	if m == nil {
		return nil, nil
	}
	pct, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return nil, nil
	}
	return &reading{sensorType: "wifi_signal", value: f(pct)}, nil
}

// collect gathers one sample from every reader, logging (once per reader,
// via warnOnce) any that returned an error rather than "just unavailable".
func collect(ctx context.Context, logger *slog.Logger, warnOnce map[string]bool) []reading {
	readers := []struct {
		name string
		fn   func() (*reading, error)
	}{
		{"cpu", func() (*reading, error) { return readCPUPercent(ctx) }},
		{"cpu_temp", func() (*reading, error) { return readCPUTemperature(ctx) }},
		{"mem", func() (*reading, error) { return readMemory(ctx) }},
		{"battery", readBattery},
		{"wifi_signal", func() (*reading, error) { return readWiFiRSSI(ctx) }},
	}

	var out []reading
	for _, r := range readers {
		res, err := r.fn()
		if err != nil {
			if !warnOnce[r.name] {
				logger.Warn("metric unavailable, will keep retrying silently", "metric", r.name, "err", err)
				warnOnce[r.name] = true
			}
			continue
		}
		if res == nil {
			continue
		}
		out = append(out, *res)
	}
	return out
}
