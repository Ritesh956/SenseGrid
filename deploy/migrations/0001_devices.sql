-- Device registry — the system of record for claimed devices, replacing
-- Phase 1's Redis placeholder (internal/devicestore keeps only the
-- short-lived, naturally-TTL'd registration tokens now).
CREATE TABLE devices (
    id            uuid PRIMARY KEY,
    name          text NOT NULL,
    type          text NOT NULL,
    registered_at timestamptz NOT NULL DEFAULT now(),
    last_seen     timestamptz,
    status        text NOT NULL DEFAULT 'unknown',
    metadata      jsonb NOT NULL DEFAULT '{}'
);
