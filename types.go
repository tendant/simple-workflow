package simpleworkflow

import (
	"context"
	"time"
)

// Intent represents a workflow intent to be executed
type Intent struct {
	Type           string      // e.g. "content.thumbnail.v1" (renamed from Name for semantic clarity)
	Payload        interface{} // Will be JSON-encoded
	Priority       int         // Lower executes first (default: 100)
	RunAfter       time.Time   // Schedule for future (default: now)
	IdempotencyKey string      // Optional: deduplication key
	MaxAttempts    int         // Default: 3
}

// HeartbeatFunc extends the lease on a workflow run
// duration: how long to extend the lease by
type HeartbeatFunc func(ctx context.Context, duration time.Duration) error

// CancellationCheckFunc checks if the workflow run has been cancelled
// Returns true if cancelled, false otherwise
type CancellationCheckFunc func(ctx context.Context) (bool, error)

// WorkflowRun represents a claimed workflow run from the database
// (renamed from WorkflowIntent for consistency with new naming)
type WorkflowRun struct {
	ID          string
	Type        string
	Payload     []byte // JSON
	Attempt     int
	MaxAttempts int

	// Functions for long-running workflows
	Heartbeat    HeartbeatFunc          // Extend the lease to prevent timeout
	IsCancelled  CancellationCheckFunc  // Check if workflow has been cancelled
}

// WorkflowExecutor is implemented by users to execute specific workflows
type WorkflowExecutor interface {
	// Execute runs the workflow and returns the result or error
	Execute(ctx context.Context, run *WorkflowRun) (interface{}, error)
}
