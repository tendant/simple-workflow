package simpleworkflow

// Import the pure-Go SQLite driver (no CGO required).
// This driver registers itself as "sqlite" with database/sql.
// It supports RETURNING clauses needed for atomic claim queries.
import _ "modernc.org/sqlite"
