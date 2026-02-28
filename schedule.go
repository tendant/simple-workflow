package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ScheduleBuilder provides a fluent API for creating workflow schedules.
type ScheduleBuilder struct {
	client      *Client
	workflowType string
	payload     interface{}
	cronExpr    string
	timezone    string
	priority    int
	maxAttempts int
}

// Schedule starts building a new workflow schedule.
//
// Example:
//
//	id, err := client.Schedule("report.daily.v1", payload).
//	    Cron("0 9 * * 1").
//	    InTimezone("America/New_York").
//	    Create(ctx)
func (c *Client) Schedule(workflowType string, payload interface{}) *ScheduleBuilder {
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
	if s.cronExpr == "" {
		return "", fmt.Errorf("cron expression is required")
	}

	// Parse and validate cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(s.cronExpr)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", s.cronExpr, err)
	}

	// Load timezone
	loc, err := time.LoadLocation(s.timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", s.timezone, err)
	}

	// Compute next run time
	now := time.Now().In(loc)
	nextRun := sched.Next(now)

	payloadJSON, err := json.Marshal(s.payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	query := s.client.rewrite(`
		INSERT INTO workflow_schedule (
			type, payload, schedule, timezone, next_run_at,
			priority, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`)

	var id string
	err = s.client.db.QueryRowContext(ctx, query,
		s.workflowType, payloadJSON, s.cronExpr, s.timezone, nextRun,
		s.priority, s.maxAttempts,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("failed to create schedule: %w", err)
	}

	return id, nil
}

// PauseSchedule disables a schedule so it won't fire.
func (c *Client) PauseSchedule(ctx context.Context, scheduleID string) error {
	query := c.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET enabled = false, updated_at = %s
		WHERE id = $1 AND deleted_at IS NULL
	`, c.dialect.Now()))
	result, err := c.db.ExecContext(ctx, query, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to pause schedule: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule not found")
	}
	return nil
}

// ResumeSchedule re-enables a paused schedule.
func (c *Client) ResumeSchedule(ctx context.Context, scheduleID string) error {
	query := c.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET enabled = true, updated_at = %s
		WHERE id = $1 AND deleted_at IS NULL
	`, c.dialect.Now()))
	result, err := c.db.ExecContext(ctx, query, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to resume schedule: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule not found")
	}
	return nil
}

// DeleteSchedule soft-deletes a schedule.
func (c *Client) DeleteSchedule(ctx context.Context, scheduleID string) error {
	now := c.dialect.Now()
	query := c.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET deleted_at = %s, enabled = false, updated_at = %s
		WHERE id = $1 AND deleted_at IS NULL
	`, now, now))
	result, err := c.db.ExecContext(ctx, query, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule not found")
	}
	return nil
}

// ListSchedules returns all active (non-deleted) schedules.
func (c *Client) ListSchedules(ctx context.Context) ([]Schedule, error) {
	query := `
		SELECT id, type, payload, schedule, timezone,
			   next_run_at, last_run_at, enabled, priority, max_attempts
		FROM workflow_schedule
		WHERE deleted_at IS NULL
		ORDER BY created_at ASC
	`

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		var payloadJSON []byte
		var lastRunAt sql.NullTime

		err := rows.Scan(
			&s.ID, &s.Type, &payloadJSON, &s.CronExpr, &s.Timezone,
			&s.NextRunAt, &lastRunAt, &s.Enabled, &s.Priority, &s.MaxAttempts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}

		if lastRunAt.Valid {
			s.LastRunAt = &lastRunAt.Time
		}

		// Store raw payload JSON
		s.Payload = json.RawMessage(payloadJSON)

		schedules = append(schedules, s)
	}

	return schedules, rows.Err()
}
