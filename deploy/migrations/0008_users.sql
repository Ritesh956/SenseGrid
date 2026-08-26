-- Phase 5 console login. id is generated in Go (uuid.New(), see
-- internal/users.Store.Create) rather than a Postgres default, matching
-- devices.id/alerts.id's existing convention (0001_devices.sql,
-- 0005_alerts.sql). password_hash is a bcrypt hash — only
-- `control user create` (cmd/control/user_cli.go) ever writes this table.
CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    created_at    timestamptz NOT NULL DEFAULT now()
);
