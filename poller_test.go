package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// newTestPollerDB creates a migrated in-memory SQLite DB and returns the db + dialect.
func newTestPollerDB(t *testing.T) (*sql.DB, Dialect) {
	t.Helper()
	dialect := &SQLiteDialect{}
	db, err := dialect.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(dialect.MigrateSQL()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, dialect
}

// newTestPoller creates a Poller backed by the given db/dialect.
func newTestPoller(t *testing.T, db *sql.DB, dialect Dialect) *Poller {
	t.Helper()
	return &Poller{
		db:            db,
		dialect:       dialect,
		runs:          NewRunRepository(db, dialect),
		executors:     make(map[string]WorkflowExecutor),
		pollInterval:  50 * time.Millisecond,
		leaseDuration: 30 * time.Second,
		workerID:      "test-worker",
		stopCh:        make(chan struct{}),
	}
}

// insertPendingRun directly inserts a pending workflow run for testing claim logic.
func insertPendingRun(t *testing.T, db *sql.DB, id, workflowType string, payload interface{}) {
	t.Helper()
	payloadJSON, _ := json.Marshal(payload)
	_, err := db.Exec(
		`INSERT INTO workflow_run (id, type, payload, status, priority, run_at, max_attempts)
		 VALUES (?, ?, ?, 'pending', 100, datetime('now'), 3)`,
		id, workflowType, payloadJSON,
	)
	if err != nil {
		t.Fatalf("insert pending run: %v", err)
	}
}

func TestClaimRun(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-1", "billing.invoice.v1", map[string]string{"a": "b"})

	run, err := poller.claimRun(context.Background())
	if err != nil {
		t.Fatalf("claimRun: %v", err)
	}
	if run == nil {
		t.Fatal("claimRun returned nil, expected a run")
	}
	if run.ID != "run-1" {
		t.Errorf("run.ID = %q, want %q", run.ID, "run-1")
	}
	if run.Type != "billing.invoice.v1" {
		t.Errorf("run.Type = %q, want %q", run.Type, "billing.invoice.v1")
	}

	// Verify the run is now leased in the DB
	var status, leasedBy string
	err = db.QueryRow("SELECT status, leased_by FROM workflow_run WHERE id = ?", "run-1").Scan(&status, &leasedBy)
	if err != nil {
		t.Fatalf("query after claim: %v", err)
	}
	if status != "leased" {
		t.Errorf("status = %q, want %q", status, "leased")
	}
	if leasedBy != "test-worker" {
		t.Errorf("leased_by = %q, want %q", leasedBy, "test-worker")
	}
}

func TestClaimRunEmpty(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	run, err := poller.claimRun(context.Background())
	if err != nil {
		t.Fatalf("claimRun: %v", err)
	}
	if run != nil {
		t.Errorf("expected nil run when no work available, got %+v", run)
	}
}

func TestClaimRunWithTypePrefixes(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)
	poller.typePrefixes = []string{"billing.%"}

	insertPendingRun(t, db, "run-billing", "billing.invoice.v1", nil)
	insertPendingRun(t, db, "run-media", "media.thumbnail.v1", nil)

	run, err := poller.claimRun(context.Background())
	if err != nil {
		t.Fatalf("claimRun: %v", err)
	}
	if run == nil {
		t.Fatal("claimRun returned nil")
	}
	if run.Type != "billing.invoice.v1" {
		t.Errorf("claimed wrong type: got %q, want billing.invoice.v1", run.Type)
	}

	// Media run should still be pending
	var status string
	db.QueryRow("SELECT status FROM workflow_run WHERE id = ?", "run-media").Scan(&status)
	if status != "pending" {
		t.Errorf("media run status = %q, want %q", status, "pending")
	}
}

func TestMarkRunSucceeded(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-s", "test.v1", nil)

	run, _ := poller.claimRun(context.Background())
	if run == nil {
		t.Fatal("claimRun returned nil")
	}

	result := map[string]string{"output": "done"}
	poller.markRunSucceeded(context.Background(), run, result, time.Now())

	var status string
	var resultJSON []byte
	err := db.QueryRow("SELECT status, result FROM workflow_run WHERE id = ?", "run-s").Scan(&status, &resultJSON)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("status = %q, want %q", status, "succeeded")
	}

	var got map[string]string
	json.Unmarshal(resultJSON, &got)
	if got["output"] != "done" {
		t.Errorf("result = %v, want output=done", got)
	}
}

func TestMarkRunFailedWithRetry(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-f", "test.v1", nil)

	run, _ := poller.claimRun(context.Background())
	if run == nil {
		t.Fatal("claimRun returned nil")
	}

	poller.markRunFailed(context.Background(), run, fmt.Errorf("transient error"), time.Now())

	var status string
	var attempt int
	var lastError string
	err := db.QueryRow("SELECT status, attempt, last_error FROM workflow_run WHERE id = ?", "run-f").Scan(&status, &attempt, &lastError)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// attempt 0 -> 1, max_attempts=3, so should be retried (pending)
	if status != "pending" {
		t.Errorf("status = %q, want %q (should retry)", status, "pending")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1", attempt)
	}
	if lastError != "transient error" {
		t.Errorf("last_error = %q, want %q", lastError, "transient error")
	}
}

