package simpleworkflow

import (
	"context"
	"testing"
	"time"
)

func TestNewWorkflow(t *testing.T) {
	wf, err := New("sqlite://:memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer wf.Close()

	if wf.DB() == nil {
		t.Fatal("DB() returned nil")
	}
	if wf.Dialect() == nil {
		t.Fatal("Dialect() returned nil")
	}
}

func TestNewWithDB(t *testing.T) {
	dialect := &SQLiteDialect{}
	db, err := dialect.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	wf := NewWithDB(db, dialect)
	if wf.DB() != db {
		t.Error("DB() should return the provided db")
	}
	if wf.Dialect() != dialect {
		t.Error("Dialect() should return the provided dialect")
	}
}

func TestWorkflowAutoMigrate(t *testing.T) {
	wf, err := New("sqlite://:memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer wf.Close()

	ctx := context.Background()
	if err := wf.AutoMigrate(ctx); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Idempotent
	if err := wf.AutoMigrate(ctx); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
}

func TestWorkflowSubmitAndProcess(t *testing.T) {
	wf, err := New("sqlite://:memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer wf.Close()

	ctx := context.Background()
	if err := wf.AutoMigrate(ctx); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Register handler
	executed := make(chan string, 1)
	wf.HandleFunc("test.workflow.v1", func(ctx context.Context, run *WorkflowRun) (any, error) {
		executed <- run.ID
		return map[string]string{"status": "done"}, nil
	}).WithPollInterval(10 * time.Millisecond)

	// Submit a workflow
	runID, err := wf.Submit("test.workflow.v1", map[string]string{"key": "value"}).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if runID == "" {
		t.Fatal("Submit returned empty ID")
	}

	// Start poller in background
	done := make(chan struct{})
	go func() {
		wf.Start(ctx)
		close(done)
	}()

	// Wait for execution
	select {
	case id := <-executed:
		if id != runID {
			t.Errorf("executed run ID = %q, want %q", id, runID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow execution")
	}

	// Give the poller time to mark the run as succeeded before stopping
	time.Sleep(50 * time.Millisecond)
	wf.Stop()
	<-done

	run, err := wf.GetWorkflowRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run.Status != "succeeded" {
		t.Errorf("status = %q, want %q", run.Status, "succeeded")
	}
}

func TestWorkflowWithAutoMigrate(t *testing.T) {
	wf, err := New("sqlite://:memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer wf.Close()

	ctx := context.Background()

	// Register handler and enable auto-migrate — no explicit AutoMigrate call
	executed := make(chan string, 1)
	wf.HandleFunc("test.automigrate.v1", func(ctx context.Context, run *WorkflowRun) (any, error) {
		executed <- run.ID
		return map[string]string{"status": "done"}, nil
	}).WithAutoMigrate().WithPollInterval(10 * time.Millisecond)

	// Start poller in background (triggers migration)
	done := make(chan struct{})
	go func() {
		wf.Start(ctx)
		close(done)
	}()

	// Give migration + poller time to start
	time.Sleep(50 * time.Millisecond)

	// Submit a workflow — tables should already exist from auto-migration
	runID, err := wf.Submit("test.automigrate.v1", map[string]string{"key": "value"}).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if runID == "" {
		t.Fatal("Submit returned empty ID")
	}

	select {
	case id := <-executed:
		if id != runID {
			t.Errorf("executed run ID = %q, want %q", id, runID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow execution")
	}

	time.Sleep(50 * time.Millisecond)
	wf.Stop()
	<-done

	run, err := wf.GetWorkflowRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run.Status != "succeeded" {
		t.Errorf("status = %q, want %q", run.Status, "succeeded")
	}
}

func TestWorkflowScheduleManagement(t *testing.T) {
	wf, err := New("sqlite://:memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer wf.Close()

	ctx := context.Background()
	if err := wf.AutoMigrate(ctx); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Create a schedule
	schedID, err := wf.Schedule("test.scheduled.v1", nil).
		Cron("0 9 * * 1").
		Create(ctx)
	if err != nil {
		t.Fatalf("Schedule.Create: %v", err)
	}
	if schedID == "" {
		t.Fatal("Schedule.Create returned empty ID")
	}

	// List schedules
	schedules, err := wf.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("got %d schedules, want 1", len(schedules))
	}

	// Pause
	if err := wf.PauseSchedule(ctx, schedID); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}

	// Resume
	if err := wf.ResumeSchedule(ctx, schedID); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}

	// Delete
	if err := wf.DeleteSchedule(ctx, schedID); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	// List should be empty after delete
	schedules, err = wf.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules after delete: %v", err)
	}
	if len(schedules) != 0 {
		t.Errorf("got %d schedules after delete, want 0", len(schedules))
	}
}
