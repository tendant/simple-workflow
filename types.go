package simpleworkflow

import (
	"context"
	"time"
)

// Intent represents a workflow intent to be executed
type Intent struct {
	Name           string      // e.g. "content.thumbnail.v1"
	Payload        interface{} // Will be JSON-encoded
	Priority       int         // Lower executes first (default: 100)
	RunAfter       time.Time   // Schedule for future (default: now)
	IdempotencyKey string      // Optional: deduplication key
	MaxAttempts    int         // Default: 3
}

// WorkflowIntent represents a claimed intent from the database
type WorkflowIntent struct {
	ID           string
	Name         string
	Payload      []byte // JSON
	AttemptCount int
	MaxAttempts  int
}

// WorkflowExecutor is implemented by users to execute specific workflows
type WorkflowExecutor interface {
	// Execute runs the workflow and returns the result or error
	Execute(ctx context.Context, intent *WorkflowIntent) (interface{}, error)
}
