-- +goose Up
-- Create workflow_registry table
-- Declarative mapping of workflow name → execution runtime

CREATE TABLE IF NOT EXISTS workflow.workflow_registry (
    workflow_name TEXT PRIMARY KEY,  -- e.g. 'content.thumbnail.v1'
    intent_type TEXT NOT NULL DEFAULT 'workflow',
    runtime TEXT NOT NULL,  -- 'go' or 'python'
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for querying by runtime
CREATE INDEX IF NOT EXISTS idx_workflow_registry_runtime
    ON workflow.workflow_registry(runtime, is_enabled);

COMMENT ON TABLE workflow.workflow_registry IS 'Registry mapping workflow names to execution runtimes';
COMMENT ON COLUMN workflow.workflow_registry.workflow_name IS 'Unique workflow name (e.g. content.thumbnail.v1)';
COMMENT ON COLUMN workflow.workflow_registry.runtime IS 'Execution runtime: go or python';
COMMENT ON COLUMN workflow.workflow_registry.is_enabled IS 'Whether this workflow is currently enabled';

-- Seed initial workflows
INSERT INTO workflow.workflow_registry (workflow_name, runtime, description) VALUES
('content.thumbnail.v1', 'go', 'Generate image thumbnails'),
('content.ocr.v1', 'python', 'Extract text using PaddleOCR'),
('content.object_detection.v1', 'python', 'Detect objects using YOLO11')
ON CONFLICT (workflow_name) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS workflow.workflow_registry CASCADE;
