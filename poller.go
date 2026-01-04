package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
)

// Poller polls workflow_intent table and executes workflows (Worker API)
type Poller struct {
	db                 *sql.DB
	supportedWorkflows []string // e.g. ["content.thumbnail.v1"]
	executors          map[string]WorkflowExecutor
	pollInterval       time.Duration
	workerID           string
	stopCh             chan struct{}
	metrics            MetricsCollector // Optional: metrics collector for observability
	startTime          time.Time        // Worker start time for uptime calculation
}

// NewPoller creates a new intent poller for workers
func NewPoller(db *sql.DB, supportedWorkflows []string) *Poller {
	return &Poller{
		db:                 db,
		supportedWorkflows: supportedWorkflows,
		executors:          make(map[string]WorkflowExecutor),
		pollInterval:       time.Second * 2,
		workerID:           "go-worker",
		stopCh:             make(chan struct{}),
	}
}

// RegisterExecutor registers a workflow executor for a specific workflow name
func (p *Poller) RegisterExecutor(workflowName string, executor WorkflowExecutor) {
	p.executors[workflowName] = executor
}

// SetWorkerID sets the worker identifier (default: "go-worker")
func (p *Poller) SetWorkerID(workerID string) {
	p.workerID = workerID
}

// SetPollInterval sets the poll interval (default: 2 seconds)
func (p *Poller) SetPollInterval(interval time.Duration) {
	p.pollInterval = interval
}

// SetMetrics sets the metrics collector (optional, pass nil to disable metrics)
func (p *Poller) SetMetrics(m MetricsCollector) {
	p.metrics = m
	p.startTime = time.Now()
}

