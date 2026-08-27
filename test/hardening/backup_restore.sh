#!/usr/bin/env bash
# Phase 8 hardening drill: full backup/restore of TimescaleDB, measured as
# an RTO. Blueprint DoD: "A restore drill with a measured RTO" — take a
# backup, destroy the volume, restore, verify continuous aggregates
# rebuild, time it.
#
# DESTRUCTIVE: this stops ingest/processor, takes a pg_dump, then deletes
# the timescaledb service's data volume entirely and recreates it from
# that dump. Point of the drill is proving restore actually works, not
# just that pg_dump runs — only point this at a stack you're prepared to
# lose (the dump file itself is the safety net; it's left in
# test/hardening/results/ afterward, not cleaned up).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
source ./lib.sh

DUMP_FILE="$RESULTS_DIR/sensegrid_backup_$(date +%Y%m%dT%H%M%S).dump"
OUT="$RESULTS_DIR/backup_restore_$(date +%Y%m%dT%H%M%S).csv"
csv_init "$OUT" "step,elapsed_s,readings,readings_1m,readings_1h,devices,alerts"

count_all() {
	local r r1m r1h d a
	r="$(psql_query "SELECT count(*) FROM readings;")"
	r1m="$(psql_query "SELECT count(*) FROM readings_1m;")"
	r1h="$(psql_query "SELECT count(*) FROM readings_1h;")"
	d="$(psql_query "SELECT count(*) FROM devices;")"
	a="$(psql_query "SELECT count(*) FROM alerts;")"
	echo "$r,$r1m,$r1h,$d,$a"
}

log "quiescing writers (ingest/processor) so backup and post-restore counts are directly comparable"
compose stop processor ingest > /dev/null
pre_counts="$(count_all)"
log "pre-backup counts (readings,readings_1m,readings_1h,devices,alerts): $pre_counts"
csv_row "$OUT" "pre_backup" "0" "$pre_counts"

log "taking a pg_dump (custom format) inside the timescaledb container"
t0=$SECONDS
compose exec -T timescaledb pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc -f /tmp/sensegrid_backup.dump
compose cp "timescaledb:/tmp/sensegrid_backup.dump" "$DUMP_FILE"
backup_s=$((SECONDS - t0))
log "backup done in ${backup_s}s -> $DUMP_FILE ($(du -h "$DUMP_FILE" | cut -f1))"
csv_row "$OUT" "backup" "$backup_s" "$pre_counts"

log "=== starting the RTO clock: destroying deploy_timescale_data ==="
rto_t0=$SECONDS
compose stop timescaledb
compose rm -f timescaledb
docker volume rm deploy_timescale_data

log "recreating the service (fresh volume, fresh initdb, timescaledb extension auto-created by the base image)"
compose up -d timescaledb
deadline=$((SECONDS + 90))
until [ "$(docker inspect deploy-timescaledb-1 --format '{{.State.Health.Status}}' 2>/dev/null)" = "healthy" ]; do
	if [ "$SECONDS" -ge "$deadline" ]; then
		log "ERROR: timescaledb didn't become healthy within 90s of recreation"
		exit 1
	fi
	sleep 2
done
log "fresh container healthy after $((SECONDS - rto_t0))s"

log "restoring: timescaledb_pre_restore() -> pg_restore -> timescaledb_post_restore()"
compose cp "$DUMP_FILE" "timescaledb:/tmp/sensegrid_backup.dump"
psql_query "SELECT timescaledb_pre_restore();" > /dev/null
compose exec -T timescaledb pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --no-owner /tmp/sensegrid_backup.dump
psql_query "SELECT timescaledb_post_restore();" > /dev/null

post_counts="$(count_all)"
rto_s=$((SECONDS - rto_t0))
log "post-restore counts: $post_counts"
csv_row "$OUT" "restored" "$rto_s" "$post_counts"

if [ "$pre_counts" != "$post_counts" ]; then
	log "ERROR: post-restore counts don't match pre-backup counts (pre=$pre_counts post=$post_counts)"
	exit 1
fi

log "verifying continuous aggregates: refreshing readings_1m/readings_1h post-restore"
psql_query "CALL refresh_continuous_aggregate('readings_1m', NULL, NULL);" > /dev/null
psql_query "CALL refresh_continuous_aggregate('readings_1h', NULL, NULL);" > /dev/null
refreshed_counts="$(count_all)"
csv_row "$OUT" "aggregates_refreshed" "$((SECONDS - rto_t0))" "$refreshed_counts"

log "resuming ingest/processor"
compose up -d ingest processor > /dev/null

log "=== RTO: ${rto_s}s (volume destroyed -> data verified + aggregates refreshed) === -> $OUT"
