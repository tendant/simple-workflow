package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	simpleworkflow "github.com/tendant/simple-workflow"
)

// NewRouter creates a chi router with all API routes registered.
// If apiKey is non-empty, all routes require X-API-Key authentication.
func NewRouter(client *simpleworkflow.Client, apiKey string) *chi.Mux {
	h := NewHandler(client)
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.SetHeader("Content-Type", "application/json"))
	r.Use(APIKeyAuth(apiKey))

	r.Route("/api/v1", func(r chi.Router) {
		// Workflows
		r.Post("/workflows", h.SubmitWorkflow)
		r.Get("/workflows", h.ListWorkflows)
		r.Get("/workflows/{id}", h.GetWorkflow)
		r.Delete("/workflows/{id}", h.CancelWorkflow)

		// Schedules
		r.Post("/schedules", h.CreateSchedule)
		r.Get("/schedules", h.ListSchedules)
		r.Patch("/schedules/{id}/pause", h.PauseSchedule)
		r.Patch("/schedules/{id}/resume", h.ResumeSchedule)
		r.Delete("/schedules/{id}", h.DeleteSchedule)

		// Health
		r.Get("/health", h.Health)
	})

	return r
}