// Start begins polling for workflow intents
func (p *Poller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	log.Printf("Intent poller started, watching workflows: %v", p.supportedWorkflows)

	for {
		select {
		case <-ticker.C:
			p.pollAndExecute(ctx)
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the poller
func (p *Poller) Stop() {
	close(p.stopCh)
}

func (p *Poller) pollAndExecute(ctx context.Context) {
	// Record poll cycle (automatically updates uptime and last poll timestamp)
	if p.metrics != nil {
		p.metrics.RecordPollCycle(p.workerID)
	}

	executionStart := time.Now()

	// Claim an intent
	intent, err := p.claimIntent(ctx)
	if err != nil {
		if p.metrics != nil {
			p.metrics.RecordPollError(p.workerID, classifyError(err))
		}
		log.Printf("Failed to claim intent: %v", err)
		return
	}
	if intent == nil {
		// No work available - update queue depth metrics
		if p.metrics != nil {
			p.updateQueueDepth(ctx)
		}
		return
	}

	// Record intent claim
	if p.metrics != nil {
		p.metrics.RecordIntentClaimed(intent.Name, p.workerID)
	}

	log.Printf("Claimed intent: %s (name: %s)", intent.ID, intent.Name)

	// Execute the workflow
	p.executeIntent(ctx, intent, executionStart)
}

func (p *Poller) claimIntent(ctx context.Context) (*WorkflowIntent, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Claim using SELECT FOR UPDATE SKIP LOCKED
	query := `
		SELECT id, name, payload, attempt_count, max_attempts
		FROM workflow_intent
		WHERE status = 'pending'
		  AND name = ANY($1)
		  AND run_after <= NOW()
		  AND deleted_at IS NULL
		ORDER BY priority ASC, created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`

	var intent WorkflowIntent
	err = tx.QueryRowContext(ctx, query, pq.Array(p.supportedWorkflows)).Scan(
		&intent.ID, &intent.Name, &intent.Payload,
		&intent.AttemptCount, &intent.MaxAttempts,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Mark as running
	updateQuery := `
		UPDATE workflow_intent
		SET status = 'running',
			claimed_by = $1,
			lease_expires_at = NOW() + INTERVAL '5 minutes',
			updated_at = NOW()
		WHERE id = $2
	`
	_, err = tx.ExecContext(ctx, updateQuery, p.workerID, intent.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &intent, nil
}

func (p *Poller) executeIntent(ctx context.Context, intent *WorkflowIntent, executionStart time.Time) {
	// Find executor for this workflow
	executor, ok := p.executors[intent.Name]
	if !ok {
		err := fmt.Errorf("no executor registered for workflow: %s", intent.Name)
		p.markIntentFailed(ctx, intent, err, executionStart)
		return
	}

	// Execute workflow
	result, err := executor.Execute(ctx, intent)

	// Update intent status
	if err != nil {
		p.markIntentFailed(ctx, intent, err, executionStart)
	} else {
		p.markIntentSucceeded(ctx, intent, result, executionStart)
	}
}

func (p *Poller) markIntentSucceeded(ctx context.Context, intent *WorkflowIntent, result interface{}, executionStart time.Time) {
	resultJSON, _ := json.Marshal(result)

	query := `
		UPDATE workflow_intent
		SET status = 'succeeded',
			result = $1,
			updated_at = NOW()
		WHERE id = $2
	`
	_, err := p.db.ExecContext(ctx, query, resultJSON, intent.ID)
	if err != nil {
		log.Printf("Failed to mark intent %s as succeeded: %v", intent.ID, err)
	} else {
		log.Printf("Intent %s succeeded", intent.ID)
	}

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)
		p.metrics.RecordIntentCompleted(intent.Name, p.workerID, "succeeded", duration)
	}
}

func (p *Poller) markIntentFailed(ctx context.Context, intent *WorkflowIntent, execErr error, executionStart time.Time) {
	newAttemptCount := intent.AttemptCount + 1
	status := "pending"
	runAfter := time.Now().Add(time.Duration(newAttemptCount*newAttemptCount) * time.Minute) // Exponential backoff

	if newAttemptCount >= intent.MaxAttempts {
		status = "deadletter"
	}

	query := `
		UPDATE workflow_intent
		SET status = $1,
			attempt_count = $2,
			run_after = $3,
			last_error = $4,
			updated_at = NOW()
		WHERE id = $5
	`
	_, err := p.db.ExecContext(ctx, query, status, newAttemptCount, runAfter, execErr.Error(), intent.ID)
	if err != nil {
		log.Printf("Failed to mark intent %s as failed: %v", intent.ID, err)
	} else {
		log.Printf("Intent %s failed (attempt %d/%d): %v", intent.ID, newAttemptCount, intent.MaxAttempts, execErr)
	}

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)

		// Record failed attempt
		p.metrics.RecordFailedAttempt(intent.Name, p.workerID, newAttemptCount)

		// Record completion status
		p.metrics.RecordIntentCompleted(intent.Name, p.workerID, status, duration)

		// Record deadletter metric if workflow permanently failed
		if status == "deadletter" {
			p.metrics.RecordIntentDeadletter(intent.Name, p.workerID)
		}
	}
}

// updateQueueDepth queries and updates queue depth metrics for all supported workflows
func (p *Poller) updateQueueDepth(ctx context.Context) {
	if p.metrics == nil {
		return
	}

	for _, workflowName := range p.supportedWorkflows {
		var depth int
		err := p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM workflow_intent
			 WHERE name = $1 AND status = 'pending' AND deleted_at IS NULL`,
			workflowName,
		).Scan(&depth)

		if err == nil {
			p.metrics.RecordQueueDepth(workflowName, "pending", depth)
		}
	}
}

// classifyError categorizes errors for metrics tracking
func classifyError(err error) string {
	if err == nil {
		return "none"
	}
	switch err {
	case sql.ErrNoRows:
		return "no_rows"
	case sql.ErrTxDone:
		return "tx_done"
	case context.DeadlineExceeded:
		return "timeout"
	case context.Canceled:
		return "canceled"
	default:
		// Check for database errors
		if err.Error() == "database is closed" {
			return "db_closed"
		}
		return "unknown"
	}
}
