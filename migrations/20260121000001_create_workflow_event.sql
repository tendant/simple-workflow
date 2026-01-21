-- +goose Up
-- Create workflow_event table for audit trail
-- This table logs all lifecycle events for workflow runs

CREATE TABLE IF NOT EXISTS workflow_event (
    id BIGSERIAL PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'leased', 'started', 'heartbeat', 'succeeded', 'failed', 'retried', 'cancelled')),
    data JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for querying events by workflow_id (most common query pattern)
CREATE INDEX IF NOT EXISTS idx_workflow_event_workflow_id
    ON workflow_event(workflow_id, created_at DESC);

-- Index for querying events by type (for analytics/monitoring)
CREATE INDEX IF NOT EXISTS idx_workflow_event_type
    ON workflow_event(event_type, created_at DESC);

COMMENT ON TABLE workflow_event IS 'Audit trail of all workflow run lifecycle events';
COMMENT ON COLUMN workflow_event.id IS 'Auto-incrementing event ID';
COMMENT ON COLUMN workflow_event.workflow_id IS 'Reference to workflow_run.id';
COMMENT ON COLUMN workflow_event.event_type IS 'Event type: created, leased, started, heartbeat, succeeded, failed, retried, cancelled';
COMMENT ON COLUMN workflow_event.data IS 'Additional event metadata (JSON)';
COMMENT ON COLUMN workflow_event.created_at IS 'Event timestamp';

-- +goose Down
DROP TABLE IF EXISTS workflow_event CASCADE;
