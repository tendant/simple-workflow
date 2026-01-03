package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Client inserts workflow intents into the database (Producer API)
type Client struct {
	db *sql.DB
}

// NewClient creates a new intent client for inserting workflow intents
func NewClient(db *sql.DB) *Client {
	return &Client{db: db}
}

// Create inserts a new workflow intent
func (c *Client) Create(ctx context.Context, intent Intent) (string, error) {
	payloadJSON, err := json.Marshal(intent.Payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	intentID := uuid.New().String()
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
		INSERT INTO workflow_intent (
			id, name, payload, priority, run_after,
			idempotency_key, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`

	var returnedID string
	err = c.db.QueryRowContext(ctx, query,
		intentID, intent.Name, payloadJSON, priority, runAfter,
		sql.NullString{String: intent.IdempotencyKey, Valid: intent.IdempotencyKey != ""},
		maxAttempts,
	).Scan(&returnedID)

	if err == sql.ErrNoRows {
		// Idempotency key conflict - intent already exists
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to insert intent: %w", err)
	}

	return returnedID, nil
}
