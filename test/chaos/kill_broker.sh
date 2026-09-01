#!/usr/bin/env bash
# Chaos test: restart the MQTT broker mid-stream. Every connected device
# loses its connection at once; cmd/fleet's per-device reconnect-with-backoff
# (cmd/fleet/device.go's sampleLoop) is what's supposed to bring them all
# back without operator intervention. Measures recovery time and confirms
# zero data loss via seq-gap detection (lib.sh's verify_no_seq_gaps).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

FLEET_SIZE="${FLEET_SIZE:-100}"
DRAIN_S="${DRAIN_S:-15}"
# GAP_SINCE: see lib.sh's verify_no_seq_gaps doc comment. Defaults to "now"
# so a standalone run of this script is still correct; a caller running
# several chaos scripts back-to-back against the same fleet container
# should export one shared GAP_SINCE (captured before the first script's
# fleet_scale) so later scripts' checks aren't polluted by earlier
# scripts' own valid rows falling outside a fresh per-script window.
GAP_SINCE="${GAP_SINCE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
OUT="$RESULTS_DIR/kill_broker_$(date +%Y%m%dT%H%M%S).csv"
GAP_OUT="$RESULTS_DIR/kill_broker_seq_gaps_$(date +%Y%m%dT%H%M%S).csv"

csv_init "$OUT" "failure_mode,fleet_size,outage_started,recovery_time_s,connected_before,connected_after_recovery,total_seq_gap"

log "kill_broker: scaling fleet to $FLEET_SIZE, zeroing misbehavior for a clean measurement"
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null
fleet_scale "$FLEET_SIZE" > /dev/null
status="$(wait_for_connected 0.95 90)"
connected_before="$(echo "$status" | jq -r '.connected')"
log "stabilized: $connected_before/$FLEET_SIZE connected. Publishing for 20s to build a baseline before the kill."
sleep 20

log "restarting mosquitto"
outage_started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
t0=$SECONDS
compose restart mosquitto

# Wait for the broker's own healthcheck before expecting devices to
# reconnect — matches how depends_on: condition: service_healthy gates
# every other service against mosquitto.
log "waiting for mosquitto healthcheck"
until [ "$(compose ps mosquitto --format '{{.Health}}')" = "healthy" ]; do
	sleep 1
done
log "broker healthy again after $((SECONDS - t0))s, watching fleet reconnect"

status="$(wait_for_connected 0.95 120)"
recovery_time=$((SECONDS - t0))
connected_after="$(echo "$status" | jq -r '.connected')"
log "recovered: $connected_after/$FLEET_SIZE connected, ${recovery_time}s after restart"

log "draining ${DRAIN_S}s for cmd/processor's batched persistence to catch up before checking for gaps"
sleep "$DRAIN_S"
total_gap="$(verify_no_seq_gaps "$GAP_OUT" 25 "$GAP_SINCE")"

csv_row "$OUT" "broker_restart" "$FLEET_SIZE" "$outage_started" "$recovery_time" "$connected_before" "$connected_after" "$total_gap"
log "kill_broker done -> $OUT (per-device detail: $GAP_OUT)"
