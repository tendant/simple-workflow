package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	simpleworkflow "github.com/tendant/simple-workflow"
)

// newTestRouter creates an in-memory SQLite-backed router for testing.
func newTestRouter(t *testing.T, apiKey string) *chi.Mux {
	t.Helper()
	dialect := &simpleworkflow.SQLiteDialect{}
	db, err := dialect.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	client := simpleworkflow.NewClientWithDB(db, dialect)
	if err := client.AutoMigrate(t.Context()); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return NewRouter(client, apiKey)
}

// doRequest performs an HTTP request against the router and returns the response.
func doRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// doRequestWithKey performs a request with an X-API-Key header.
func doRequestWithKey(t *testing.T, router http.Handler, method, path string, body any, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestHealth(t *testing.T) {
	router := newTestRouter(t, "")
	rr := doRequest(t, router, "GET", "/api/v1/health", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestSubmitWorkflow(t *testing.T) {
	router := newTestRouter(t, "")

	// Success
	rr := doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"type":    "test.v1",
		"payload": map[string]string{"key": "val"},
	})
	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	if created["id"] == "" {
		t.Error("expected id in response")
	}

	// Missing type → 400
	rr = doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"payload": "data",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	// Idempotency conflict → 409
	rr = doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"type":            "test.v1",
		"payload":         "data",
		"idempotency_key": "dedup-1",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("first idempotent submit: status = %d, want %d", rr.Code, http.StatusCreated)
	}
	rr = doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"type":            "test.v1",
		"payload":         "data",
		"idempotency_key": "dedup-1",
	})
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate submit: status = %d, want %d", rr.Code, http.StatusConflict)
	}
}

func TestGetWorkflow(t *testing.T) {
	router := newTestRouter(t, "")

	// Create one
	rr := doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"type":    "test.v1",
		"payload": nil,
	})
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"]

	// Get existing → 200
	rr = doRequest(t, router, "GET", "/api/v1/workflows/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Get non-existent → 404
	rr = doRequest(t, router, "GET", "/api/v1/workflows/nonexistent-id", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestListWorkflows(t *testing.T) {
	router := newTestRouter(t, "")

	// Submit two different types
	doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{"type": "billing.v1", "payload": nil})
	doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{"type": "media.v1", "payload": nil})

	// List all
	rr := doRequest(t, router, "GET", "/api/v1/workflows", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var all []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &all)
	if len(all) != 2 {
		t.Errorf("got %d workflows, want 2", len(all))
	}

	// Filter by type
	rr = doRequest(t, router, "GET", "/api/v1/workflows?type=billing.v1", nil)
	var filtered []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &filtered)
	if len(filtered) != 1 {
		t.Errorf("got %d workflows for type filter, want 1", len(filtered))
	}

	// Filter by status
	rr = doRequest(t, router, "GET", "/api/v1/workflows?status=pending", nil)
	var byStatus []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &byStatus)
	if len(byStatus) != 2 {
		t.Errorf("got %d pending workflows, want 2", len(byStatus))
	}
}

func TestCancelWorkflow(t *testing.T) {
	router := newTestRouter(t, "")

	rr := doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{"type": "test.v1", "payload": nil})
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"]

	// Cancel → 200
	rr = doRequest(t, router, "DELETE", "/api/v1/workflows/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("cancel: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Re-cancel → 500 (already completed)
	rr = doRequest(t, router, "DELETE", "/api/v1/workflows/"+id, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("re-cancel: status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestCreateSchedule(t *testing.T) {
	router := newTestRouter(t, "")

	// Success
	rr := doRequest(t, router, "POST", "/api/v1/schedules", map[string]any{
		"type": "billing.v1",
		"cron": "*/5 * * * *",
	})
	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Missing fields → 400
	rr = doRequest(t, router, "POST", "/api/v1/schedules", map[string]any{
		"type": "billing.v1",
	})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestListSchedules(t *testing.T) {
	router := newTestRouter(t, "")

	doRequest(t, router, "POST", "/api/v1/schedules", map[string]any{
		"type": "billing.v1",
		"cron": "*/5 * * * *",
	})

	rr := doRequest(t, router, "GET", "/api/v1/schedules", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var schedules []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &schedules)
	if len(schedules) != 1 {
		t.Errorf("got %d schedules, want 1", len(schedules))
	}
}

func TestPauseResumeSchedule(t *testing.T) {
	router := newTestRouter(t, "")

	rr := doRequest(t, router, "POST", "/api/v1/schedules", map[string]any{
		"type": "billing.v1",
		"cron": "*/5 * * * *",
	})
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"]

	// Pause
	rr = doRequest(t, router, "PATCH", "/api/v1/schedules/"+id+"/pause", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("pause: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Resume
	rr = doRequest(t, router, "PATCH", "/api/v1/schedules/"+id+"/resume", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("resume: status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestDeleteSchedule(t *testing.T) {
	router := newTestRouter(t, "")

	rr := doRequest(t, router, "POST", "/api/v1/schedules", map[string]any{
		"type": "billing.v1",
		"cron": "*/5 * * * *",
	})
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"]

	rr = doRequest(t, router, "DELETE", "/api/v1/schedules/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestGetWorkflowEvents(t *testing.T) {
	router := newTestRouter(t, "")

	// Submit a workflow (which creates a "created" event)
	rr := doRequest(t, router, "POST", "/api/v1/workflows", map[string]any{
		"type":    "test.v1",
		"payload": nil,
	})
	var created map[string]string
	json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"]

	// Get events
	rr = doRequest(t, router, "GET", "/api/v1/workflows/"+id+"/events", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var events []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &events)
	if len(events) < 1 {
		t.Errorf("expected at least 1 event (created), got %d", len(events))
	}
	if len(events) > 0 && events[0]["event_type"] != "created" {
		t.Errorf("first event type = %q, want %q", events[0]["event_type"], "created")
	}
}

func TestAPIKeyAuth(t *testing.T) {
	const key = "test-secret-key"

	t.Run("no key configured passes all", func(t *testing.T) {
		router := newTestRouter(t, "")
		rr := doRequest(t, router, "GET", "/api/v1/health", nil)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("correct key passes", func(t *testing.T) {
		router := newTestRouter(t, key)
		rr := doRequestWithKey(t, router, "GET", "/api/v1/health", nil, key)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("wrong key returns 401", func(t *testing.T) {
		router := newTestRouter(t, key)
		rr := doRequestWithKey(t, router, "GET", "/api/v1/health", nil, "wrong-key")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})

	t.Run("missing key returns 401", func(t *testing.T) {
		router := newTestRouter(t, key)
		rr := doRequest(t, router, "GET", "/api/v1/health", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})
}
