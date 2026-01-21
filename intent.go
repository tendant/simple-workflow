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

// NewClient creates a new client for managing workflow runs
func NewClient(db *sql.DB) *Client {
	return &Client{db: db}
}

// Create inserts a new workflow run
func (c *Client) Create(ctx context.Context, intent Intent) (string, error) {
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
		INSERT INTO workflow.workflow_run (
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
		UPDATE workflow.workflow_run
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
		INSERT INTO workflow.workflow_event (workflow_id, event_type, data)
		VALUES ($1, $2, $3)
	`

	// Use a short timeout for event logging to avoid blocking
	eventCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, _ = c.db.ExecContext(eventCtx, query, workflowID, eventType, dataJSON)
}
