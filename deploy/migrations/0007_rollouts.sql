-- Phase 4 Pass B: staged rollout state. Unlike the device shadow (Phase 4
-- Pass A), whose hot path is a JetStream KV bucket with Postgres only
-- mirroring history, rollout orchestration state lives in Postgres as the
-- source of truth — internal/rollout.Engine reads it back at startup
-- (ListNonTerminal) to resume any rollout that was in-flight when the
-- process last stopped.
CREATE TABLE rollouts (
    id                        uuid PRIMARY KEY,
    name                      text NOT NULL,
    cohort                    jsonb NOT NULL,
    desired_config            jsonb NOT NULL,
    stages                    jsonb NOT NULL,
    health_criteria           jsonb NOT NULL,
    state                     text NOT NULL DEFAULT 'running',
    current_stage_index       int NOT NULL DEFAULT 0,
    current_stage_started_at  timestamptz NOT NULL DEFAULT now(),
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);

-- Every device a rollout has ever pushed config to, plus enough of its
-- pre-rollout shadow state to revert on auto-rollback (or a manual
-- abort). pre_rollout_desired is NULL when the device had no desired
-- config before this rollout touched it — see internal/rollout.Engine's
-- rollbackLocked for how that case is handled (left as-is, not reverted).
CREATE TABLE rollout_targets (
    rollout_id            uuid NOT NULL REFERENCES rollouts(id),
    device_id             uuid NOT NULL REFERENCES devices(id),
    included_at_stage     int NOT NULL,
    pre_rollout_desired   jsonb,
    pre_rollout_revision  bigint NOT NULL DEFAULT 0,
    pushed_revision       bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (rollout_id, device_id)
);
