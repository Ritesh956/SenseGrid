#!/usr/bin/env bash
# Shared helpers for test/chaos's scripts: docker compose wrapper, the
# fleet control API, Prometheus queries, and CSV writing. Every chaos
# script sources this instead of duplicating it — see
# test/chaos/README.md for how the scripts fit together.
#
# Requires: bash, curl, jq, docker compose. Run from anywhere; ROOT_DIR is
# derived from this file's location, not the caller's cwd.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RESULTS_DIR="$ROOT_DIR/test/chaos/results"
mkdir -p "$RESULTS_DIR"

# CLAUDE.md's "Windows/Git Bash gotchas" covers MSYS_NO_PATHCONV: without
# it, `docker compose exec ... /app ...` gets its /app rewritten into a
# Windows path. But MSYS_NO_PATHCONV is all-or-nothing for a whole command
# line — it also stops -f/--env-file's own host paths from being
# auto-converted, which corrupts them into "C:\c\Users\..." (found live,
# not theoretical: docker.exe's own path handling mangles an
# unconverted /c/Users/... arg rather than erroring cleanly on it). Fixed
# by converting COMPOSE_FILE/ENV_FILE to native Windows paths ourselves via
# cygpath, so they're already in the form MSYS_NO_PATHCONV=1 leaves alone —
# then it's safe to export globally for every docker compose call this
# script makes, container-internal paths included.
if command -v cygpath > /dev/null 2>&1; then
	COMPOSE_FILE="$(cygpath -w "$ROOT_DIR/deploy/docker-compose.yml")"
	ENV_FILE="$(cygpath -w "$ROOT_DIR/.env")"
	export MSYS_NO_PATHCONV=1
else
	COMPOSE_FILE="$ROOT_DIR/deploy/docker-compose.yml"
	ENV_FILE="$ROOT_DIR/.env"
fi

FLEET_URL="${FLEET_URL:-http://localhost:8083}"
PROMETHEUS_URL="${PROMETHEUS_URL:-http://localhost:9190}"
CONTROL_URL="${CONTROL_URL:-https://localhost:8090}"

POSTGRES_USER="${POSTGRES_USER:-sensegrid}"
POSTGRES_DB="${POSTGRES_DB:-sensegrid}"

# CLAUDE.md's "Windows/Git Bash gotchas": curl.exe on Windows uses the
# Schannel backend, which does OCSP/revocation checking that fails against
# our throwaway dev CA. -k also skips CA verification entirely, since these
# scripts hit localhost with a self-signed cert and aren't worth
# distributing deploy/certs/ca.pem to. --ssl-no-revoke is Schannel-only, so
# it's only added when curl actually supports it (real curl on Linux/macOS
# errors on an unrecognized flag).
CURL_TLS_FLAGS="-k"
if curl --help all 2>/dev/null | grep -q -- '--ssl-no-revoke'; then
	CURL_TLS_FLAGS="-k --ssl-no-revoke"
fi

# control_jwt mints a bearer token via the CLI-only issuance path (see
# cmd/control/jwt_cli.go's doc comment on why there's no login endpoint for
# this) and prints just the token — `control jwt create` itself prints a
# human-readable line above it.
control_jwt() {
	local role="${1:-admin}" ttl="${2:-1h}"
	compose exec -T control /app jwt create -role "$role" -ttl "$ttl" | tail -n1 | tr -d '\r'
}

compose() {
	# < /dev/null: found live — `docker compose exec` forwards the calling
	# shell's stdin to the container by default, even with -T and even
	# though `psql -c "..."` never reads stdin. Called from inside
	# verify_no_seq_gaps's `while read ... done <<< "$device_ids"` loop,
	# that forwarding silently drains the *rest* of the here-string (docker
	# compose's own client-side proxying reads whatever's available on fd
	# 0), so only the first device ever got processed — no error, no
	# nonzero exit, the loop just quietly ran once. None of this file's
	# calls need real stdin, so cutting it off here fixes the whole class
	# of call site at once rather than patching each one.
	docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@" < /dev/null
}

# psql runs a single query inside the timescaledb container (no TLS/host
# networking concerns — see CLAUDE.md's curl+Schannel gotcha, which this
# sidesteps entirely by not going over the host network) and prints
# tuples-only, unaligned output: one value (or tab-separated row) per line.
psql_query() {
	compose exec -T timescaledb psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tA -c "$1"
}

