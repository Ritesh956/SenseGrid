#!/usr/bin/env bash
# Chaos test: pause (SIGSTOP, not a restart) TimescaleDB while the fleet
# and cmd/processor keep running. cmd/processor's batch writes should start
# failing/blocking and it should back off and nack rather than crash-loop
# or ack messages it never persisted; JetStream redelivers once the DB
# comes back. Confirms both zero loss (verify_no_seq_gaps) and — the part
# specific to this failure mode — no duplicate rows from the redelivery
# (check_no_duplicate_rows), i.e. the persistence consumer's
# ON CONFLICT DO NOTHING idempotency guard actually held.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

FLEET_SIZE="${FLEET_SIZE:-100}"
PAUSE_S="${PAUSE_S:-20}"
DRAIN_S="${DRAIN_S:-15}"
OUT="$RESULTS_DIR/pause_db_$(date +%Y%m%dT%H%M%S).csv"
GAP_OUT="$RESULTS_DIR/pause_db_seq_gaps_$(date +%Y%m%dT%H%M%S).csv"

csv_init "$OUT" "failure_mode,fleet_size,pause_started,pause_duration_s,catchup_time_s,total_seq_gap,duplicate_rows"

log "pause_db: scaling fleet to $FLEET_SIZE, zeroing misbehavior for a clean measurement"
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null
fleet_scale "$FLEET_SIZE" > /dev/null
wait_for_connected 0.95 90 > /dev/null
log "stabilized. Publishing 20s baseline before the pause."
sleep 20

dupes_before="$(check_no_duplicate_rows)"

log "pausing timescaledb"
pause_started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
compose pause timescaledb

log "letting cmd/processor hit write failures for ${PAUSE_S}s"
sleep "$PAUSE_S"

log "unpausing timescaledb"
t0=$SECONDS
compose unpause timescaledb

log "waiting for sensegrid_processor_consumer_lag to drain"
deadline=$((SECONDS + 180))
lag="999999"
while [ "$SECONDS" -lt "$deadline" ]; do
	lag="$(prom_scalar 'sensegrid_processor_consumer_lag')"
	if [ "$lag" = "NaN" ]; then
		sleep 2
		continue
	fi
	lag_int="${lag%.*}"
	if [ "${lag_int:-999999}" -le 2 ]; then
		break
	fi
	sleep 2
done
catchup_time=$((SECONDS - t0))
log "consumer_lag drained to ~$lag after ${catchup_time}s"

log "draining ${DRAIN_S}s more before checking for gaps/duplicates"
sleep "$DRAIN_S"
total_gap="$(verify_no_seq_gaps "$GAP_OUT" 25)"
dupes_after="$(check_no_duplicate_rows)"
new_dupes=$((dupes_after - dupes_before))

csv_row "$OUT" "db_pause" "$FLEET_SIZE" "$pause_started" "$PAUSE_S" "$catchup_time" "$total_gap" "$new_dupes"
log "pause_db done -> $OUT (per-device detail: $GAP_OUT, new_duplicate_rows=$new_dupes)"
