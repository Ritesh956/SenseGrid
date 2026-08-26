#!/usr/bin/env bash
# Load test: ramp the synthetic fleet through increasing device counts,
# recording latency/error-rate at each step — the data behind the
# blueprint's required "saturation point on the latency-vs-fleet-size
# curve" (P7's definition of done). Assumes the stack is already up
# (docker compose ... up -d) and a fleet token pool has been issued — see
# test/chaos/README.md for the one-time setup.
#
# Usage: RAMP_STEPS="10 25 50 100 200 400 600 800 1000" ./ramp.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

STEPS=(${RAMP_STEPS:-10 25 50 100 200 400 600 800 1000})
STABILIZE_S="${RAMP_STABILIZE_S:-20}"
HOLD_S="${RAMP_HOLD_S:-30}"
OUT="$RESULTS_DIR/ramp_$(date +%Y%m%dT%H%M%S).csv"

csv_init "$OUT" "fleet_size,timestamp,target,connected,published_total,publish_errors_total,malformed_sent_total,ingest_p99_lag_s,e2e_p50_s,e2e_p95_s,e2e_p99_s"

log "ramp test starting, steps: ${STEPS[*]}, results -> $OUT"

# Zero out misbehavior for a clean saturation curve — this test measures
# capacity, not resilience (kill_broker.sh/kill_processor.sh/pause_db.sh
# cover that).
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null

for step in "${STEPS[@]}"; do
	log "scaling to $step devices"
	fleet_scale "$step" > /dev/null

	status="$(wait_for_connected 0.95 90)"
	connected="$(echo "$status" | jq -r '.connected')"
	log "step $step: $connected/$step connected, holding ${HOLD_S}s for the curve to settle"
	sleep "$STABILIZE_S"

	# Hold at this size so rate()'s window has enough steady-state samples
	# before we read it, then read.
	sleep "$HOLD_S"
	status="$(fleet_status)"

	ingest_p99_lag="$(prom_quantile sensegrid_ingest_lag_seconds 0.99)"
	e2e_p50="$(prom_quantile sensegrid_processor_end_to_end_latency_seconds 0.50)"
	e2e_p95="$(prom_quantile sensegrid_processor_end_to_end_latency_seconds 0.95)"
	e2e_p99="$(prom_quantile sensegrid_processor_end_to_end_latency_seconds 0.99)"

	csv_row "$OUT" \
		"$step" \
		"$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		"$(echo "$status" | jq -r '.target')" \
		"$(echo "$status" | jq -r '.connected')" \
		"$(echo "$status" | jq -r '.published_total')" \
		"$(echo "$status" | jq -r '.publish_errors_total')" \
		"$(echo "$status" | jq -r '.malformed_sent_total')" \
		"$ingest_p99_lag" \
		"$e2e_p50" "$e2e_p95" "$e2e_p99"

	log "step $step: e2e p50=${e2e_p50}s p95=${e2e_p95}s p99=${e2e_p99}s ingest_p99_lag=${ingest_p99_lag}s"
done

log "ramp test done -> $OUT"
log "scale back down: RAMP_STEPS=0 or call fleet_scale 0 directly to release connections"