# curl_retry retries a curl invocation up to 5 times with a short backoff.
# Found live: at 200+ devices, a single transient network hiccup (curl
# exit 56, "recv error" — the fleet and Prometheus were both healthy a
# moment later) took down an otherwise-clean 20+ minute ramp run under
# set -e. Same stdout/exit-status contract as a bare curl call, just more
# patient about one bad moment; a call that's STILL failing after 5 tries
# is a real problem and is meant to propagate.
curl_retry() {
	local tries=5 delay=2 i
	for ((i = 1; i <= tries; i++)); do
		if curl "$@"; then
			return 0
		fi
		if [ "$i" -lt "$tries" ]; then
			sleep "$delay"
		fi
	done
	return 1
}

fleet_status() {
	curl_retry -sf "$FLEET_URL/fleet/status"
}

fleet_scale() {
	local count="$1"
	curl_retry -sf -X POST "$FLEET_URL/fleet/scale" -H 'Content-Type: application/json' \
		-d "{\"count\":$count}"
}

fleet_partition() {
	local count="$1" duration_ms="$2"
	curl_retry -sf -X POST "$FLEET_URL/fleet/partition" -H 'Content-Type: application/json' \
		-d "{\"count\":$count,\"duration_ms\":$duration_ms}"
}

fleet_config() {
	local body="$1"
	curl_retry -sf -X POST "$FLEET_URL/fleet/config" -H 'Content-Type: application/json' -d "$body"
}

# wait_for_connected polls fleet/status until at least `fraction` of target
# devices report connected, or timeout_s elapses. Echoes the final status
# JSON either way — callers decide whether "didn't fully converge" is fatal.
wait_for_connected() {
	local fraction="${1:-0.95}" timeout_s="${2:-60}"
	local deadline=$((SECONDS + timeout_s))
	local status target connected
	while true; do
		status="$(fleet_status)"
		target="$(echo "$status" | jq -r '.target')"
		connected="$(echo "$status" | jq -r '.connected')"
		if [ "$target" -eq 0 ] || awk -v c="$connected" -v t="$target" -v f="$fraction" 'BEGIN{exit !(c >= t*f)}'; then
			echo "$status"
			return 0
		fi
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "$status"
			return 0
		fi
		sleep 1
	done
}

# prom_quantile queries histogram_quantile(q, rate(metric_bucket[range])) at
# the current instant and prints just the scalar value (or "NaN" if the
# series has no data yet, e.g. right after a fresh start).
prom_quantile() {
	local metric="$1" quantile="$2" range="${3:-1m}"
	local query="histogram_quantile(${quantile}, sum(rate(${metric}_bucket[${range}])) by (le))"
	curl_retry -sf --data-urlencode "query=$query" "$PROMETHEUS_URL/api/v1/query" \
		| jq -r '.data.result[0].value[1] // "NaN"'
}

prom_scalar() {
	local query="$1"
	curl_retry -sf --data-urlencode "query=$query" "$PROMETHEUS_URL/api/v1/query" \
		| jq -r '.data.result[0].value[1] // "NaN"'
}

csv_init() {
	local file="$1" header="$2"
	echo "$header" > "$file"
}

csv_row() {
	local file="$1"
	shift
	local IFS=,
	echo "$*" >> "$file"
}

log() {
	echo "[$(date '+%H:%M:%S')] $*" >&2
}

