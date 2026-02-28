package simpleworkflow

import (
	"context"
	"database/sql"
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
	dialect          Dialect
	runs             *RunRepository
	typePrefixes     []string // e.g. ["billing.%", "media.%"]
	executors        map[string]WorkflowExecutor
	pollInterval     time.Duration
	leaseDuration    time.Duration
	workerID         string
	stopCh           chan struct{}
	metrics          MetricsCollector // Optional: metrics collector for observability
	startTime        time.Time        // Worker start time for uptime calculation
	autoDetectPrefix bool             // true if type prefixes should be auto-detected from handlers

	// Schedule ticker (optional, enabled via WithScheduleTicker)
	scheduleTicker         *ScheduleTicker
	scheduleTickerEnabled  bool
}

// NewPoller creates a new workflow run poller from a PostgreSQL connection string.
// Type prefixes are auto-detected from registered handlers.
// Use fluent methods to configure: WithWorkerID(), WithLeaseDuration(), etc.
//
// Example:
//
//	poller, err := NewPoller("postgres://user:pass@localhost/db?schema=workflow")
//	poller.HandleFunc("billing.invoice.v1", handler)
//	poller.Start(ctx)
func NewPoller(connString string) (*Poller, error) {
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

	// Generate default worker ID
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())

	return &Poller{
		db:               db,
		dialect:          dialect,
		runs:             NewRunRepository(db, dialect),
		typePrefixes:     nil, // Will be auto-detected
		executors:        make(map[string]WorkflowExecutor),
		pollInterval:     2 * time.Second,  // default
		leaseDuration:    30 * time.Second, // default
		workerID:         workerID,
		stopCh:           make(chan struct{}),
		autoDetectPrefix: true,
	}, nil
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

// WithScheduleTicker enables the schedule ticker inside the poller.
// The ticker converts due workflow_schedule rows into workflow_run rows.
func (p *Poller) WithScheduleTicker() *Poller {
	p.scheduleTickerEnabled = true
	return p
}

// WithScheduleTickInterval sets the tick interval for the embedded schedule ticker.
// Default: 15 seconds. Only effective if WithScheduleTicker() is also called.
func (p *Poller) WithScheduleTickInterval(d time.Duration) *Poller {
	p.scheduleTickerEnabled = true
	if p.scheduleTicker == nil {
		p.scheduleTicker = newScheduleTickerFromDB(p.db, p.dialect)
	}
	p.scheduleTicker.tickInterval = d
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
//
//	poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
//	    // Process invoice
//	    return result, nil
//	})
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

// Close closes the database connection.
func (p *Poller) Close() error {
	if p.db != nil {
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

	// Start schedule ticker goroutine if enabled
	if p.scheduleTickerEnabled {
		if p.scheduleTicker == nil {
			p.scheduleTicker = newScheduleTickerFromDB(p.db, p.dialect)
		}
		go p.scheduleTicker.Start(ctx)
		log.Printf("Embedded schedule ticker started (interval: %s)", p.scheduleTicker.tickInterval)
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	log.Printf("Workflow poller started, watching type prefixes: %v", p.typePrefixes)

	for {
		select {
		case <-ticker.C:
			p.pollAndExecute(ctx)
		case <-p.stopCh:
			if p.scheduleTicker != nil {
				p.scheduleTicker.Stop()
			}
			return
		case <-ctx.Done():
			if p.scheduleTicker != nil {
				p.scheduleTicker.Stop()
			}
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
	run, err := p.runs.Claim(ctx, p.workerID, p.typePrefixes, p.leaseDuration)
	if err != nil || run == nil {
		return run, err
	}

	// Attach heartbeat and cancellation check functions
	run.Heartbeat = p.makeHeartbeatFunc(run.ID)
	run.IsCancelled = p.makeCancellationCheckFunc(run.ID)

	return run, nil
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
	p.runs.logEvent(ctx, run.ID, "started", map[string]interface{}{
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
	if err := p.runs.MarkSucceeded(ctx, run.ID, result); err != nil {
		log.Printf("%v", err)
	} else {
		log.Printf("Workflow run %s succeeded", run.ID)
	}

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)
		p.metrics.RecordIntentCompleted(run.Type, p.workerID, "succeeded", duration)
	}
}

func (p *Poller) markRunFailed(ctx context.Context, run *WorkflowRun, execErr error, executionStart time.Time) {
	status := p.runs.MarkFailed(ctx, run, execErr)

	// Record metrics
	if p.metrics != nil {
		duration := time.Since(executionStart)
		newAttempt := run.Attempt + 1

		p.metrics.RecordFailedAttempt(run.Type, p.workerID, newAttempt)
		p.metrics.RecordIntentCompleted(run.Type, p.workerID, status, duration)

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
		depth, err := p.runs.CountPending(ctx, prefix)
		if err == nil {
			p.metrics.RecordQueueDepth(prefix, "pending", depth)
		}
	}
}

// makeHeartbeatFunc creates a heartbeat function for extending workflow run lease
func (p *Poller) makeHeartbeatFunc(runID string) HeartbeatFunc {
	return func(ctx context.Context, duration time.Duration) error {
		return p.runs.ExtendLease(ctx, runID, duration)
	}
}

// makeCancellationCheckFunc creates a function to check if workflow run has been cancelled
func (p *Poller) makeCancellationCheckFunc(runID string) CancellationCheckFunc {
	return func(ctx context.Context) (bool, error) {
		return p.runs.CheckCancelled(ctx, runID)
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
		if err.Error() == "database is closed" {
			return "db_closed"
		}
		return "unknown"
	}
}
