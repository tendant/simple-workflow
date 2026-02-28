package simpleworkflow

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Client manages workflow runs in the database (Producer API)
type Client struct {
	db      *sql.DB
	dialect Dialect
	runs    *RunRepository
	sched   *ScheduleRepository
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

	return &Client{
		db:      db,
		dialect: dialect,
		runs:    NewRunRepository(db, dialect),
		sched:   NewScheduleRepository(db, dialect),
	}, nil
}

// NewClientWithDB creates a new client from an existing *sql.DB and Dialect.
// Useful for testing or when you manage the connection yourself.
func NewClientWithDB(db *sql.DB, dialect Dialect) *Client {
	return &Client{
		db:      db,
		dialect: dialect,
		runs:    NewRunRepository(db, dialect),
		sched:   NewScheduleRepository(db, dialect),
	}
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
	return c.runs.Create(ctx, intent)
}

// Cancel marks a workflow run as cancelled (cooperative cancellation)
func (c *Client) Cancel(ctx context.Context, runID string) error {
	return c.runs.Cancel(ctx, runID)
}

// Submit creates a new workflow run with fluent configuration.
// Returns a SubmitBuilder for chaining configuration methods.
//
// Example:
//
//	runID, err := client.Submit("billing.invoice.v1", payload).
//	    WithIdempotency("invoice:123").
//	    WithPriority(10).
//	    Execute(ctx)
func (c *Client) Submit(workflowType string, payload any) *SubmitBuilder {
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
	return c.runs.Get(ctx, id)
}

// ListWorkflowRuns returns workflow runs matching the given options.
func (c *Client) ListWorkflowRuns(ctx context.Context, opts ListOptions) ([]WorkflowRunStatus, error) {
	return c.runs.List(ctx, opts)
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
