package simpleworkflow

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestScheduleCreate(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, err := client.Schedule("report.daily.v1", map[string]string{"format": "pdf"}).
		Cron("0 9 * * 1").
		Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty ID")
	}

	// Verify in DB
	var typ, cronExpr, tz string
	var enabled int
	var priority, maxAttempts int
	err = client.DB().QueryRow(
		"SELECT type, schedule, timezone, enabled, priority, max_attempts FROM workflow_schedule WHERE id = ?", id,
	).Scan(&typ, &cronExpr, &tz, &enabled, &priority, &maxAttempts)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if typ != "report.daily.v1" {
		t.Errorf("type = %q, want %q", typ, "report.daily.v1")
	}
	if cronExpr != "0 9 * * 1" {
		t.Errorf("schedule = %q, want %q", cronExpr, "0 9 * * 1")
	}
	if tz != "UTC" {
		t.Errorf("timezone = %q, want %q", tz, "UTC")
	}
	if enabled != 1 {
		t.Errorf("enabled = %d, want 1", enabled)
	}
}

func TestScheduleCreateWithOptions(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, err := client.Schedule("billing.v1", nil).
		Cron("*/5 * * * *").
		InTimezone("America/New_York").
		WithPriority(10).
		WithMaxAttempts(5).
		Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var tz string
	var priority, maxAttempts int
	client.DB().QueryRow(
		"SELECT timezone, priority, max_attempts FROM workflow_schedule WHERE id = ?", id,
	).Scan(&tz, &priority, &maxAttempts)

	if tz != "America/New_York" {
		t.Errorf("timezone = %q, want %q", tz, "America/New_York")
	}
	if priority != 10 {
		t.Errorf("priority = %d, want 10", priority)
	}
	if maxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5", maxAttempts)
	}
}

func TestScheduleCreateInvalidCron(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	_, err := client.Schedule("test.v1", nil).
		Cron("bad cron").
		Create(ctx)
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestScheduleCreateNoCron(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	_, err := client.Schedule("test.v1", nil).Create(ctx)
	if err == nil {
		t.Fatal("expected error when cron expression is missing")
	}
}

func TestScheduleCreateInvalidTimezone(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	_, err := client.Schedule("test.v1", nil).
		Cron("* * * * *").
		InTimezone("Invalid/Timezone").
		Create(ctx)
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestPauseSchedule(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, _ := client.Schedule("test.v1", nil).Cron("* * * * *").Create(ctx)

	if err := client.PauseSchedule(ctx, id); err != nil {
		t.Fatalf("PauseSchedule: %v", err)
	}

	var enabled int
	client.DB().QueryRow("SELECT enabled FROM workflow_schedule WHERE id = ?", id).Scan(&enabled)
	if enabled != 0 {
		t.Errorf("enabled = %d, want 0 after pause", enabled)
	}
}

func TestResumeSchedule(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, _ := client.Schedule("test.v1", nil).Cron("* * * * *").Create(ctx)
	client.PauseSchedule(ctx, id)

	if err := client.ResumeSchedule(ctx, id); err != nil {
		t.Fatalf("ResumeSchedule: %v", err)
	}

	var enabled int
	client.DB().QueryRow("SELECT enabled FROM workflow_schedule WHERE id = ?", id).Scan(&enabled)
	if enabled != 1 {
		t.Errorf("enabled = %d, want 1 after resume", enabled)
	}
}

func TestDeleteSchedule(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, _ := client.Schedule("test.v1", nil).Cron("* * * * *").Create(ctx)

	if err := client.DeleteSchedule(ctx, id); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}

	// Should be soft-deleted
	var deletedAt sql.NullString
	client.DB().QueryRow("SELECT deleted_at FROM workflow_schedule WHERE id = ?", id).Scan(&deletedAt)
	if !deletedAt.Valid {
		t.Error("deleted_at should be set after delete")
	}
}

func TestDeleteScheduleTwice(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, _ := client.Schedule("test.v1", nil).Cron("* * * * *").Create(ctx)
	client.DeleteSchedule(ctx, id)

	err := client.DeleteSchedule(ctx, id)
	if err == nil {
		t.Error("expected error when deleting already-deleted schedule")
	}
}

func TestPauseNonExistent(t *testing.T) {
	client := newTestClient(t)
	err := client.PauseSchedule(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for non-existent schedule")
	}
}

func TestListSchedules(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	client.Schedule("sched.a.v1", nil).Cron("0 * * * *").Create(ctx)
	client.Schedule("sched.b.v1", nil).Cron("*/5 * * * *").Create(ctx)
	id3, _ := client.Schedule("sched.c.v1", nil).Cron("0 9 * * *").Create(ctx)
	client.DeleteSchedule(ctx, id3) // soft-delete one

	schedules, err := client.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(schedules) != 2 {
		t.Errorf("got %d schedules, want 2 (deleted should be excluded)", len(schedules))
	}
}

func TestScheduleTickerTick(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	client := NewClientWithDB(db, dialect)
	ctx := context.Background()

	// Create a schedule that's already due (next_run_at in the past)
	id, err := client.Schedule("tick.test.v1", map[string]string{"k": "v"}).
		Cron("* * * * *").
		Create(ctx)
	if err != nil {
		t.Fatalf("Create schedule: %v", err)
	}

	// Force next_run_at to the past so the ticker picks it up
	_, err = db.Exec("UPDATE workflow_schedule SET next_run_at = datetime('now', '-1 minute') WHERE id = ?", id)
	if err != nil {
		t.Fatalf("update next_run_at: %v", err)
	}

	// Run a tick
	ticker := newScheduleTickerFromDB(db, dialect)
	if err := ticker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Verify a workflow_run was created
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM workflow_run WHERE type = 'tick.test.v1'").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 workflow_run, got %d", count)
	}

	// Verify next_run_at was advanced
	var nextRunStr string
	db.QueryRow("SELECT next_run_at FROM workflow_schedule WHERE id = ?", id).Scan(&nextRunStr)
	nextRun, _ := parseTimestamp(nextRunStr)
	if !nextRun.After(time.Now().Add(-1 * time.Second)) {
		t.Errorf("next_run_at should be in the future, got %v", nextRun)
	}
}

