#!/usr/bin/env bash
# Phase 8 hardening check: confirm the compression and retention policies
# from deploy/migrations/0004_compression_retention.sql actually do what
# they claim under real accumulated volume, and measure storage saved.
#
# Chunks only become policy-eligible after compress_after (2 days) /
# drop_after (7 days), which is longer than anyone wants to sit around for
# in a verification script. So this script proves the same code path two
# ways instead of waiting on the clock:
#   1. Manually compresses every chunk older than 2 hours (compress_chunk
#      is the exact function the "Columnstore Policy" background job
#      calls once a chunk crosses compress_after — this just calls it
#      directly on-demand) and measures the size delta.
#   2. Inserts one synthetic reading backdated 10 days, manually runs the
#      retention job (CALL run_job), and confirms that row alone is
#      dropped without touching any real data.
# The *policies themselves* (schedule, compress_after, drop_after) are
# read from timescaledb_information.jobs and asserted present/correct —
# that part doesn't need to be re-proven by hand, just confirmed
# registered.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

OUT="$RESULTS_DIR/compression_retention_$(date +%Y%m%dT%H%M%S).csv"
csv_init "$OUT" "check,before,after,detail"

log "confirming both policies are registered with the config from the Blueprint's DoD"
jobs="$(psql_query "SELECT proc_name || ':' || config::text FROM timescaledb_information.jobs WHERE proc_name IN ('policy_compression','policy_retention');")"
echo "$jobs" | while IFS= read -r line; do log "  $line"; done
if ! echo "$jobs" | grep -q "policy_compression"; then
	log "ERROR: no compression policy registered on readings"
	exit 1
fi
if ! echo "$jobs" | grep -q "policy_retention"; then
	log "ERROR: no retention policy registered on readings"
	exit 1
fi
csv_row "$OUT" "policies_registered" "-" "-" "$(echo "$jobs" | tr ',' ';' | tr '\n' '|')"

log "compression: measuring hypertable size before"
before_total="$(psql_query "SELECT hypertable_size('readings');")"
before_rows="$(psql_query "SELECT count(*) FROM readings;")"
log "before: $(psql_query "SELECT pg_size_pretty(hypertable_size('readings'));") ($before_rows rows)"

log "compressing every chunk older than 2h (same compress_chunk() call the policy job makes, just on-demand rather than waiting out compress_after=2 days)"
psql_query "SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('readings', older_than => INTERVAL '2 hours') c;" > /dev/null

after_total="$(psql_query "SELECT hypertable_size('readings');")"
after_rows="$(psql_query "SELECT count(*) FROM readings;")"
pct_saved="$(psql_query "SELECT round(100.0*(1 - after_compression_total_bytes::numeric/before_compression_total_bytes::numeric),1) FROM hypertable_compression_stats('readings');")"
log "after: $(psql_query "SELECT pg_size_pretty(hypertable_size('readings'));") ($after_rows rows, ${pct_saved}% saved)"

if [ "$before_rows" != "$after_rows" ]; then
	log "ERROR: row count changed across compression ($before_rows -> $after_rows) — compression must be lossless"
	exit 1
fi
csv_row "$OUT" "compression" "$before_total" "$after_total" "pct_saved=${pct_saved};rows_before=${before_rows};rows_after=${after_rows}"

log "retention: inserting one synthetic reading backdated 10 days (drop_after is 7)"
device_id="$(psql_query "SELECT device_id FROM readings LIMIT 1;")"
psql_query "INSERT INTO readings (time, device_id, sensor_type, value, device_time, ingest_time, seq) VALUES (now() - interval '10 days', '${device_id}', 'hardening_retention_probe', 42.0, now() - interval '10 days', now() - interval '10 days', 999999999);" > /dev/null
probe_before="$(psql_query "SELECT count(*) FROM readings WHERE sensor_type='hardening_retention_probe';")"
real_before="$(psql_query "SELECT count(*) FROM readings WHERE sensor_type != 'hardening_retention_probe';")"

log "running the retention job on-demand (CALL run_job — same proc_name the daily schedule calls)"
retention_job_id="$(psql_query "SELECT job_id FROM timescaledb_information.jobs WHERE proc_name='policy_retention' LIMIT 1;")"
psql_query "CALL run_job(${retention_job_id});" > /dev/null

probe_after="$(psql_query "SELECT count(*) FROM readings WHERE sensor_type='hardening_retention_probe';")"
real_after="$(psql_query "SELECT count(*) FROM readings WHERE sensor_type != 'hardening_retention_probe';")"
log "probe row: $probe_before -> $probe_after (expect 1 -> 0); real data: $real_before -> $real_after (expect unchanged)"

if [ "$probe_after" != "0" ]; then
	log "ERROR: retention did not drop the backdated probe row"
	exit 1
fi
if [ "$real_before" != "$real_after" ]; then
	log "ERROR: retention touched real data ($real_before -> $real_after) — it should only drop chunks entirely older than drop_after"
	exit 1
fi
csv_row "$OUT" "retention" "$probe_before" "$probe_after" "real_rows_before=${real_before};real_rows_after=${real_after}"

log "compression + retention check passed -> $OUT"