func TestMarkRunFailedExhausted(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-x", "test.v1", nil)
	// Set attempt to max_attempts - 1 so next failure is terminal
	db.Exec("UPDATE workflow_run SET attempt = 2 WHERE id = ?", "run-x")

	run, _ := poller.claimRun(context.Background())
	if run == nil {
		t.Fatal("claimRun returned nil")
	}

	poller.markRunFailed(context.Background(), run, fmt.Errorf("final error"), time.Now())

	var status string
	err := db.QueryRow("SELECT status FROM workflow_run WHERE id = ?", "run-x").Scan(&status)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %q, want %q (should be terminal)", status, "failed")
	}
}

func TestPollAndExecute(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	executed := make(chan string, 1)
	poller.HandleFunc("test.echo.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
		executed <- run.ID
		return map[string]string{"echoed": "true"}, nil
	})

	insertPendingRun(t, db, "run-e", "test.echo.v1", nil)

	poller.pollAndExecute(context.Background())

	select {
	case id := <-executed:
		if id != "run-e" {
			t.Errorf("executed run ID = %q, want %q", id, "run-e")
		}
	default:
		t.Fatal("handler was not called")
	}

	// Verify succeeded
	var status string
	db.QueryRow("SELECT status FROM workflow_run WHERE id = ?", "run-e").Scan(&status)
	if status != "succeeded" {
		t.Errorf("status = %q, want %q", status, "succeeded")
	}
}

func TestPollAndExecuteError(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	poller.HandleFunc("test.fail.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
		return nil, fmt.Errorf("handler error")
	})

	insertPendingRun(t, db, "run-err", "test.fail.v1", nil)

	poller.pollAndExecute(context.Background())

	var status, lastError string
	db.QueryRow("SELECT status, last_error FROM workflow_run WHERE id = ?", "run-err").Scan(&status, &lastError)
	if status != "pending" {
		t.Errorf("status = %q, want %q (should retry)", status, "pending")
	}
	if lastError != "handler error" {
		t.Errorf("last_error = %q, want %q", lastError, "handler error")
	}
}

func TestPollAndExecuteNoHandler(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)
	// Register a handler for a different type
	poller.HandleFunc("other.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
		return nil, nil
	})

	insertPendingRun(t, db, "run-nh", "test.unknown.v1", nil)

	poller.pollAndExecute(context.Background())

	// Should be marked as failed because no handler exists
	var lastError string
	db.QueryRow("SELECT last_error FROM workflow_run WHERE id = ?", "run-nh").Scan(&lastError)
	if lastError == "" {
		t.Error("expected last_error to be set for unhandled workflow type")
	}
}

func TestHeartbeat(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-hb", "test.v1", nil)

	run, _ := poller.claimRun(context.Background())
	if run == nil {
		t.Fatal("claimRun returned nil")
	}

	// Get original lease_until
	var origLease string
	db.QueryRow("SELECT lease_until FROM workflow_run WHERE id = ?", "run-hb").Scan(&origLease)

	// Extend the lease
	err := run.Heartbeat(context.Background(), 60*time.Second)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Verify lease was extended
	var newLease string
	db.QueryRow("SELECT lease_until FROM workflow_run WHERE id = ?", "run-hb").Scan(&newLease)
	if newLease == origLease {
		t.Error("lease_until was not updated by heartbeat")
	}
}

func TestCancellationCheck(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	insertPendingRun(t, db, "run-cc", "test.v1", nil)

	run, _ := poller.claimRun(context.Background())
	if run == nil {
		t.Fatal("claimRun returned nil")
	}

	// Not cancelled yet
	cancelled, err := run.IsCancelled(context.Background())
	if err != nil {
		t.Fatalf("IsCancelled: %v", err)
	}
	if cancelled {
		t.Error("expected not cancelled")
	}

	// Cancel it
	db.Exec("UPDATE workflow_run SET status = 'cancelled' WHERE id = ?", "run-cc")

	cancelled, err = run.IsCancelled(context.Background())
	if err != nil {
		t.Fatalf("IsCancelled: %v", err)
	}
	if !cancelled {
		t.Error("expected cancelled after update")
	}
}

func TestDetectTypePrefixes(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)

	poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) { return nil, nil })
	poller.HandleFunc("billing.receipt.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) { return nil, nil })
	poller.HandleFunc("media.thumbnail.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) { return nil, nil })

	prefixes := poller.detectTypePrefixes()

	prefixMap := make(map[string]bool)
	for _, p := range prefixes {
		prefixMap[p] = true
	}

	if !prefixMap["billing.%"] {
		t.Error("missing billing.% prefix")
	}
	if !prefixMap["media.%"] {
		t.Error("missing media.% prefix")
	}
	if len(prefixes) != 2 {
		t.Errorf("expected 2 prefixes, got %d: %v", len(prefixes), prefixes)
	}
}

func TestStartAndStop(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	poller := newTestPoller(t, db, dialect)
	poller.pollInterval = 10 * time.Millisecond

	executed := make(chan string, 10)
	poller.HandleFunc("test.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
		executed <- run.ID
		return nil, nil
	})

	insertPendingRun(t, db, "run-ss", "test.v1", nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Wait for execution
	select {
	case <-executed:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for execution")
	}

	cancel()
	select {
	case <-done:
		// clean shutdown
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poller to stop")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{sql.ErrNoRows, "no_rows"},
		{sql.ErrTxDone, "tx_done"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "canceled"},
		{fmt.Errorf("something else"), "unknown"},
	}
	for _, tt := range tests {
		got := classifyError(tt.err)
		if got != tt.want {
			t.Errorf("classifyError(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
