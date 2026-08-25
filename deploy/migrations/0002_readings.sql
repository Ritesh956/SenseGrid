-- Raw readings hypertable.
--
-- Chunk interval: 1 hour. At Phase 2 scale (a handful of real devices, the
-- PWA publishing up to ~60 msg/s across 3 sensor types at 20Hz, hostagent
-- around 1 msg/s) that keeps each chunk's working set comfortably small
-- and in memory without fragmenting the index over thousands of tiny
-- chunks. Phase 7's load test (up to 500 synthetic devices) is the real
-- test of this choice — revisit there if chunks start ballooning.
CREATE TABLE readings (
    time        timestamptz NOT NULL,
    device_id   uuid NOT NULL REFERENCES devices(id),
    sensor_type text NOT NULL,
    value       double precision NOT NULL,
    device_time timestamptz NOT NULL,
    ingest_time timestamptz NOT NULL,
    seq         bigint NOT NULL
);

SELECT create_hypertable('readings', 'time', chunk_time_interval => interval '1 hour');

-- Doubles as the idempotency key for at-least-once redelivery from
-- JetStream: the persistence consumer inserts with ON CONFLICT DO NOTHING
-- against this index, so a redelivered message is a no-op, not a
-- duplicate row.
CREATE UNIQUE INDEX readings_device_sensor_seq_time_idx
    ON readings (device_id, sensor_type, seq, time);

-- The common query: one device's one sensor over a time range.
CREATE INDEX readings_device_sensor_time_idx
    ON readings (device_id, sensor_type, time DESC);
