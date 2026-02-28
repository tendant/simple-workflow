package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	simpleworkflow "github.com/tendant/simple-workflow"
)

// Handler wraps a Client to serve REST API requests.
type Handler struct {
	client *simpleworkflow.Client
}

// NewHandler creates a new API handler.
func NewHandler(client *simpleworkflow.Client) *Handler {
	return &Handler{client: client}
}

// SubmitWorkflow handles POST /api/v1/workflows
func (h *Handler) SubmitWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type           string      `json:"type"`
		Payload        any `json:"payload"`
		Priority       int         `json:"priority,omitempty"`
		IdempotencyKey string      `json:"idempotency_key,omitempty"`
		MaxAttempts    int         `json:"max_attempts,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}

	builder := h.client.Submit(req.Type, req.Payload)
	if req.Priority > 0 {
		builder.WithPriority(req.Priority)
	}
	if req.IdempotencyKey != "" {
		builder.WithIdempotency(req.IdempotencyKey)
	}
	if req.MaxAttempts > 0 {
		builder.WithMaxAttempts(req.MaxAttempts)
	}

	runID, err := builder.Execute(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "idempotency conflict, run already exists"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": runID})
}

// ListWorkflows handles GET /api/v1/workflows
func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	opts := simpleworkflow.ListOptions{
		Type:   r.URL.Query().Get("type"),
		Status: r.URL.Query().Get("status"),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		opts.Limit, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		opts.Offset, _ = strconv.Atoi(v)
	}

	runs, err := h.client.ListWorkflowRuns(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []simpleworkflow.WorkflowRunStatus{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// GetWorkflow handles GET /api/v1/workflows/{id}
func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	run, err := h.client.GetWorkflowRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GetWorkflowEvents handles GET /api/v1/workflows/{id}/events
func (h *Handler) GetWorkflowEvents(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	events, err := h.client.GetWorkflowEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []simpleworkflow.WorkflowEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// CancelWorkflow handles DELETE /api/v1/workflows/{id}
func (h *Handler) CancelWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.client.Cancel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// CreateSchedule handles POST /api/v1/schedules
func (h *Handler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type        string      `json:"type"`
		Payload     any `json:"payload"`
		Cron        string      `json:"cron"`
		Timezone    string      `json:"timezone,omitempty"`
		Priority    int         `json:"priority,omitempty"`
		MaxAttempts int         `json:"max_attempts,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Type == "" || req.Cron == "" {
		writeError(w, http.StatusBadRequest, "type and cron are required")
		return
	}

	builder := h.client.Schedule(req.Type, req.Payload).Cron(req.Cron)
	if req.Timezone != "" {
		builder.InTimezone(req.Timezone)
	}
	if req.Priority > 0 {
		builder.WithPriority(req.Priority)
	}
	if req.MaxAttempts > 0 {
		builder.WithMaxAttempts(req.MaxAttempts)
	}

	id, err := builder.Create(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// ListSchedules handles GET /api/v1/schedules
func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.client.ListSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if schedules == nil {
		schedules = []simpleworkflow.Schedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

// PauseSchedule handles PATCH /api/v1/schedules/{id}/pause
func (h *Handler) PauseSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.client.PauseSchedule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// ResumeSchedule handles PATCH /api/v1/schedules/{id}/resume
func (h *Handler) ResumeSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.client.ResumeSchedule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// DeleteSchedule handles DELETE /api/v1/schedules/{id}
func (h *Handler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.client.DeleteSchedule(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Health handles GET /api/v1/health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	err := h.client.DB().PingContext(context.Background())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unhealthy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
