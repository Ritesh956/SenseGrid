-- Phase 3 alert lifecycle table. id is generated in Go (uuid.New(), see
-- internal/alerts.Store.Open) rather than a Postgres default, matching
-- devices.id's existing convention (0001_devices.sql).
CREATE TABLE alerts (
    id              uuid PRIMARY KEY,
    device_id       uuid NOT NULL REFERENCES devices(id),
    sensor_type     text NOT NULL,
    rule_name       text NOT NULL,
    severity        text NOT NULL,
    state           text NOT NULL DEFAULT 'firing',
    detail          jsonb NOT NULL DEFAULT '{}',
    fired_at        timestamptz NOT NULL,
    acknowledged_at timestamptz,
    resolved_at     timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Enforces "never re-fire an already-firing alert" at the database level:
-- at most one non-resolved alert can exist per (device, sensor, rule).
-- A cleared-then-refired condition opens a new row instead of reusing the
-- resolved one, so alert history stays append-only.
CREATE UNIQUE INDEX alerts_open_idx
    ON alerts (device_id, sensor_type, rule_name)
    WHERE state <> 'resolved';

CREATE INDEX alerts_device_time_idx ON alerts (device_id, fired_at DESC);
