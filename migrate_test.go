package simpleworkflow

import (
	"context"
	"testing"
	"time"
)

func TestPollerAutoMigrate(t *testing.T) {
	dialect := &SQLiteDialect{}
	db, err := dialect.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	poller := NewPollerWithDB(db, dialect)
	ctx := context.Background()

	if err := poller.AutoMigrate(ctx); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Verify tables exist by querying them
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM workflow_run").Scan(&count); err != nil {
		t.Fatalf("workflow_run table should exist: %v", err)
	}

	// Idempotent
	if err := poller.AutoMigrate(ctx); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
}

func TestPollerWithAutoMigrate(t *testing.T) {
	dialect := &SQLiteDialect{}
	db, err := dialect.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Create poller with WithAutoMigrate — no explicit AutoMigrate call
	poller := NewPollerWithDB(db, dialect).
		WithAutoMigrate().
		WithPollInterval(10 * time.Millisecond)

	executed := make(chan string, 1)
	poller.HandleFunc("test.migrate.v1", func(ctx context.Context, run *WorkflowRun) (any, error) {
		executed <- run.ID
		return map[string]string{"status": "done"}, nil
	})

	// Submit a run directly via the DB (migration hasn't run yet, so use client)
	// We need to start poller first (which triggers migration), then submit
	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Give migration + poller time to start
	time.Sleep(50 * time.Millisecond)

	// Now submit via a client sharing the same DB
	client := NewClientWithDB(db, dialect)
	runID, err := client.Submit("test.migrate.v1", map[string]string{"key": "value"}).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
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
	poller.Stop()
	<-done
}
