-- +goose Up
CREATE TABLE IF NOT EXISTS workflow_schedule (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,                              -- workflow type to create
    payload JSONB NOT NULL DEFAULT '{}',             -- static payload for each firing
    schedule TEXT NOT NULL,                           -- cron expression (5-field)
    timezone TEXT NOT NULL DEFAULT 'UTC',             -- IANA timezone
    next_run_at TIMESTAMPTZ NOT NULL,                -- pre-computed next fire time
    last_run_at TIMESTAMPTZ,                         -- last time a run was created
    enabled BOOLEAN NOT NULL DEFAULT true,            -- pause/resume
    priority INT NOT NULL DEFAULT 100,               -- inherited by created runs
    max_attempts INT NOT NULL DEFAULT 3,             -- inherited by created runs
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ                           -- soft delete
);

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_next_run
    ON workflow_schedule(next_run_at)
    WHERE enabled = true AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_type
    ON workflow_schedule(type);

-- +goose Down
DROP INDEX IF EXISTS idx_workflow_schedule_type;
DROP INDEX IF EXISTS idx_workflow_schedule_next_run;
DROP TABLE IF EXISTS workflow_schedule;