# verify_no_seq_gaps checks, for up to `sample` devices from the current
# fleet status, whether every seq the device published (1..last_seq) has a
# corresponding row in TimescaleDB — internal/telemetry.Reading.Seq exists
# specifically to make "zero data loss" checkable (see CLAUDE.md's data
# contract section), and readings_device_sensor_seq_time_idx's uniqueness
# means count(DISTINCT seq) == max(seq) is sufficient proof of no gaps for
# a counter that starts at 1. Callers should give the pipeline a few
# seconds to drain (async batched persistence — cmd/processor's consumer.go)
# before calling this. Writes one row per checked device to out_csv and
# echoes the total gap across all sampled devices (0 == zero data loss).
#
# `since` (optional, RFC3339) scopes the DB-side count to
# `time >= since` — required whenever devices/readings can carry state
# from a previous test session (reused fleet_data credentials, retained
# shadow config, undropped readings rows from an earlier run). Without it,
# `fleet_last_seq` (this run's live in-memory counter, reset at process
# start) gets compared against `distinct(seq)`/`max(seq)` computed over
# *every* row that device_id has ever had in `readings`, including old
# runs that also started their own seq counter at 1 — found live on a
# stack that had been through Phase 7/8 testing already: one reused
# device_id showed a "total_gap" of 43,736 driven entirely by historical
# rows from a prior, much longer run, while 90%+ of sampled devices showed
# large *negative* gaps (old distinct-seq count exceeding this run's small
# fresh counter) that the `gap > 0` sum silently discards instead of
# netting out. Same class of trap documented in the Phase 8 hardening
# report for rate_limit_load.sh's original seq-gap approach — that one
# switched to a wall-clock window instead of raw seq counters for exactly
# this reason. Callers running multiple chaos scripts back-to-back in one
# session (same `cmd/fleet` container, so seq keeps accumulating across
# scripts) should capture one shared `since` before the first script's
# `fleet_scale` call and reuse it for all of them — a fresh per-script
# `since` would incorrectly exclude valid earlier-script rows that this
# run's cumulative `fleet_last_seq` still counts.
verify_no_seq_gaps() {
	local out_csv="$1" sample="${2:-25}" since="${3:-}"
	csv_init "$out_csv" "device_id,fleet_last_seq,db_distinct_seq,db_max_seq,gap"

	local status device_ids total_gap=0 checked=0
	status="$(fleet_status)"
	# tr -d '\r': found live — multi-line jq -r output picks up an embedded
	# carriage return per line somewhere in the curl/docker/jq chain on
	# Windows, which `read -r` preserves (it only disables backslash
	# escaping, not CR stripping). A stray \r turns a perfectly valid UUID
	# into one Postgres rejects with "invalid input syntax for type uuid" —
	# invisible in a terminal, only obvious from a hex dump. Single-value
	# jq extractions elsewhere in this file don't need this: $(...) strips
	# a trailing \r\n at the very end of its output, just not one embedded
	# mid-stream between multiple lines.
	device_ids="$(echo "$status" | jq -r --argjson n "$sample" \
		'.devices | sort_by(-.seq) | .[:$n][] | select(.seq>0) | .device_id' | tr -d '\r')"

	local time_filter=""
	[ -n "$since" ] && time_filter=" AND time >= '${since}'"

	while IFS= read -r device_id; do
		[ -z "$device_id" ] && continue
		local last_seq row distinct_seq max_seq gap
		last_seq="$(echo "$status" | jq -r --arg id "$device_id" '.devices[] | select(.device_id==$id) | .seq')"
		row="$(psql_query "SELECT count(DISTINCT seq), coalesce(max(seq),0) FROM readings WHERE device_id='${device_id}'${time_filter};")"
		distinct_seq="$(echo "$row" | cut -d'|' -f1)"
		max_seq="$(echo "$row" | cut -d'|' -f2)"
		gap=$((last_seq - distinct_seq))
		csv_row "$out_csv" "$device_id" "$last_seq" "$distinct_seq" "$max_seq" "$gap"
		if [ "$gap" -gt 0 ]; then
			total_gap=$((total_gap + gap))
		fi
		checked=$((checked + 1))
	done <<< "$device_ids"

	log "seq-gap check: $checked device(s) sampled, total_gap=$total_gap (0 = zero data loss) -> $out_csv"
	echo "$total_gap"
}

# check_no_duplicate_rows compares total row count against distinct
# (device_id, sensor_type, seq, time) tuples for the whole readings table —
# a nonzero difference means the persistence consumer's ON CONFLICT DO
# NOTHING idempotency guard (deploy/migrations/0002_readings.sql) didn't
# hold, which is exactly what pause_db.sh's resume needs to rule out.
check_no_duplicate_rows() {
	local row total distinct
	row="$(psql_query "SELECT count(*), count(DISTINCT (device_id, sensor_type, seq, time)) FROM readings;")"
	total="$(echo "$row" | cut -d'|' -f1)"
	distinct="$(echo "$row" | cut -d'|' -f2)"
	log "duplicate-row check: total=$total distinct=$distinct"
	echo $((total - distinct))
}
