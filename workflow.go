package simpleworkflow

import (
	"context"
	"database/sql"
	"time"
)

// Workflow combines Client (producer) and Poller (consumer) into a single entry point
// for applications that both submit and process workflows.
type Workflow struct {
	client *Client
	poller *Poller
}

// New creates a Workflow from a connection string, sharing one DB connection
// between the client and poller.
func New(connString string) (*Workflow, error) {
	client, err := NewClient(connString)
	if err != nil {
		return nil, err
	}
	poller := NewPollerWithDB(client.DB(), client.Dialect())
	return &Workflow{client: client, poller: poller}, nil
}

// NewWithDB creates a Workflow from an existing *sql.DB and Dialect.
func NewWithDB(db *sql.DB, dialect Dialect) *Workflow {
	client := NewClientWithDB(db, dialect)
	poller := NewPollerWithDB(db, dialect)
	return &Workflow{client: client, poller: poller}
}

// --- Producer methods (delegate to Client) ---

// Submit creates a new workflow run with fluent configuration.
func (w *Workflow) Submit(workflowType string, payload any) *SubmitBuilder {
	return w.client.Submit(workflowType, payload)
}

// Cancel marks a workflow run as cancelled.
func (w *Workflow) Cancel(ctx context.Context, runID string) error {
	return w.client.Cancel(ctx, runID)
}

// GetWorkflowRun retrieves a single workflow run by ID.
func (w *Workflow) GetWorkflowRun(ctx context.Context, id string) (*WorkflowRunStatus, error) {
	return w.client.GetWorkflowRun(ctx, id)
}

// ListWorkflowRuns returns workflow runs matching the given options.
func (w *Workflow) ListWorkflowRuns(ctx context.Context, opts ListOptions) ([]WorkflowRunStatus, error) {
	return w.client.ListWorkflowRuns(ctx, opts)
}

// GetWorkflowEvents returns the audit event log for a workflow run.
func (w *Workflow) GetWorkflowEvents(ctx context.Context, runID string) ([]WorkflowEvent, error) {
	return w.client.GetWorkflowEvents(ctx, runID)
}

// --- Schedule methods (delegate to Client) ---

// Schedule starts building a new workflow schedule.
func (w *Workflow) Schedule(workflowType string, payload any) *ScheduleBuilder {
	return w.client.Schedule(workflowType, payload)
}

// PauseSchedule disables a schedule so it won't fire.
func (w *Workflow) PauseSchedule(ctx context.Context, scheduleID string) error {
	return w.client.PauseSchedule(ctx, scheduleID)
}

// ResumeSchedule re-enables a paused schedule.
func (w *Workflow) ResumeSchedule(ctx context.Context, scheduleID string) error {
	return w.client.ResumeSchedule(ctx, scheduleID)
}

// DeleteSchedule soft-deletes a schedule.
func (w *Workflow) DeleteSchedule(ctx context.Context, scheduleID string) error {
	return w.client.DeleteSchedule(ctx, scheduleID)
}

// ListSchedules returns all active (non-deleted) schedules.
func (w *Workflow) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return w.client.ListSchedules(ctx)
}

// --- Consumer methods (delegate to Poller) ---

// HandleFunc registers a function handler for a workflow type.
func (w *Workflow) HandleFunc(workflowType string, fn func(context.Context, *WorkflowRun) (any, error)) *Workflow {
	w.poller.HandleFunc(workflowType, fn)
	return w
}

// Handle registers a WorkflowExecutor for a workflow type.
func (w *Workflow) Handle(workflowType string, executor WorkflowExecutor) *Workflow {
	w.poller.Handle(workflowType, executor)
	return w
}

// --- Poller configuration (fluent, returns *Workflow) ---

// WithWorkerID sets a custom worker ID.
func (w *Workflow) WithWorkerID(id string) *Workflow {
	w.poller.WithWorkerID(id)
	return w
}

// WithLeaseDuration sets the lease duration for claimed workflow runs.
func (w *Workflow) WithLeaseDuration(d time.Duration) *Workflow {
	w.poller.WithLeaseDuration(d)
	return w
}

// WithPollInterval sets how often to poll for new workflow runs.
func (w *Workflow) WithPollInterval(d time.Duration) *Workflow {
	w.poller.WithPollInterval(d)
	return w
}

// WithTypePrefixes explicitly sets type prefixes to watch.
func (w *Workflow) WithTypePrefixes(prefixes ...string) *Workflow {
	w.poller.WithTypePrefixes(prefixes...)
	return w
}

// WithAutoMigrate enables automatic schema migration when Start() is called.
func (w *Workflow) WithAutoMigrate() *Workflow {
	w.poller.WithAutoMigrate()
	return w
}

// WithScheduleTicker enables the schedule ticker inside the poller.
func (w *Workflow) WithScheduleTicker() *Workflow {
	w.poller.WithScheduleTicker()
	return w
}

// --- Lifecycle ---

// AutoMigrate runs the embedded DDL for the current dialect.
func (w *Workflow) AutoMigrate(ctx context.Context) error {
	return w.client.AutoMigrate(ctx)
}

// Start begins polling for workflow runs. Blocks until stopped or context is cancelled.
func (w *Workflow) Start(ctx context.Context) {
	w.poller.Start(ctx)
}

// Stop stops the poller. Safe to call multiple times.
func (w *Workflow) Stop() {
	w.poller.Stop()
}

// Close closes the database connection.
func (w *Workflow) Close() error {
	return w.client.Close()
}

// DB returns the underlying database connection.
func (w *Workflow) DB() *sql.DB {
	return w.client.DB()
}

// Dialect returns the dialect used by this workflow.
func (w *Workflow) Dialect() Dialect {
	return w.client.Dialect()
}
