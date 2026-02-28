package simpleworkflow

import (
	"context"
)

// ScheduleBuilder provides a fluent API for creating workflow schedules.
type ScheduleBuilder struct {
	client       *Client
	workflowType string
	payload      any
	cronExpr     string
	timezone     string
	priority     int
	maxAttempts  int
}

// Schedule starts building a new workflow schedule.
//
// Example:
//
//	id, err := client.Schedule("report.daily.v1", payload).
//	    Cron("0 9 * * 1").
//	    InTimezone("America/New_York").
//	    Create(ctx)
func (c *Client) Schedule(workflowType string, payload any) *ScheduleBuilder {
	return &ScheduleBuilder{
		client:       c,
		workflowType: workflowType,
		payload:      payload,
		timezone:     "UTC",
		priority:     100,
		maxAttempts:  3,
	}
}

// Cron sets the cron expression (5-field standard format).
// Examples: "0 9 * * 1" (every Monday at 9am), "*/5 * * * *" (every 5 minutes)
func (s *ScheduleBuilder) Cron(expr string) *ScheduleBuilder {
	s.cronExpr = expr
	return s
}

// InTimezone sets the IANA timezone for the schedule.
// Default: "UTC"
func (s *ScheduleBuilder) InTimezone(tz string) *ScheduleBuilder {
	s.timezone = tz
	return s
}

// WithPriority sets the priority inherited by created runs.
// Default: 100
func (s *ScheduleBuilder) WithPriority(p int) *ScheduleBuilder {
	s.priority = p
	return s
}

// WithMaxAttempts sets the max attempts inherited by created runs.
// Default: 3
func (s *ScheduleBuilder) WithMaxAttempts(n int) *ScheduleBuilder {
	s.maxAttempts = n
	return s
}

// Create validates the cron expression, computes the next run time, and inserts the schedule.
// Returns the schedule ID.
func (s *ScheduleBuilder) Create(ctx context.Context) (string, error) {
	return s.client.sched.Create(ctx, s.workflowType, s.cronExpr, s.timezone, s.payload, s.priority, s.maxAttempts)
}

// PauseSchedule disables a schedule so it won't fire.
func (c *Client) PauseSchedule(ctx context.Context, scheduleID string) error {
	return c.sched.SetEnabled(ctx, scheduleID, false)
}

// ResumeSchedule re-enables a paused schedule.
func (c *Client) ResumeSchedule(ctx context.Context, scheduleID string) error {
	return c.sched.SetEnabled(ctx, scheduleID, true)
}

// DeleteSchedule soft-deletes a schedule.
func (c *Client) DeleteSchedule(ctx context.Context, scheduleID string) error {
	return c.sched.SoftDelete(ctx, scheduleID)
}

// ListSchedules returns all active (non-deleted) schedules.
func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return c.sched.List(ctx)
}