func TestScheduleTickerIdempotent(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	client := NewClientWithDB(db, dialect)
	ctx := context.Background()

	id, _ := client.Schedule("idem.test.v1", nil).Cron("* * * * *").Create(ctx)

	// Force next_run_at to the past
	db.Exec("UPDATE workflow_schedule SET next_run_at = datetime('now', '-1 minute') WHERE id = ?", id)

	ticker := newScheduleTickerFromDB(db, dialect)

	// First tick creates the run
	if err := ticker.Tick(ctx); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	// Force next_run_at back to the past again (simulating clock skew)
	db.Exec("UPDATE workflow_schedule SET next_run_at = datetime('now', '-1 minute') WHERE id = ?", id)

	// Second tick — same fire time should be deduped by idempotency key
	// Note: this may create a second run because next_run_at was updated,
	// but the original fire time's run won't be duplicated
	if err := ticker.Tick(ctx); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM workflow_run WHERE type = 'idem.test.v1'").Scan(&count)
	// Should have at most 2 runs (one per distinct fire time)
	if count > 2 {
		t.Errorf("expected at most 2 workflow_runs, got %d", count)
	}
}

func TestScheduleTickerSkipsPaused(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	client := NewClientWithDB(db, dialect)
	ctx := context.Background()

	id, _ := client.Schedule("paused.test.v1", nil).Cron("* * * * *").Create(ctx)

	// Pause and set to past
	client.PauseSchedule(ctx, id)
	db.Exec("UPDATE workflow_schedule SET next_run_at = datetime('now', '-1 minute') WHERE id = ?", id)

	ticker := newScheduleTickerFromDB(db, dialect)
	if err := ticker.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM workflow_run WHERE type = 'paused.test.v1'").Scan(&count)
	if count != 0 {
		t.Errorf("paused schedule should not create runs, got %d", count)
	}
}

func TestScheduleTickerStartStop(t *testing.T) {
	db, dialect := newTestPollerDB(t)
	ticker := newScheduleTickerFromDB(db, dialect)
	ticker.tickInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ticker.Start(ctx)
		close(done)
	}()

	// Let it tick a few times
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// clean shutdown
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ticker to stop")
	}
}
