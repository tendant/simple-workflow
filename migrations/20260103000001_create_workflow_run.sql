-- +goose Up
-- Create workflow schema
CREATE SCHEMA IF NOT EXISTS workflow;

-- Create workflow_run table
-- This table serves as a durable inbox for all async work

CREATE TABLE IF NOT EXISTS workflow.workflow_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,  -- e.g. 'content.thumbnail.v1' (renamed from 'name' for semantic clarity)
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'succeeded', 'failed', 'cancelled')),
    priority INT NOT NULL DEFAULT 100,
    run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),  -- renamed from run_after for clarity
    idempotency_key TEXT UNIQUE,
    attempt INT NOT NULL DEFAULT 0,  -- renamed from attempt_count
    max_attempts INT NOT NULL DEFAULT 3,
    leased_by TEXT,  -- renamed from claimed_by
    lease_until TIMESTAMPTZ,  -- renamed from lease_expires_at
    last_error TEXT,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ  -- soft delete support
);

-- Index for efficient run claiming by workers
CREATE INDEX IF NOT EXISTS idx_workflow_run_claim
    ON workflow.workflow_run(status, run_at, priority, created_at)
    WHERE status = 'pending';

-- Index for type-prefix routing (e.g. "billing.%", "media.%")
-- Using text_pattern_ops for efficient LIKE 'prefix%' queries
CREATE INDEX IF NOT EXISTS idx_workflow_run_type_prefix
    ON workflow.workflow_run(type text_pattern_ops, status, run_at)
    WHERE status = 'pending';

-- Index for idempotency checking
CREATE INDEX IF NOT EXISTS idx_workflow_run_idempotency
    ON workflow.workflow_run(idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Index for monitoring and debugging
CREATE INDEX IF NOT EXISTS idx_workflow_run_type_status
    ON workflow.workflow_run(type, status, created_at DESC);

-- Index for soft delete queries (partial index for active records)
CREATE INDEX IF NOT EXISTS idx_workflow_run_deleted_at
    ON workflow.workflow_run(deleted_at)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE workflow.workflow_run IS 'Durable inbox for async workflow runs';
COMMENT ON COLUMN workflow.workflow_run.id IS 'Primary key UUID';
COMMENT ON COLUMN workflow.workflow_run.type IS 'Versioned workflow type with dotted notation (e.g. billing.invoice.v1)';
COMMENT ON COLUMN workflow.workflow_run.status IS 'Current status: pending, leased, succeeded, failed, cancelled';
COMMENT ON COLUMN workflow.workflow_run.priority IS 'Lower values execute first (default: 100)';
COMMENT ON COLUMN workflow.workflow_run.run_at IS 'Earliest time to execute (for scheduling/retry backoff)';
COMMENT ON COLUMN workflow.workflow_run.idempotency_key IS 'Optional key for deduplication';
COMMENT ON COLUMN workflow.workflow_run.attempt IS 'Number of execution attempts (0-indexed)';
COMMENT ON COLUMN workflow.workflow_run.leased_by IS 'Worker ID that currently holds the lease';
COMMENT ON COLUMN workflow.workflow_run.lease_until IS 'Lease expiration timestamp';
COMMENT ON COLUMN workflow.workflow_run.deleted_at IS 'Soft delete timestamp (NULL = active)';

-- +goose Down
DROP TABLE IF EXISTS workflow.workflow_run CASCADE;
