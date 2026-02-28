package simpleworkflow

// sqliteMigrateSQL contains the DDL for creating all tables in SQLite.
// Key differences from PostgreSQL:
// - TEXT instead of UUID, JSONB, TIMESTAMPTZ
// - INTEGER instead of BOOLEAN, BIGSERIAL
// - No gen_random_uuid() (IDs generated in Go code)
// - No partial indexes (SQLite supports them but syntax differs)
// - No text_pattern_ops
// - datetime('now') instead of NOW()
const sqliteMigrateSQL = `
CREATE TABLE IF NOT EXISTS workflow_run (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    payload TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'succeeded', 'failed', 'cancelled')),
    priority INTEGER NOT NULL DEFAULT 100,
    run_at TEXT NOT NULL DEFAULT (datetime('now')),
    idempotency_key TEXT UNIQUE,
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    leased_by TEXT,
    lease_until TEXT,
    last_error TEXT,
    result TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_workflow_run_claim
    ON workflow_run(status, run_at, priority, created_at);

CREATE INDEX IF NOT EXISTS idx_workflow_run_type_prefix
    ON workflow_run(type, status, run_at);

CREATE INDEX IF NOT EXISTS idx_workflow_run_idempotency
    ON workflow_run(idempotency_key);

CREATE INDEX IF NOT EXISTS idx_workflow_run_type_status
    ON workflow_run(type, status, created_at);

CREATE INDEX IF NOT EXISTS idx_workflow_run_deleted_at
    ON workflow_run(deleted_at);

CREATE TABLE IF NOT EXISTS workflow_event (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workflow_id TEXT NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'leased', 'started', 'heartbeat', 'succeeded', 'failed', 'retried', 'cancelled')),
    data TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_workflow_event_workflow_id
    ON workflow_event(workflow_id, created_at);

CREATE INDEX IF NOT EXISTS idx_workflow_event_type
    ON workflow_event(event_type, created_at);

CREATE TABLE IF NOT EXISTS workflow_registry (
    id TEXT PRIMARY KEY,
    workflow_name TEXT NOT NULL UNIQUE,
    intent_type TEXT NOT NULL DEFAULT 'workflow',
    runtime TEXT NOT NULL,
    is_enabled INTEGER NOT NULL DEFAULT 1,
    description TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_workflow_registry_runtime
    ON workflow_registry(runtime, is_enabled);

CREATE INDEX IF NOT EXISTS idx_workflow_registry_deleted_at
    ON workflow_registry(deleted_at);

CREATE TABLE IF NOT EXISTS workflow_schedule (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    schedule TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    next_run_at TEXT NOT NULL,
    last_run_at TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    priority INTEGER NOT NULL DEFAULT 100,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    deleted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_next_run
    ON workflow_schedule(next_run_at);

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_type
    ON workflow_schedule(type);
`
