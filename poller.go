package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// PollerConfig configures the workflow poller
type PollerConfig struct {
	TypePrefixes  []string      // e.g. ["billing.%", "media.%"] for type-prefix routing
	LeaseDuration time.Duration // Lease duration (default: 30s)
	PollInterval  time.Duration // Poll interval (default: 2s)
	WorkerID      string        // Worker identifier (default: "go-worker")
}

// Poller polls workflow_run table and executes workflows (Worker API)
type Poller struct {
	db               *sql.DB
	typePrefixes     []string // e.g. ["billing.%", "media.%"]
	executors        map[string]WorkflowExecutor
	pollInterval     time.Duration
	leaseDuration    time.Duration
	workerID         string
	stopCh           chan struct{}
	metrics          MetricsCollector // Optional: metrics collector for observability
	startTime        time.Time        // Worker start time for uptime calculation
	ownsDB           bool             // true if Poller opened the DB connection
	autoDetectPrefix bool             // true if type prefixes should be auto-detected from handlers
}

// NewPoller creates a new workflow run poller from a PostgreSQL connection string.
// Type prefixes are auto-detected from registered handlers.
// Use fluent methods to configure: WithWorkerID(), WithLeaseDuration(), etc.
//
// Example:
//   poller, err := NewPoller("postgres://user:pass@localhost/db?schema=workflow")
//   poller.HandleFunc("billing.invoice.v1", handler)
//   poller.Start(ctx)
func NewPoller(connString string) (*Poller, error) {
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

	// Generate default worker ID
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	return &Poller{
		db:               db,
		typePrefixes:     nil, // Will be auto-detected
		executors:        make(map[string]WorkflowExecutor),
		pollInterval:     2 * time.Second,  // default
		leaseDuration:    30 * time.Second, // default
		workerID:         workerID,
		stopCh:           make(chan struct{}),
		ownsDB:           true,
		autoDetectPrefix: true,
	}, nil
}

// NewPollerWithDB creates a new workflow run poller using an existing database connection.
// Use this if you already have a connection pool or want to manage connections yourself.
// The poller will NOT close the database connection.
func NewPollerWithDB(db *sql.DB) *Poller {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	return &Poller{
		db:               db,
		typePrefixes:     nil, // Will be auto-detected
		executors:        make(map[string]WorkflowExecutor),
		pollInterval:     2 * time.Second,  // default
		leaseDuration:    30 * time.Second, // default
		workerID:         workerID,
		stopCh:           make(chan struct{}),
		ownsDB:           false,
		autoDetectPrefix: true,
	}
}

// NewPollerWithConfig creates a new workflow run poller with explicit configuration.
// This is the old API kept for backward compatibility.
//
// Deprecated: Use NewPoller(connString) or NewPollerWithDB(db) with fluent configuration instead.
func NewPollerWithConfig(db *sql.DB, config PollerConfig) *Poller {
	// Set defaults
	if config.LeaseDuration == 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.PollInterval == 0 {
		config.PollInterval = 2 * time.Second
	}
	if config.WorkerID == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "unknown"
		}
		config.WorkerID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	}

	return &Poller{
		db:               db,
		typePrefixes:     config.TypePrefixes,
		executors:        make(map[string]WorkflowExecutor),
		pollInterval:     config.PollInterval,
		leaseDuration:    config.LeaseDuration,
		workerID:         config.WorkerID,
		stopCh:           make(chan struct{}),
		ownsDB:           false,
		autoDetectPrefix: len(config.TypePrefixes) == 0, // Auto-detect if not specified
	}
}

// WithWorkerID sets a custom worker ID.
// Default: hostname-pid
func (p *Poller) WithWorkerID(id string) *Poller {
	p.workerID = id
	return p
}

// WithLeaseDuration sets the lease duration for claimed workflow runs.
// Default: 30 seconds
func (p *Poller) WithLeaseDuration(d time.Duration) *Poller {
	p.leaseDuration = d
	return p
}

// WithPollInterval sets how often to poll for new workflow runs.
// Default: 2 seconds
func (p *Poller) WithPollInterval(d time.Duration) *Poller {
	p.pollInterval = d
	return p
}

