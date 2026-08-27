#!/usr/bin/env bash
# Phase 8 hardening check: load-test cmd/ingest's per-device rate limiter
# (cmd/ingest/ratelimit.go) — Blueprint DoD: "confirm one runaway synthetic
# device can't starve the others."
#
# Scales a small fleet of well-behaved devices, then pushes one targeted
# device's shadow config to an absurd sample rate (POST /v1/devices/{id}/
# shadow/desired, same mechanism test/chaos/partition.sh already uses to
# push targeted config), and measures whether the other devices keep
# landing real-time data while the rogue gets rate-limited.
#
# Measure by TIME WINDOW, not by seq gaps: fleet devices reuse cached
# credentials across scale up/down (see cmd/fleet's doc comment), and
# telemetry.Reading.Seq resets to 0 on every client restart — a device_id
# that's been through Phase 7's chaos testing can have rows in `readings`
# from several non-contiguous seq ranges (one per past incarnation), which
# makes "max(seq) - count(distinct seq)" meaningless as a gap check. Rows
# landed in the last N seconds is unambiguous regardless of reset history.
#
# This is the load test that found a real bug (Phase 8, not a hypothetical
# for this script): paho's default Order=true serializes every device's
# message callback through one goroutine, so a runaway device's flood
# starves everyone else even though the token bucket is per-device — fixed
# by cmd/ingest/main.go's SetOrderMatters(false). See that file's comment
# for the mechanism. With the fix in place, this script should show the
# non-rogue devices continuing to land data at a healthy rate throughout.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

FLEET_SIZE="${FLEET_SIZE:-20}"
ROGUE_SAMPLE_RATE_HZ="${ROGUE_SAMPLE_RATE_HZ:-2000}"
FLOOD_S="${FLOOD_S:-30}"
OUT="$RESULTS_DIR/rate_limit_load_$(date +%Y%m%dT%H%M%S).csv"
csv_init "$OUT" "phase,elapsed_s,normal_rows_per_window,rogue_rows_per_window,rate_limited_delta,ingest_lag_p99"

log "zeroing misbehavior and scaling fleet to $FLEET_SIZE"
fleet_config '{"malformed_rate":0,"latency_jitter_ms":0,"clock_skew_jitter_ms":0,"disconnect_rate":0,"anomaly_rate":0}' > /dev/null
fleet_scale "$FLEET_SIZE" > /dev/null
status="$(wait_for_connected 0.95 60)"

rogue_id="$(echo "$status" | jq -r '[.devices[] | select(.connected == true)][0].device_id' | tr -d '\r')"
mapfile -t normal_ids < <(echo "$status" | jq -r --arg r "$rogue_id" '.devices[] | select(.device_id != $r and .connected == true) | .device_id' | tr -d '\r')
log "rogue device: $rogue_id — $(( ${#normal_ids[@]} )) normal devices in the control group"
normal_id_list="$(printf "'%s'," "${normal_ids[@]}")"
normal_id_list="${normal_id_list%,}"

log "letting the fleet stabilize for 15s before baseline"
sleep 15
baseline_lag="$(prom_quantile sensegrid_ingest_lag_seconds 0.99)"
baseline_rl="$(prom_scalar sensegrid_ingest_rate_limited_total)"
log "baseline: ingest lag p99=${baseline_lag}s rate_limited_total=${baseline_rl}"
csv_row "$OUT" "baseline" "0" "-" "-" "0" "$baseline_lag"

jwt="$(control_jwt admin 15m)"
log "flooding $rogue_id at ${ROGUE_SAMPLE_RATE_HZ}Hz (≈$(( ROGUE_SAMPLE_RATE_HZ * 3 ))msg/s across 3 sensors) via shadow config"
curl_retry -sf $CURL_TLS_FLAGS -X PUT "$CONTROL_URL/v1/devices/$rogue_id/shadow/desired" \
	-H "Authorization: Bearer $jwt" -H 'Content-Type: application/json' \
	-d "{\"schema_version\":\"1.0\",\"sample_rate_hz\":${ROGUE_SAMPLE_RATE_HZ}}" > /dev/null

log "holding the flood for ${FLOOD_S}s"
sleep "$FLOOD_S"

flood_lag="$(prom_quantile sensegrid_ingest_lag_seconds 0.99)"
flood_rl="$(prom_scalar sensegrid_ingest_rate_limited_total)"
rl_delta=$(awk -v a="$flood_rl" -v b="$baseline_rl" 'BEGIN{printf "%.0f", a-b}')

window_s=60
normal_rows="$(psql_query "SELECT count(*) FROM readings WHERE device_id IN (${normal_id_list}) AND time > now() - interval '${window_s} seconds';")"
rogue_rows="$(psql_query "SELECT count(*) FROM readings WHERE device_id = '${rogue_id}' AND time > now() - interval '${window_s} seconds';")"
log "during flood: normal devices landed ${normal_rows} rows in the last ${window_s}s, rogue landed ${rogue_rows}, ingest lag p99=${flood_lag}s, rate_limited delta=${rl_delta}"
csv_row "$OUT" "during_flood" "$FLOOD_S" "$normal_rows" "$rogue_rows" "$rl_delta" "$flood_lag"

log "restoring rogue to a normal sample rate and scaling the fleet back down"
curl_retry -sf $CURL_TLS_FLAGS -X PUT "$CONTROL_URL/v1/devices/$rogue_id/shadow/desired" \
	-H "Authorization: Bearer $jwt" -H 'Content-Type: application/json' \
	-d '{"schema_version":"1.0","sample_rate_hz":0.5}' > /dev/null
fleet_scale 0 > /dev/null

expected_min_rows=$(( FLEET_SIZE * 3 * window_s / 10 )) # generous floor: >=10% of nominal 0.5Hz x 3 sensors rate
if [ "$normal_rows" -lt "$expected_min_rows" ]; then
	log "FAIL: normal devices landed only ${normal_rows} rows in ${window_s}s during the flood (expected at least ${expected_min_rows}) — the rogue device is starving the others"
	exit 1
fi
if [ "$rl_delta" -le 0 ]; then
	log "FAIL: rate_limited_total didn't increase during the flood — the limiter isn't shedding the rogue's excess"
	exit 1
fi

log "PASS: normal devices kept landing real-time data (${normal_rows} rows/${window_s}s) while the rogue was rate-limited (+${rl_delta}) -> $OUT"
