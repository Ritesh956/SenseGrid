#!/usr/bin/env bash
# Chaos test: simulate a network partition for a cohort of devices
# (cmd/fleet's POST /fleet/partition — see cmd/fleet/device.go's sampleLoop
# for what "partition" actually does: disconnect the MQTT client, no
# auto-reconnect until the timer fires), push a shadow config change to
# exactly that cohort while it's unreachable, heal, and measure how long it
# takes every partitioned device to reconnect, pick up the retained config
# publish (internal/shadow.Publisher retained-publishes to the config
# topic — see CLAUDE.md's auth model section), and clear GET
# /v1/devices/drift. This is the "partition heals, verify convergence"
# scenario the blueprint calls out explicitly.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

FLEET_SIZE="${FLEET_SIZE:-100}"
COHORT_SIZE="${COHORT_SIZE:-20}"
PARTITION_S="${PARTITION_S:-30}"
OUT="$RESULTS_DIR/partition_$(date +%Y%m%dT%H%M%S).csv"

csv_init "$OUT" "failure_mode,fleet_size,cohort_size,partitioned,partition_duration_s,config_pushed,convergence_time_s,converged"

log "partition: scaling fleet to $FLEET_SIZE, zeroing misbehavior for a clean measurement"
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null
fleet_scale "$FLEET_SIZE" > /dev/null
wait_for_connected 0.95 90 > /dev/null
log "stabilized"

log "minting an admin JWT for the shadow config push"
jwt="$(control_jwt admin 15m)"
if [ -z "$jwt" ]; then
	log "ERROR: failed to mint a JWT — is the control service up?"
	exit 1
fi

log "partitioning $COHORT_SIZE devices for ${PARTITION_S}s"
partition_resp="$(fleet_partition "$COHORT_SIZE" $((PARTITION_S * 1000)))"
# tr -d '\r': see lib.sh's verify_no_seq_gaps comment — multi-line jq -r
# output picks up an embedded CR per line on this platform, which mapfile
# preserves just like `read -r` does. Left in, it breaks both the shadow
# API URL built from $id below and the exact-match drift check further down.
mapfile -t device_ids < <(echo "$partition_resp" | jq -r '.partitioned_device_ids[]' | tr -d '\r')
partitioned="${#device_ids[@]}"
log "$partitioned device(s) actually partitioned: ${device_ids[*]:0:5}$([ "$partitioned" -gt 5 ] && echo ' ...')"

log "pushing a config change to the partitioned cohort while it's unreachable"
config_pushed=0
for id in "${device_ids[@]}"; do
	# -o /dev/null + -w '%{http_code}' hit an intermittent curl.exe exit 23
	# ("write error") on this platform, which — combined with set -e —
	# took the whole script down over one flaky request. Folding the body
	# and status code into a single captured stream (no separate -o
	# target) sidesteps that, and `|| echo $'\n000'` means a genuinely
	# failed request becomes a logged WARN for that one device instead of
	# aborting the cohort push.
	resp="$(curl -s $CURL_TLS_FLAGS -X PUT \
		"$CONTROL_URL/v1/devices/$id/shadow/desired" \
		-H "Authorization: Bearer $jwt" -H 'Content-Type: application/json' \
		-d '{"schema_version":"1.0","sample_rate_hz":10}' \
		-w $'\n%{http_code}' || echo $'\n000')"
	code="$(echo "$resp" | tail -n1)"
	if [ "$code" = "200" ]; then
		config_pushed=$((config_pushed + 1))
	else
		log "WARN: config push to $id returned HTTP $code"
	fi
done
log "$config_pushed/$partitioned config pushes accepted"

log "waiting out the partition (${PARTITION_S}s) — each device heals itself on its own timer"
sleep $((PARTITION_S + 5))

log "polling GET /v1/devices/drift until the cohort clears"
t0=$SECONDS
deadline=$((SECONDS + 120))
converged="false"
while [ "$SECONDS" -lt "$deadline" ]; do
	# `// []` guards against a null `.devices` even after the cmd/control
	# fix (devices_handlers.go) — belt and suspenders for an API response
	# this script doesn't control.
	drifted_ids="$(curl -sf $CURL_TLS_FLAGS "$CONTROL_URL/v1/devices/drift" -H "Authorization: Bearer $jwt" | jq -r '(.devices // [])[].id' | tr -d '\r')"
	still_drifted=0
	for id in "${device_ids[@]}"; do
		if echo "$drifted_ids" | grep -qx "$id"; then
			still_drifted=$((still_drifted + 1))
		fi
	done
	if [ "$still_drifted" -eq 0 ]; then
		converged="true"
		break
	fi
	sleep 2
done
convergence_time=$((SECONDS - t0))
log "convergence: $converged after ${convergence_time}s"

csv_row "$OUT" "partition" "$FLEET_SIZE" "$COHORT_SIZE" "$partitioned" "$PARTITION_S" "$config_pushed" "$convergence_time" "$converged"
log "partition done -> $OUT"