// WithTypePrefixes explicitly sets type prefixes to watch.
// This overrides auto-detection from registered handlers.
// Example: WithTypePrefixes("billing.%", "notify.%")
func (p *Poller) WithTypePrefixes(prefixes ...string) *Poller {
	p.typePrefixes = prefixes
	p.autoDetectPrefix = false
	return p
}

// HandleFunc registers a function handler for a workflow type.
// The function receives the WorkflowRun and returns a result or error.
//
// Example:
//   poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
//       // Process invoice
//       return result, nil
//   })
func (p *Poller) HandleFunc(workflowType string, fn func(context.Context, *WorkflowRun) (interface{}, error)) *Poller {
	p.executors[workflowType] = &funcExecutorAdapter{fn: fn}
	return p
}

// Handle registers a WorkflowExecutor for a workflow type.
// Use this for advanced cases where you need a stateful executor.
func (p *Poller) Handle(workflowType string, executor WorkflowExecutor) *Poller {
	p.executors[workflowType] = executor
	return p
}

// RegisterExecutor registers a workflow executor for a specific workflow type.
// Deprecated: Use Handle() or HandleFunc() instead.
func (p *Poller) RegisterExecutor(workflowType string, executor WorkflowExecutor) {
	p.executors[workflowType] = executor
}

// Close closes the database connection if it was opened by NewPoller.
// If the poller was created with NewPollerWithDB, this is a no-op.
func (p *Poller) Close() error {
	if p.ownsDB && p.db != nil {
		return p.db.Close()
	}
	return nil
}

// funcExecutorAdapter adapts a simple function to the WorkflowExecutor interface.
type funcExecutorAdapter struct {
	fn func(context.Context, *WorkflowRun) (interface{}, error)
}

func (a *funcExecutorAdapter) Execute(ctx context.Context, run *WorkflowRun) (interface{}, error) {
	return a.fn(ctx, run)
}

// SetMetrics sets the metrics collector (optional, pass nil to disable metrics)
func (p *Poller) SetMetrics(m MetricsCollector) {
	p.metrics = m
	p.startTime = time.Now()
}

