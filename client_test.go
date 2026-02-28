package simpleworkflow

import (
	"context"
	"testing"
)

// newTestClient returns a Client backed by an in-memory SQLite database
// with all tables created.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient("sqlite://:memory:")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	if err := client.AutoMigrate(context.Background()); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return client
}

func TestSubmitAndGet(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, err := client.Submit("test.workflow.v1", map[string]string{"key": "value"}).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id == "" {
		t.Fatal("Submit returned empty ID")
	}

	run, err := client.GetWorkflowRun(ctx, id)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run == nil {
		t.Fatal("GetWorkflowRun returned nil")
	}
	if run.Status != "pending" {
		t.Errorf("status = %q, want %q", run.Status, "pending")
	}
	if run.Type != "test.workflow.v1" {
		t.Errorf("type = %q, want %q", run.Type, "test.workflow.v1")
	}
	if run.MaxAttempts != 3 {
		t.Errorf("max_attempts = %d, want 3", run.MaxAttempts)
	}
}

func TestSubmitWithOptions(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, err := client.Submit("test.priority.v1", nil).
		WithPriority(10).
		WithMaxAttempts(5).
		WithIdempotency("unique-key-1").
		Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	run, err := client.GetWorkflowRun(ctx, id)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run.Priority != 10 {
		t.Errorf("priority = %d, want 10", run.Priority)
	}
	if run.MaxAttempts != 5 {
		t.Errorf("max_attempts = %d, want 5", run.MaxAttempts)
	}
	if run.IdempotencyKey == nil || *run.IdempotencyKey != "unique-key-1" {
		t.Errorf("idempotency_key = %v, want %q", run.IdempotencyKey, "unique-key-1")
	}
}

func TestIdempotencyDedup(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id1, err := client.Submit("test.v1", nil).WithIdempotency("dedup-key").Execute(ctx)
	if err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if id1 == "" {
		t.Fatal("first Submit returned empty ID")
	}

	id2, err := client.Submit("test.v1", nil).WithIdempotency("dedup-key").Execute(ctx)
	if err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if id2 != "" {
		t.Errorf("duplicate Submit should return empty string, got %q", id2)
	}
}

func TestCancel(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	id, err := client.Submit("test.cancel.v1", nil).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := client.Cancel(ctx, id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	run, err := client.GetWorkflowRun(ctx, id)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run.Status != "cancelled" {
		t.Errorf("status = %q, want %q", run.Status, "cancelled")
	}
}

func TestCancelNonExistent(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	err := client.Cancel(ctx, "non-existent-id")
	if err == nil {
		t.Fatal("Cancel non-existent should return error")
	}
}

func TestGetNonExistent(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	run, err := client.GetWorkflowRun(ctx, "non-existent-id")
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if run != nil {
		t.Errorf("expected nil for non-existent run, got %+v", run)
	}
}

func TestListWorkflowRuns(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	// Submit a few workflows
	for i := 0; i < 3; i++ {
		_, err := client.Submit("list.test.v1", map[string]int{"i": i}).Execute(ctx)
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	_, err := client.Submit("other.type.v1", nil).Execute(ctx)
	if err != nil {
		t.Fatalf("Submit other: %v", err)
	}

	t.Run("list all", func(t *testing.T) {
		runs, err := client.ListWorkflowRuns(ctx, ListOptions{})
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 4 {
			t.Errorf("got %d runs, want 4", len(runs))
		}
	})

	t.Run("filter by type", func(t *testing.T) {
		runs, err := client.ListWorkflowRuns(ctx, ListOptions{Type: "list.test.v1"})
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 3 {
			t.Errorf("got %d runs, want 3", len(runs))
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		runs, err := client.ListWorkflowRuns(ctx, ListOptions{Status: "pending"})
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 4 {
			t.Errorf("got %d runs, want 4", len(runs))
		}
	})

	t.Run("limit and offset", func(t *testing.T) {
		runs, err := client.ListWorkflowRuns(ctx, ListOptions{Limit: 2})
		if err != nil {
			t.Fatalf("ListWorkflowRuns: %v", err)
		}
		if len(runs) != 2 {
			t.Errorf("got %d runs, want 2", len(runs))
		}
	})
}

func TestAutoMigrateIdempotent(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	// Running migrate again should not fail
	if err := client.AutoMigrate(ctx); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
}
