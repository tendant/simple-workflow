package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Client manages workflow runs in the database (Producer API)
type Client struct {
	db      *sql.DB
	dialect Dialect
}

// NewClient creates a new client from a connection string.
// The connection string determines the dialect (PostgreSQL or SQLite).
//
// Examples:
//   - NewClient("postgres://user:pass@localhost/db?schema=workflow")
//   - NewClient("sqlite:///path/to/db.sqlite")
//   - NewClient("sqlite://:memory:")
//
// The client will manage the database connection and Close() must be called.
func NewClient(connString string) (*Client, error) {
	dialect, dsn, err := DetectDialect(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	db, err := dialect.OpenDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Client{db: db, dialect: dialect}, nil
}

// NewClientWithDB creates a new client from an existing *sql.DB and Dialect.
// Useful for testing or when you manage the connection yourself.
func NewClientWithDB(db *sql.DB, dialect Dialect) *Client {
	return &Client{db: db, dialect: dialect}
}

// Close closes the database connection.
func (c *Client) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// create inserts a new workflow run (internal method)
func (c *Client) create(ctx context.Context, intent Intent) (string, error) {
	payloadJSON, err := json.Marshal(intent.Payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	runID := uuid.New().String()
	priority := intent.Priority
	if priority == 0 {
		priority = 100
	}
	runAfter := intent.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	maxAttempts := intent.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 3
	}

	query := c.rewrite(`
		INSERT INTO workflow_run (
			id, type, payload, priority, run_at,
			idempotency_key, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`)

	var returnedID string
	err = c.db.QueryRowContext(ctx, query,
		runID, intent.Type, payloadJSON, priority, runAfter,
		sql.NullString{String: intent.IdempotencyKey, Valid: intent.IdempotencyKey != ""},
		maxAttempts,
	).Scan(&returnedID)

	if err == sql.ErrNoRows {
		// Idempotency key conflict - run already exists
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to insert workflow run: %w", err)
	}

	// Log creation event (best-effort, don't fail if event logging fails)
	c.logEvent(ctx, returnedID, "created", nil)

	return returnedID, nil
}

// rewrite converts $N placeholders to ? for SQLite, or returns as-is for PostgreSQL.
func (c *Client) rewrite(query string) string {
	if c.dialect.DriverName() == "postgres" {
		return query
	}
	return RewritePlaceholders(query)
}

// Cancel marks a workflow run as cancelled (cooperative cancellation)
func (c *Client) Cancel(ctx context.Context, runID string) error {
	query := c.rewrite(fmt.Sprintf(`
		UPDATE workflow_run
		SET status = 'cancelled', updated_at = %s
		WHERE id = $1 AND status IN ('pending', 'leased')
	`, c.dialect.Now()))

	result, err := c.db.ExecContext(ctx, query, runID)
	if err != nil {
		return fmt.Errorf("failed to cancel workflow run: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("workflow run not found or already completed")
	}

	// Log cancellation event (best-effort)
	c.logEvent(ctx, runID, "cancelled", nil)

	return nil
}

// logEvent inserts an audit event (best-effort, errors are ignored)
func (c *Client) logEvent(ctx context.Context, workflowID, eventType string, data map[string]interface{}) {
	var dataJSON []byte
	var err error
	if data != nil {
		dataJSON, err = json.Marshal(data)
		if err != nil {
			return // Ignore error
		}
	}

	query := c.rewrite(`
		INSERT INTO workflow_event (workflow_id, event_type, data)
		VALUES ($1, $2, $3)
	`)

	// Use a short timeout for event logging to avoid blocking
	eventCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, _ = c.db.ExecContext(eventCtx, query, workflowID, eventType, dataJSON)
}

// Submit creates a new workflow run with fluent configuration.
// Returns a SubmitBuilder for chaining configuration methods.
//
// Example:
//   runID, err := client.Submit("billing.invoice.v1", payload).
//       WithIdempotency("invoice:123").
//       WithPriority(10).
//       Execute(ctx)
func (c *Client) Submit(workflowType string, payload interface{}) *SubmitBuilder {
	return &SubmitBuilder{
		client: c,
		intent: Intent{
			Type:        workflowType,
			Payload:     payload,
			Priority:    100, // default priority
			MaxAttempts: 3,   // default max attempts
		},
	}
}

// SubmitBuilder provides a fluent API for configuring and submitting workflow runs.
type SubmitBuilder struct {
	client *Client
	intent Intent
}

// WithIdempotency sets an idempotency key for deduplication.
// If a workflow run with this key already exists, submission will be skipped.
func (s *SubmitBuilder) WithIdempotency(key string) *SubmitBuilder {
	s.intent.IdempotencyKey = key
	return s
}

// WithPriority sets the priority (lower number = higher priority).
// Default: 100
func (s *SubmitBuilder) WithPriority(priority int) *SubmitBuilder {
	s.intent.Priority = priority
	return s
}

// WithMaxAttempts sets the maximum number of retry attempts.
// Default: 3
func (s *SubmitBuilder) WithMaxAttempts(attempts int) *SubmitBuilder {
	s.intent.MaxAttempts = attempts
	return s
}

// RunAfter schedules the workflow to run after a specific time.
func (s *SubmitBuilder) RunAfter(t time.Time) *SubmitBuilder {
	s.intent.RunAfter = t
	return s
}

// RunIn schedules the workflow to run after a duration from now.
func (s *SubmitBuilder) RunIn(d time.Duration) *SubmitBuilder {
	s.intent.RunAfter = time.Now().Add(d)
	return s
}

// Execute submits the workflow run and returns its ID.
// Returns empty string if idempotency key conflict (run already exists).
func (s *SubmitBuilder) Execute(ctx context.Context) (string, error) {
	return s.client.create(ctx, s.intent)
}

// GetWorkflowRun retrieves a single workflow run by ID.
func (c *Client) GetWorkflowRun(ctx context.Context, id string) (*WorkflowRunStatus, error) {
	query := c.rewrite(`
		SELECT id, type, payload, status, priority, run_at,
			   idempotency_key, attempt, max_attempts,
			   leased_by, lease_until, last_error, result,
			   created_at, updated_at
		FROM workflow_run
		WHERE id = $1 AND deleted_at IS NULL
	`)

	var run WorkflowRunStatus
	var idempotencyKey, leasedBy, lastError sql.NullString
	var leaseUntilStr sql.NullString
	var runAtStr, createdAtStr, updatedAtStr string
	var result []byte

	err := c.db.QueryRowContext(ctx, query, id).Scan(
		&run.ID, &run.Type, &run.Payload, &run.Status, &run.Priority, &runAtStr,
		&idempotencyKey, &run.Attempt, &run.MaxAttempts,
		&leasedBy, &leaseUntilStr, &lastError, &result,
		&createdAtStr, &updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow run: %w", err)
	}

	run.RunAt, _ = parseTimestamp(runAtStr)
	run.CreatedAt, _ = parseTimestamp(createdAtStr)
	run.UpdatedAt, _ = parseTimestamp(updatedAtStr)

	if idempotencyKey.Valid {
		run.IdempotencyKey = &idempotencyKey.String
	}
	if leasedBy.Valid {
		run.LeasedBy = &leasedBy.String
	}
	if leaseUntilStr.Valid {
		t, _ := parseTimestamp(leaseUntilStr.String)
		run.LeaseUntil = &t
	}
	if lastError.Valid {
		run.LastError = &lastError.String
	}
	if result != nil {
		run.Result = result
	}

	return &run, nil
}

// ListWorkflowRuns returns workflow runs matching the given options.
func (c *Client) ListWorkflowRuns(ctx context.Context, opts ListOptions) ([]WorkflowRunStatus, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	query := "SELECT id, type, payload, status, priority, run_at, idempotency_key, attempt, max_attempts, leased_by, lease_until, last_error, result, created_at, updated_at FROM workflow_run WHERE deleted_at IS NULL"
	var args []interface{}
	argN := 1

	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = %s", c.dialect.Placeholder(argN))
		args = append(args, opts.Type)
		argN++
	}
	if opts.Status != "" {
		query += fmt.Sprintf(" AND status = %s", c.dialect.Placeholder(argN))
		args = append(args, opts.Status)
		argN++
	}

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT %s OFFSET %s", c.dialect.Placeholder(argN), c.dialect.Placeholder(argN+1))
	args = append(args, opts.Limit, opts.Offset)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}
	defer rows.Close()

	var runs []WorkflowRunStatus
	for rows.Next() {
		var run WorkflowRunStatus
		var idempotencyKey, leasedBy, lastError sql.NullString
		var leaseUntilStr sql.NullString
		var runAtStr, createdAtStr, updatedAtStr string
		var result []byte

		if err := rows.Scan(
			&run.ID, &run.Type, &run.Payload, &run.Status, &run.Priority, &runAtStr,
			&idempotencyKey, &run.Attempt, &run.MaxAttempts,
			&leasedBy, &leaseUntilStr, &lastError, &result,
			&createdAtStr, &updatedAtStr,
		); err != nil {
			return nil, fmt.Errorf("failed to scan workflow run: %w", err)
		}

		run.RunAt, _ = parseTimestamp(runAtStr)
		run.CreatedAt, _ = parseTimestamp(createdAtStr)
		run.UpdatedAt, _ = parseTimestamp(updatedAtStr)

		if idempotencyKey.Valid {
			run.IdempotencyKey = &idempotencyKey.String
		}
		if leasedBy.Valid {
			run.LeasedBy = &leasedBy.String
		}
		if leaseUntilStr.Valid {
			t, _ := parseTimestamp(leaseUntilStr.String)
			run.LeaseUntil = &t
		}
		if lastError.Valid {
			run.LastError = &lastError.String
		}
		if result != nil {
			run.Result = result
		}

		runs = append(runs, run)
	}

	return runs, rows.Err()
}

// DB returns the underlying database connection.
// Useful for the REST API layer that needs to pass the db to handler functions.
func (c *Client) DB() *sql.DB {
	return c.db
}

// Dialect returns the dialect used by this client.
func (c *Client) Dialect() Dialect {
	return c.dialect
}

// parseTimestamp parses a timestamp string from either PostgreSQL or SQLite format.
func parseTimestamp(s string) (time.Time, error) {
	// Try RFC3339 first (PostgreSQL with timezone)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	// SQLite datetime('now') format: "2006-01-02 15:04:05"
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC(), nil
	}
	// SQLite with fractional seconds
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp: %q", s)
}
