-- Phase 4 device shadow audit trail. The JetStream KV bucket ("SHADOW",
-- see internal/shadow) is the hot path devices/REST handlers actually read
-- from; this table is insert-only and never read back on that path — it
-- exists purely so shadow history survives longer than the KV bucket's
-- History:5 revision cap and is queryable for reporting later.
CREATE TABLE shadow_history (
    id          uuid PRIMARY KEY,
    device_id   uuid NOT NULL REFERENCES devices(id),
    direction   text NOT NULL CHECK (direction IN ('desired', 'reported')),
    revision    bigint NOT NULL,
    payload     jsonb NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX shadow_history_device_time_idx
    ON shadow_history (device_id, recorded_at DESC);