// Start begins polling for workflow runs
func (p *Poller) Start(ctx context.Context) {
	// Auto-detect type prefixes from registered handlers if needed
	if p.autoDetectPrefix && len(p.typePrefixes) == 0 {
		p.typePrefixes = p.detectTypePrefixes()
	}

	// Validate that at least one handler is registered
	if len(p.executors) == 0 {
		log.Fatal("No workflow handlers registered. Use Handle() or HandleFunc() to register handlers.")
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	log.Printf("Workflow poller started, watching type prefixes: %v", p.typePrefixes)

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

// detectTypePrefixes extracts type prefixes from registered handlers.
// It auto-detects common prefixes to minimize polling overhead.
func (p *Poller) detectTypePrefixes() []string {
	prefixMap := make(map[string]bool)

	for workflowType := range p.executors {
		// Skip if it's already a prefix pattern
		if strings.HasSuffix(workflowType, "%") {
			prefixMap[workflowType] = true
			continue
		}

		// Extract prefix (everything up to last dot + %)
		parts := strings.Split(workflowType, ".")
		if len(parts) > 1 {
			// Use domain prefix (e.g., "billing.invoice.v1" -> "billing.%")
			prefix := parts[0] + ".%"
			prefixMap[prefix] = true
		} else {
			// No dots, use exact match
			prefixMap[workflowType] = true
		}
	}

	// Convert map to slice
	prefixes := make([]string, 0, len(prefixMap))
	for prefix := range prefixMap {
		prefixes = append(prefixes, prefix)
	}

	return prefixes
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

	// Claim a workflow run
	run, err := p.claimRun(ctx)
	if err != nil {
		if p.metrics != nil {
			p.metrics.RecordPollError(p.workerID, classifyError(err))
		}
		log.Printf("Failed to claim workflow run: %v", err)
		return
	}
	if run == nil {
		// No work available - update queue depth metrics
		if p.metrics != nil {
			p.updateQueueDepth(ctx)
		}
		return
	}

	// Record run claim
	if p.metrics != nil {
		p.metrics.RecordIntentClaimed(run.Type, p.workerID)
	}

	log.Printf("Claimed workflow run: %s (type: %s)", run.ID, run.Type)

	// Execute the workflow
	p.executeRun(ctx, run, executionStart)
}

func (p *Poller) claimRun(ctx context.Context) (*WorkflowRun, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Build WHERE clause for type-prefix matching
	// Example: (type LIKE 'billing.%' OR type LIKE 'payment.%')
	whereClauses := ""
	if len(p.typePrefixes) > 0 {
		whereClauses = " AND ("
		for i, prefix := range p.typePrefixes {
			if i > 0 {
				whereClauses += " OR "
			}
			whereClauses += fmt.Sprintf("type LIKE '%s'", prefix)
		}
		whereClauses += ")"
	}

	// Claim using SELECT FOR UPDATE SKIP LOCKED
	query := fmt.Sprintf(`
		SELECT id, type, payload, attempt, max_attempts
		FROM workflow_run
		WHERE status = 'pending'
		  AND run_at <= NOW()
		  AND deleted_at IS NULL
		  %s
		ORDER BY priority ASC, created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, whereClauses)

	var run WorkflowRun
	err = tx.QueryRowContext(ctx, query).Scan(
		&run.ID, &run.Type, &run.Payload,
		&run.Attempt, &run.MaxAttempts,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Mark as leased
	updateQuery := `
		UPDATE workflow_run
		SET status = 'leased',
			leased_by = $1,
			lease_until = NOW() + $2::interval,
			updated_at = NOW()
		WHERE id = $3
	`
	leaseDuration := fmt.Sprintf("%d seconds", int(p.leaseDuration.Seconds()))
	_, err = tx.ExecContext(ctx, updateQuery, p.workerID, leaseDuration, run.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Attach heartbeat and cancellation check functions
	run.Heartbeat = p.makeHeartbeatFunc(run.ID)
	run.IsCancelled = p.makeCancellationCheckFunc(run.ID)

	// Log leased event (best-effort)
	p.logEvent(ctx, run.ID, "leased", map[string]interface{}{
		"worker_id": p.workerID,
		"attempt":   run.Attempt,
	})

	return &run, nil
}

func (p *Poller) executeRun(ctx context.Context, run *WorkflowRun, executionStart time.Time) {
	// Find executor for this workflow type
	executor, ok := p.executors[run.Type]
	if !ok {
		err := fmt.Errorf("no executor registered for workflow type: %s", run.Type)
		p.markRunFailed(ctx, run, err, executionStart)
		return
	}

	// Log started event (best-effort)
	p.logEvent(ctx, run.ID, "started", map[string]interface{}{
		"worker_id": p.workerID,
	})

	// Execute workflow
	result, err := executor.Execute(ctx, run)

	// Update run status
	if err != nil {
		p.markRunFailed(ctx, run, err, executionStart)
	} else {
		p.markRunSucceeded(ctx, run, result, executionStart)
	}
}

func (p *Poller) markRunSucceeded(ctx context.Context, run *WorkflowRun, result interface{}, executionStart time.Time) {
	resultJSON, _ := json.Marshal(result)

	query := `
		UPDATE workflow_run
		SET status = 'succeeded',
			result = $1,
			updated_at = NOW()
		WHERE id = $2
	`
	_, err := p.db.ExecContext(ctx, query, resultJSON, run.ID)
	if err != nil {
		log.Printf("Failed to mark workflow run %s as succeeded: %v", run.ID, err)
	} else {
		log.Printf("Workflow run %s succeeded", run.ID)
	}

	// Log succeeded event (best-effort)
	p.logEvent(ctx, run.ID, "succeeded", nil)

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)
		p.metrics.RecordIntentCompleted(run.Type, p.workerID, "succeeded", duration)
	}
}

func (p *Poller) markRunFailed(ctx context.Context, run *WorkflowRun, execErr error, executionStart time.Time) {
	newAttempt := run.Attempt + 1
	status := "pending"

	// Exponential backoff with jitter (prevents thundering herd)
	baseDelay := time.Duration(newAttempt*newAttempt) * time.Minute
	jitter := time.Duration(float64(baseDelay) * 0.1) // 10% jitter
	runAt := time.Now().Add(baseDelay + jitter)

	if newAttempt >= run.MaxAttempts {
		status = "failed" // Terminal state (was "deadletter")
	}

	query := `
		UPDATE workflow_run
		SET status = $1,
			attempt = $2,
			run_at = $3,
			last_error = $4,
			updated_at = NOW()
		WHERE id = $5
	`
	_, err := p.db.ExecContext(ctx, query, status, newAttempt, runAt, execErr.Error(), run.ID)
	if err != nil {
		log.Printf("Failed to mark workflow run %s as failed: %v", run.ID, err)
	} else {
		log.Printf("Workflow run %s failed (attempt %d/%d): %v", run.ID, newAttempt, run.MaxAttempts, execErr)
	}

	// Log event (best-effort)
	eventType := "retried"
	if status == "failed" {
		eventType = "failed"
	}
	p.logEvent(ctx, run.ID, eventType, map[string]interface{}{
		"attempt": newAttempt,
		"error":   execErr.Error(),
	})

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)

		// Record failed attempt
		p.metrics.RecordFailedAttempt(run.Type, p.workerID, newAttempt)

		// Record completion status
		p.metrics.RecordIntentCompleted(run.Type, p.workerID, status, duration)

		// Record deadletter metric if workflow permanently failed
		if status == "failed" {
			p.metrics.RecordIntentDeadletter(run.Type, p.workerID)
		}
	}
}

// updateQueueDepth queries and updates queue depth metrics for all type prefixes
func (p *Poller) updateQueueDepth(ctx context.Context) {
	if p.metrics == nil {
		return
	}

	for _, prefix := range p.typePrefixes {
		var depth int
		err := p.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM workflow_run
			 WHERE type LIKE $1 AND status = 'pending' AND deleted_at IS NULL`,
			prefix,
		).Scan(&depth)

		if err == nil {
			p.metrics.RecordQueueDepth(prefix, "pending", depth)
		}
	}
}

// makeHeartbeatFunc creates a heartbeat function for extending workflow run lease
func (p *Poller) makeHeartbeatFunc(runID string) HeartbeatFunc {
	return func(ctx context.Context, duration time.Duration) error {
		query := `
			UPDATE workflow_run
			SET lease_until = NOW() + $1::interval,
				updated_at = NOW()
			WHERE id = $2 AND status = 'leased'
		`
		durationStr := fmt.Sprintf("%d seconds", int(duration.Seconds()))
		result, err := p.db.ExecContext(ctx, query, durationStr, runID)
		if err != nil {
			return fmt.Errorf("failed to extend lease: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}

		if rowsAffected == 0 {
			return fmt.Errorf("workflow run not found or not in leased state")
		}

		// Log heartbeat event (best-effort)
		p.logEvent(ctx, runID, "heartbeat", map[string]interface{}{
			"extended_by_seconds": int(duration.Seconds()),
		})

		return nil
	}
}

// makeCancellationCheckFunc creates a function to check if workflow run has been cancelled
func (p *Poller) makeCancellationCheckFunc(runID string) CancellationCheckFunc {
	return func(ctx context.Context) (bool, error) {
		var status string
		query := `SELECT status FROM workflow_run WHERE id = $1`
		err := p.db.QueryRowContext(ctx, query, runID).Scan(&status)
		if err != nil {
			return false, fmt.Errorf("failed to check cancellation status: %w", err)
		}
		return status == "cancelled", nil
	}
}

// logEvent inserts an audit event (best-effort, errors are ignored)
func (p *Poller) logEvent(ctx context.Context, workflowID, eventType string, data map[string]interface{}) {
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

	_, _ = p.db.ExecContext(eventCtx, query, workflowID, eventType, dataJSON)
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
