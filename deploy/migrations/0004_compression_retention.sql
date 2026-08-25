-- no-transaction
-- Compression and retention policies, like continuous aggregates, run
-- outside an explicit transaction — see the 0003 migration's note.
--
-- Compress chunks older than 2 days: raw readings are the largest thing
-- in this database by far, and by then nothing is still writing to them.
ALTER TABLE readings SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id, sensor_type',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('readings', compress_after => INTERVAL '2 days');

-- Drop raw readings after 7 days; the continuous aggregates already
-- computed for that range live on independently of the source rows.
SELECT add_retention_policy('readings', drop_after => INTERVAL '7 days');
