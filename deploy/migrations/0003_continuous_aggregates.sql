-- no-transaction
-- Continuous aggregate creation isn't guaranteed safe inside an explicit
-- transaction block across TimescaleDB versions, so this file (unlike the
-- others) runs statement-by-statement outside one — see
-- internal/migrations for what the "-- no-transaction" marker does.
CREATE MATERIALIZED VIEW readings_1m
WITH (timescaledb.continuous) AS
SELECT
    time_bucket('1 minute', time) AS bucket,
    device_id,
    sensor_type,
    avg(value) AS avg,
    min(value) AS min,
    max(value) AS max,
    count(*)   AS n
FROM readings
GROUP BY 1, 2, 3
WITH NO DATA;

CREATE MATERIALIZED VIEW readings_1h
WITH (timescaledb.continuous) AS
SELECT
    time_bucket('1 hour', time) AS bucket,
    device_id,
    sensor_type,
    avg(value) AS avg,
    min(value) AS min,
    max(value) AS max,
    count(*)   AS n
FROM readings
GROUP BY 1, 2, 3
WITH NO DATA;

SELECT add_continuous_aggregate_policy('readings_1m',
    start_offset => INTERVAL '3 hours',
    end_offset   => INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute');

SELECT add_continuous_aggregate_policy('readings_1h',
    start_offset => INTERVAL '3 days',
    end_offset   => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');
