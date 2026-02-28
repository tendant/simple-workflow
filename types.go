package simpleworkflow

import (
	"context"
	"time"
)

// Intent represents a workflow intent to be executed
type Intent struct {
	Type           string      // e.g. "content.thumbnail.v1" (renamed from Name for semantic clarity)
	Payload        any // Will be JSON-encoded
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
	Execute(ctx context.Context, run *WorkflowRun) (any, error)
}

// Schedule represents a recurring workflow schedule.
type Schedule struct {
	ID          string
	Type        string
	Payload     any
	CronExpr    string
	Timezone    string
	NextRunAt   time.Time
	LastRunAt   *time.Time
	Enabled     bool
	Priority    int
	MaxAttempts int
}

// WorkflowRunStatus is the full read model for a workflow run, with JSON tags for API responses.
type WorkflowRunStatus struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	Payload        []byte     `json:"payload"`
	Status         string     `json:"status"`
	Priority       int        `json:"priority"`
	RunAt          time.Time  `json:"run_at"`
	IdempotencyKey *string    `json:"idempotency_key,omitempty"`
	Attempt        int        `json:"attempt"`
	MaxAttempts    int        `json:"max_attempts"`
	LeasedBy       *string    `json:"leased_by,omitempty"`
	LeaseUntil     *time.Time `json:"lease_until,omitempty"`
	LastError      *string    `json:"last_error,omitempty"`
	Result         []byte     `json:"result,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ListOptions configures filtering and pagination for listing workflow runs.
type ListOptions struct {
	Type   string // Filter by workflow type (exact match)
	Status string // Filter by status
	Limit  int    // Max results (default: 50)
	Offset int    // Pagination offset
}
