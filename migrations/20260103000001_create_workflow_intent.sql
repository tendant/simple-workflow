-- +goose Up
-- Create workflow schema
CREATE SCHEMA IF NOT EXISTS workflow;

-- Create workflow_intent table
-- This table serves as a durable inbox for all async work

CREATE TABLE IF NOT EXISTS workflow.workflow_intent (
    intent_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_type TEXT NOT NULL DEFAULT 'workflow',
    name TEXT NOT NULL,  -- e.g. 'content.thumbnail.v1'
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, succeeded, failed, deadletter
    priority INT NOT NULL DEFAULT 100,
    run_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key TEXT UNIQUE,
    attempt_count INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    claimed_by TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for efficient intent claiming by workers
CREATE INDEX IF NOT EXISTS idx_workflow_intent_claim
    ON workflow.workflow_intent(status, run_after, priority, created_at)
    WHERE status = 'pending';

-- Index for idempotency checking
CREATE INDEX IF NOT EXISTS idx_workflow_intent_idempotency
    ON workflow.workflow_intent(idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Index for monitoring and debugging
CREATE INDEX IF NOT EXISTS idx_workflow_intent_name_status
    ON workflow.workflow_intent(name, status, created_at DESC);

COMMENT ON TABLE workflow.workflow_intent IS 'Durable inbox for async workflow intents';
COMMENT ON COLUMN workflow.workflow_intent.intent_id IS 'Unique identifier for the intent';
COMMENT ON COLUMN workflow.workflow_intent.name IS 'Versioned workflow name (e.g. content.thumbnail.v1)';
COMMENT ON COLUMN workflow.workflow_intent.status IS 'Current status: pending, running, succeeded, failed, deadletter';
COMMENT ON COLUMN workflow.workflow_intent.priority IS 'Lower values execute first (default: 100)';
COMMENT ON COLUMN workflow.workflow_intent.run_after IS 'Earliest time to execute (for scheduling/retry backoff)';
COMMENT ON COLUMN workflow.workflow_intent.idempotency_key IS 'Optional key for deduplication';

-- +goose Down
DROP TABLE IF EXISTS workflow.workflow_intent CASCADE;
