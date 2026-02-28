package simpleworkflow

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq" // PostgreSQL driver
)

// PostgresDialect implements Dialect for PostgreSQL.
type PostgresDialect struct{}

func (d *PostgresDialect) DriverName() string { return "postgres" }

func (d *PostgresDialect) Placeholder(n int) string {
	return fmt.Sprintf("$%d", n)
}

func (d *PostgresDialect) Now() string { return "NOW()" }

func (d *PostgresDialect) TimestampAfterNow(seconds int) string {
	return fmt.Sprintf("NOW() + interval '%d seconds'", seconds)
}

func (d *PostgresDialect) IntervalParam(n int, seconds int) (string, interface{}) {
	return fmt.Sprintf("$%d::interval", n), fmt.Sprintf("%d seconds", seconds)
}

func (d *PostgresDialect) ClaimRunQuery(typeCondition string, leaseSec int) string {
	return fmt.Sprintf(`
		UPDATE workflow_run
		SET status = 'leased',
			leased_by = $1,
			lease_until = NOW() + $2::interval,
			updated_at = NOW()
		WHERE id = (
			SELECT id FROM workflow_run
			WHERE status = 'pending'
			  AND run_at <= NOW()
			  AND deleted_at IS NULL
			  %s
			ORDER BY priority ASC, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id, type, payload, attempt, max_attempts
	`, typeCondition)
}

func (d *PostgresDialect) ClaimSchedulesQuery() string {
	return `
		SELECT id, type, payload, schedule, timezone, next_run_at, priority, max_attempts
		FROM workflow_schedule
		WHERE next_run_at <= NOW()
		  AND enabled = true
		  AND deleted_at IS NULL
		FOR UPDATE SKIP LOCKED
		LIMIT 10
	`
}

func (d *PostgresDialect) MigrateSQL() string {
	return postgresMigrateSQL
}

func (d *PostgresDialect) OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres database: %w", err)
	}
	return db, nil
}

const postgresMigrateSQL = `
CREATE TABLE IF NOT EXISTS workflow_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'succeeded', 'failed', 'cancelled')),
    priority INT NOT NULL DEFAULT 100,
    run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key TEXT UNIQUE,
    attempt INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 3,
    leased_by TEXT,
    lease_until TIMESTAMPTZ,
    last_error TEXT,
    result JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_workflow_run_claim
    ON workflow_run(status, run_at, priority, created_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_workflow_run_type_prefix
    ON workflow_run(type text_pattern_ops, status, run_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_workflow_run_idempotency
    ON workflow_run(idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_workflow_run_type_status
    ON workflow_run(type, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workflow_run_deleted_at
    ON workflow_run(deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS workflow_event (
    id BIGSERIAL PRIMARY KEY,
    workflow_id UUID NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL CHECK (event_type IN ('created', 'leased', 'started', 'heartbeat', 'succeeded', 'failed', 'retried', 'cancelled')),
    data JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_workflow_event_workflow_id
    ON workflow_event(workflow_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workflow_event_type
    ON workflow_event(event_type, created_at DESC);

CREATE TABLE IF NOT EXISTS workflow_registry (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_name TEXT NOT NULL UNIQUE,
    intent_type TEXT NOT NULL DEFAULT 'workflow',
    runtime TEXT NOT NULL,
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    description TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_workflow_registry_runtime
    ON workflow_registry(runtime, is_enabled);

CREATE INDEX IF NOT EXISTS idx_workflow_registry_deleted_at
    ON workflow_registry(deleted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS workflow_schedule (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}',
    schedule TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    enabled BOOLEAN NOT NULL DEFAULT true,
    priority INT NOT NULL DEFAULT 100,
    max_attempts INT NOT NULL DEFAULT 3,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_next_run
    ON workflow_schedule(next_run_at)
    WHERE enabled = true AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_workflow_schedule_type
    ON workflow_schedule(type);
`
