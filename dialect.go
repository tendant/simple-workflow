package simpleworkflow

import "database/sql"

// Dialect abstracts SQL differences between PostgreSQL and SQLite.
// All dialect-specific SQL generation flows through this interface.
type Dialect interface {
	// DriverName returns the database/sql driver name (e.g. "postgres", "sqlite").
	DriverName() string

	// Placeholder returns a positional parameter placeholder.
	// For PostgreSQL: Placeholder(1) → "$1"
	// For SQLite:     Placeholder(1) → "?"
	Placeholder(n int) string

	// Now returns the SQL expression for the current timestamp.
	// PostgreSQL: "NOW()"
	// SQLite:     "datetime('now')"
	Now() string

	// TimestampAfterNow returns a SQL expression for NOW() + seconds.
	// PostgreSQL: "NOW() + $1::interval" (with value "N seconds")
	// SQLite:     "datetime('now', '+N seconds')"
	// The literal parameter indicates whether to embed the value literally
	// or use a placeholder.
	TimestampAfterNow(seconds int) string

	// IntervalParam returns the SQL fragment and argument for adding a duration.
	// PostgreSQL: returns ("$N::interval", "30 seconds")
	// SQLite:     not used (TimestampAfterNow embeds the value)
	IntervalParam(n int, seconds int) (sqlFragment string, arg interface{})

	// ClaimRunQuery returns the SQL for atomically claiming a workflow run.
	// typeCondition is an already-formatted "AND (type LIKE ...)" fragment.
	// leaseSec is the lease duration in seconds.
	// For PostgreSQL, caller provides args: [workerID, intervalString, ...typePrefixes].
	// For SQLite, caller provides args: [workerID, ...typePrefixes] (lease embedded in SQL).
	ClaimRunQuery(typeCondition string, leaseSec int) string

	// ClaimSchedulesQuery returns the SQL for claiming due schedules.
	ClaimSchedulesQuery() string

	// MigrateSQL returns the DDL statements for creating all tables.
	MigrateSQL() string

	// OpenDB opens a database connection with dialect-specific settings.
	OpenDB(dsn string) (*sql.DB, error)
}
