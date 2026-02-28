package simpleworkflow

import (
	"database/sql"
	"fmt"
)

// SQLiteDialect implements Dialect for SQLite (via modernc.org/sqlite).
type SQLiteDialect struct{}

func (d *SQLiteDialect) DriverName() string { return "sqlite" }

func (d *SQLiteDialect) Placeholder(n int) string { return "?" }

func (d *SQLiteDialect) Now() string { return "datetime('now')" }

func (d *SQLiteDialect) TimestampAfterNow(seconds int) string {
	return fmt.Sprintf("datetime('now', '+%d seconds')", seconds)
}

func (d *SQLiteDialect) IntervalParam(n int, seconds int) (string, interface{}) {
	// SQLite doesn't use interval params; value is embedded in SQL via TimestampAfterNow.
	return fmt.Sprintf("datetime('now', '+%d seconds')", seconds), nil
}

func (d *SQLiteDialect) ClaimRunQuery(typeCondition string, leaseSec int) string {
	// SQLite: no FOR UPDATE SKIP LOCKED. Single-writer via WAL + _txlock=immediate
	// serializes writes. UPDATE...WHERE id=(SELECT...LIMIT 1) RETURNING is atomic.
	return fmt.Sprintf(`
		UPDATE workflow_run
		SET status = 'leased',
			leased_by = ?,
			lease_until = datetime('now', '+%d seconds'),
			updated_at = datetime('now')
		WHERE id = (
			SELECT id FROM workflow_run
			WHERE status = 'pending'
			  AND run_at <= datetime('now')
			  AND deleted_at IS NULL
			  %s
			ORDER BY priority ASC, created_at ASC
			LIMIT 1
		)
		RETURNING id, type, payload, attempt, max_attempts
	`, leaseSec, typeCondition)
}

func (d *SQLiteDialect) ClaimSchedulesQuery() string {
	// SQLite: no row-level locking needed; single-writer serializes via WAL.
	return `
		SELECT id, type, payload, schedule, timezone, next_run_at, priority, max_attempts
		FROM workflow_schedule
		WHERE next_run_at <= datetime('now')
		  AND enabled = 1
		  AND deleted_at IS NULL
		LIMIT 10
	`
}

func (d *SQLiteDialect) MigrateSQL() string {
	return sqliteMigrateSQL
}

func (d *SQLiteDialect) OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Enable WAL mode for concurrent reads + serialized writes
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout so concurrent writers wait instead of failing
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy_timeout: %w", err)
	}

	// Foreign keys are off by default in SQLite
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Single connection for write serialization
	db.SetMaxOpenConns(1)

	return db, nil
}
