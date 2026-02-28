package simpleworkflow

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func newTestRepoDB(t *testing.T) (*sql.DB, Dialect) {
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

// ---------------------------------------------------------------------------
// RunRepository
// ---------------------------------------------------------------------------

func TestRunRepoCreateAndGet(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, err := repo.Create(ctx, Intent{
		Type:    "test.run.v1",
		Payload: map[string]string{"key": "value"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty ID")
	}

	run, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if run == nil {
		t.Fatal("Get returned nil")
	}
	if run.Type != "test.run.v1" {
		t.Errorf("Type = %q, want %q", run.Type, "test.run.v1")
	}
	if run.Status != "pending" {
		t.Errorf("Status = %q, want %q", run.Status, "pending")
	}
}

func TestRunRepoCancel(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, Intent{Type: "test.cancel.v1"})

	if err := repo.Cancel(ctx, id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	run, _ := repo.Get(ctx, id)
	if run.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", run.Status, "cancelled")
	}

	// Re-cancel should return ErrAlreadyComplete
	err := repo.Cancel(ctx, id)
	if !errors.Is(err, ErrAlreadyComplete) {
		t.Errorf("re-cancel error = %v, want ErrAlreadyComplete", err)
	}
}

func TestRunRepoList(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	repo.Create(ctx, Intent{Type: "list.a.v1"})
	repo.Create(ctx, Intent{Type: "list.b.v1"})
	id3, _ := repo.Create(ctx, Intent{Type: "list.a.v1"})
	repo.Cancel(ctx, id3)

	// List all
	runs, err := repo.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != 3 {
		t.Errorf("List all: got %d, want 3", len(runs))
	}

	// Filter by type
	runs, err = repo.List(ctx, ListOptions{Type: "list.a.v1"})
	if err != nil {
		t.Fatalf("List by type: %v", err)
	}
	if len(runs) != 2 {
		t.Errorf("List by type: got %d, want 2", len(runs))
	}

	// Filter by status
	runs, err = repo.List(ctx, ListOptions{Status: "cancelled"})
	if err != nil {
		t.Fatalf("List by status: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("List by status: got %d, want 1", len(runs))
	}
}

func TestRunRepoMarkSucceeded(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, Intent{Type: "test.succeed.v1"})

	// Claim the run first
	_, err := repo.Claim(ctx, "worker-1", nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := repo.MarkSucceeded(ctx, id, map[string]string{"out": "ok"}); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	run, _ := repo.Get(ctx, id)
	if run.Status != "succeeded" {
		t.Errorf("Status = %q, want %q", run.Status, "succeeded")
	}
	if run.Result == nil {
		t.Error("Result should not be nil")
	}
}

func TestRunRepoMarkFailed(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, Intent{Type: "test.fail.v1"})

	claimed, _ := repo.Claim(ctx, "worker-1", nil, 30*time.Second)
	if claimed == nil {
		t.Fatal("Claim returned nil")
	}

	// First failure should retry (attempt 0 -> 1, max 3)
	status := repo.MarkFailed(ctx, claimed, errors.New("oops"))
	if status != "pending" {
		t.Errorf("status = %q, want %q", status, "pending")
	}

	run, _ := repo.Get(ctx, id)
	if run.Attempt != 1 {
		t.Errorf("Attempt = %d, want 1", run.Attempt)
	}

	// Exhaust attempts
	db.Exec("UPDATE workflow_run SET attempt = 2, status = 'leased' WHERE id = ?", id)
	claimed.Attempt = 2
	status = repo.MarkFailed(ctx, claimed, errors.New("final"))
	if status != "failed" {
		t.Errorf("status = %q, want %q", status, "failed")
	}
}

func TestRunRepoExtendLease(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, Intent{Type: "test.lease.v1"})
	repo.Claim(ctx, "worker-1", nil, 30*time.Second)

	if err := repo.ExtendLease(ctx, id, 60*time.Second); err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}

	// Non-leased run should return ErrNotFound
	err := repo.ExtendLease(ctx, "nonexistent", 60*time.Second)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ExtendLease non-leased error = %v, want ErrNotFound", err)
	}
}

