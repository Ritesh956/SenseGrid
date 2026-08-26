#!/usr/bin/env bash
# Chaos test: hard-kill cmd/processor mid-stream while the fleet keeps
# publishing, then bring it back and confirm the durable JetStream consumer
# (internal/telemetry's TELEMETRY stream, cmd/processor's persistence
# consumer) redelivers everything that piled up while it was down — zero
# loss, verified the same way kill_broker.sh does (lib.sh's
# verify_no_seq_gaps). Unlike a broker restart, devices never notice this
# outage: they keep publishing straight through it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

FLEET_SIZE="${FLEET_SIZE:-100}"
OUTAGE_S="${OUTAGE_S:-20}"
DRAIN_S="${DRAIN_S:-15}"
OUT="$RESULTS_DIR/kill_processor_$(date +%Y%m%dT%H%M%S).csv"
GAP_OUT="$RESULTS_DIR/kill_processor_seq_gaps_$(date +%Y%m%dT%H%M%S).csv"

csv_init "$OUT" "failure_mode,fleet_size,outage_started,outage_duration_s,catchup_time_s,total_seq_gap"

log "kill_processor: scaling fleet to $FLEET_SIZE, zeroing misbehavior for a clean measurement"
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null
fleet_scale "$FLEET_SIZE" > /dev/null
wait_for_connected 0.95 90 > /dev/null
log "stabilized. Publishing 20s baseline before the kill."
sleep 20

log "SIGKILLing processor"
outage_started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
compose kill processor

log "letting the fleet publish into JetStream unconsumed for ${OUTAGE_S}s"
sleep "$OUTAGE_S"

log "bringing processor back up"
t0=$SECONDS
compose up -d processor

# processor has no healthcheck defined (only mosquitto/nats/timescaledb/
# redis do — see deploy/docker-compose.yml), so "running" plus the
# consumer_lag drain below is the real readiness signal, not a health
# status that doesn't exist.
log "waiting for processor container to be running"
until [ "$(compose ps processor --format '{{.State}}')" = "running" ]; do
	sleep 1
done

# consumer_lag (cmd/processor/metrics.go, Phase 6) is messages pending on
# the durable consumer — polling it until it drains is a direct read of
# "has the backlog been redelivered and persisted yet," not a proxy.
log "waiting for sensegrid_processor_consumer_lag to drain"
deadline=$((SECONDS + 180))
lag="999999"
while [ "$SECONDS" -lt "$deadline" ]; do
	lag="$(prom_scalar 'sensegrid_processor_consumer_lag')"
	if [ "$lag" = "NaN" ]; then
		sleep 2
		continue
	fi
	# integer-truncate for the comparison; consumer_lag is a message count
	lag_int="${lag%.*}"
	if [ "${lag_int:-999999}" -le 2 ]; then
		break
	fi
	sleep 2
done
catchup_time=$((SECONDS - t0))
log "consumer_lag drained to ~$lag after ${catchup_time}s"

log "draining ${DRAIN_S}s more before checking for gaps"
sleep "$DRAIN_S"
total_gap="$(verify_no_seq_gaps "$GAP_OUT" 25)"

csv_row "$OUT" "processor_kill" "$FLEET_SIZE" "$outage_started" "$OUTAGE_S" "$catchup_time" "$total_gap"
log "kill_processor done -> $OUT (per-device detail: $GAP_OUT)"
