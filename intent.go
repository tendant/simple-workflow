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
	db *sql.DB
}

// NewClient creates a new client from a PostgreSQL connection string.
// The connection string can include ?schema=name or ?search_path=name parameter.
//
// Examples:
//   - NewClient("postgres://user:pass@localhost/db?schema=workflow")
//   - NewClient("postgres://user:pass@localhost/db?search_path=workflow")
//   - NewClient("postgres://user:pass@localhost/db") // uses default "workflow" schema
//
// The client will manage the database connection and Close() must be called.
func NewClient(connString string) (*Client, error) {
	// Parse connection string and inject search_path if needed
	modifiedConn, _, err := ParseConnString(connString, DefaultSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Open database connection
	db, err := sql.Open("postgres", modifiedConn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &Client{db: db}, nil
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

	query := `
		INSERT INTO workflow_run (
			id, type, payload, priority, run_at,
			idempotency_key, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`

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

// Cancel marks a workflow run as cancelled (cooperative cancellation)
func (c *Client) Cancel(ctx context.Context, runID string) error {
	query := `
		UPDATE workflow_run
		SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'leased')
	`

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

	query := `
		INSERT INTO workflow_event (workflow_id, event_type, data)
		VALUES ($1, $2, $3)
	`

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