func TestRunRepoCheckCancelled(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, Intent{Type: "test.checkcancel.v1"})

	cancelled, err := repo.CheckCancelled(ctx, id)
	if err != nil {
		t.Fatalf("CheckCancelled: %v", err)
	}
	if cancelled {
		t.Error("expected not cancelled")
	}

	repo.Cancel(ctx, id)

	cancelled, err = repo.CheckCancelled(ctx, id)
	if err != nil {
		t.Fatalf("CheckCancelled after cancel: %v", err)
	}
	if !cancelled {
		t.Error("expected cancelled after cancel")
	}
}

// ---------------------------------------------------------------------------
// ScheduleRepository
// ---------------------------------------------------------------------------

func TestScheduleRepoCreateAndList(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewScheduleRepository(db, dialect)
	ctx := context.Background()

	id, err := repo.Create(ctx, "sched.test.v1", "*/5 * * * *", "UTC", nil, 100, 3)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty ID")
	}

	schedules, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("List: got %d, want 1", len(schedules))
	}
	if schedules[0].Type != "sched.test.v1" {
		t.Errorf("Type = %q, want %q", schedules[0].Type, "sched.test.v1")
	}
}

func TestScheduleRepoSetEnabled(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewScheduleRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, "sched.enable.v1", "0 * * * *", "UTC", nil, 100, 3)

	// Disable
	if err := repo.SetEnabled(ctx, id, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	schedules, _ := repo.List(ctx)
	if schedules[0].Enabled {
		t.Error("expected disabled")
	}

	// Re-enable
	if err := repo.SetEnabled(ctx, id, true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	schedules, _ = repo.List(ctx)
	if !schedules[0].Enabled {
		t.Error("expected enabled")
	}

	// Non-existent
	err := repo.SetEnabled(ctx, "nonexistent", true)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnabled non-existent error = %v, want ErrNotFound", err)
	}
}

func TestScheduleRepoSoftDelete(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	repo := NewScheduleRepository(db, dialect)
	ctx := context.Background()

	id, _ := repo.Create(ctx, "sched.delete.v1", "0 * * * *", "UTC", nil, 100, 3)

	if err := repo.SoftDelete(ctx, id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	// Should not appear in list
	schedules, _ := repo.List(ctx)
	if len(schedules) != 0 {
		t.Errorf("List after delete: got %d, want 0", len(schedules))
	}

	// Re-delete should return ErrNotFound
	err := repo.SoftDelete(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("re-delete error = %v, want ErrNotFound", err)
	}
}

func TestScheduleRepoClaimDueAndAdvance(t *testing.T) {
	db, dialect := newTestRepoDB(t)
	schedRepo := NewScheduleRepository(db, dialect)
	runRepo := NewRunRepository(db, dialect)
	ctx := context.Background()

	id, _ := schedRepo.Create(ctx, "sched.due.v1", "* * * * *", "UTC", map[string]string{"k": "v"}, 100, 3)

	// Force next_run_at to the past
	db.Exec("UPDATE workflow_schedule SET next_run_at = datetime('now', '-1 minute') WHERE id = ?", id)

	// Begin transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	schedTx := schedRepo.WithTx(tx)
	runTx := runRepo.WithTx(tx)

	due, err := schedTx.ClaimDue(ctx)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("ClaimDue: got %d, want 1", len(due))
	}
	if due[0].Type != "sched.due.v1" {
		t.Errorf("Type = %q, want %q", due[0].Type, "sched.due.v1")
	}

	// Create a run from the schedule
	idempotencyKey := "test-key"
	runID, err := runTx.CreateFromSchedule(ctx, due[0].Type, due[0].Payload, due[0].Priority, due[0].MaxAttempts, idempotencyKey)
	if err != nil {
		t.Fatalf("CreateFromSchedule: %v", err)
	}
	if runID == "" {
		t.Fatal("CreateFromSchedule returned empty ID")
	}

	// Advance next run
	nextRun := time.Now().Add(1 * time.Minute)
	if err := schedTx.AdvanceNextRun(ctx, id, nextRun); err != nil {
		t.Fatalf("AdvanceNextRun: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify the run was created
	run, err := runRepo.Get(ctx, runID)
	if err != nil {
		t.Fatalf("Get run: %v", err)
	}
	if run == nil {
		t.Fatal("run not found after commit")
	}
	if run.Type != "sched.due.v1" {
		t.Errorf("run Type = %q, want %q", run.Type, "sched.due.v1")
	}
}
