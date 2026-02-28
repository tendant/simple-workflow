package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// dbHelper holds a shared database handle and dialect, plus common helper methods.
type dbHelper struct {
	db      *sql.DB
	dialect Dialect
}

// rewrite converts $N placeholders to ? for SQLite, or returns as-is for PostgreSQL.
func (h *dbHelper) rewrite(query string) string {
	if h.dialect.DriverName() == "postgres" {
		return query
	}
	return RewritePlaceholders(query)
}

// logEvent inserts an audit event (best-effort, errors are ignored).
func (h *dbHelper) logEvent(ctx context.Context, workflowID, eventType string, data map[string]interface{}) {
	var dataJSON []byte
	var err error
	if data != nil {
		dataJSON, err = json.Marshal(data)
		if err != nil {
			return
		}
	}

	query := h.rewrite(`
		INSERT INTO workflow_event (workflow_id, event_type, data)
		VALUES ($1, $2, $3)
	`)

	eventCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, _ = h.db.ExecContext(eventCtx, query, workflowID, eventType, dataJSON)
}

// parseTimestamp parses a timestamp string from either PostgreSQL or SQLite format.
func parseTimestamp(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05+00:00",
		"2006-01-02 15:04:05 +0000 UTC",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05+00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp: %q", s)
}

// scanWorkflowRun scans a single row into a WorkflowRunStatus.
// The row must have columns: id, type, payload, status, priority, run_at,
// idempotency_key, attempt, max_attempts, leased_by, lease_until, last_error, result,
// created_at, updated_at.
func scanWorkflowRun(scanner interface{ Scan(dest ...interface{}) error }) (*WorkflowRunStatus, error) {
	var run WorkflowRunStatus
	var idempotencyKey, leasedBy, lastError sql.NullString
	var leaseUntilStr sql.NullString
	var runAtStr, createdAtStr, updatedAtStr string
	var result []byte

	err := scanner.Scan(
		&run.ID, &run.Type, &run.Payload, &run.Status, &run.Priority, &runAtStr,
		&idempotencyKey, &run.Attempt, &run.MaxAttempts,
		&leasedBy, &leaseUntilStr, &lastError, &result,
		&createdAtStr, &updatedAtStr,
	)
	if err != nil {
		return nil, err
	}

	run.RunAt, _ = parseTimestamp(runAtStr)
	run.CreatedAt, _ = parseTimestamp(createdAtStr)
	run.UpdatedAt, _ = parseTimestamp(updatedAtStr)

	if idempotencyKey.Valid {
		run.IdempotencyKey = &idempotencyKey.String
	}
	if leasedBy.Valid {
		run.LeasedBy = &leasedBy.String
	}
	if leaseUntilStr.Valid {
		t, _ := parseTimestamp(leaseUntilStr.String)
		run.LeaseUntil = &t
	}
	if lastError.Valid {
		run.LastError = &lastError.String
	}
	if result != nil {
		run.Result = result
	}

	return &run, nil
}

// requireOneRow checks that an exec result affected exactly one row.
func requireOneRow(result sql.Result, notFoundMsg string) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("%s", notFoundMsg)
	}
	return nil
}

// ---------------------------------------------------------------------------
// RunRepository
// ---------------------------------------------------------------------------

// RunRepository encapsulates all data access for the workflow_run table.
type RunRepository struct {
	dbHelper
	tx *sql.Tx
}

// NewRunRepository creates a RunRepository from a db and dialect.
func NewRunRepository(db *sql.DB, dialect Dialect) *RunRepository {
	return &RunRepository{dbHelper: dbHelper{db: db, dialect: dialect}}
}

// WithTx returns a copy of the repository that executes queries within the given transaction.
func (r *RunRepository) WithTx(tx *sql.Tx) *RunRepository {
	return &RunRepository{dbHelper: r.dbHelper, tx: tx}
}

// queryer returns the tx if set, otherwise the db.
func (r *RunRepository) queryer() interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
} {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

// Create inserts a new workflow run and returns the ID. Returns "" if idempotency conflict.
func (r *RunRepository) Create(ctx context.Context, intent Intent) (string, error) {
	payloadJSON, err := json.Marshal(intent.Payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	runID := uuid.New().String()
	priority := intent.Priority
	if priority == 0 {
		priority = 100
	}
	runAfter := intent.RunAfter
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	maxAttempts := intent.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 3
	}

	query := r.rewrite(`
		INSERT INTO workflow_run (
			id, type, payload, priority, run_at,
			idempotency_key, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`)

	var returnedID string
	err = r.queryer().QueryRowContext(ctx, query,
		runID, intent.Type, payloadJSON, priority, runAfter,
		sql.NullString{String: intent.IdempotencyKey, Valid: intent.IdempotencyKey != ""},
		maxAttempts,
	).Scan(&returnedID)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to insert workflow run: %w", err)
	}

	r.logEvent(ctx, returnedID, "created", nil)
	return returnedID, nil
}

// Cancel marks a workflow run as cancelled.
func (r *RunRepository) Cancel(ctx context.Context, runID string) error {
	query := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_run
		SET status = 'cancelled', updated_at = %s
		WHERE id = $1 AND status IN ('pending', 'leased')
	`, r.dialect.Now()))

	result, err := r.queryer().ExecContext(ctx, query, runID)
	if err != nil {
		return fmt.Errorf("failed to cancel workflow run: %w", err)
	}
	if err := requireOneRow(result, "workflow run not found or already completed"); err != nil {
		return err
	}

	r.logEvent(ctx, runID, "cancelled", nil)
	return nil
}

// Get retrieves a single workflow run by ID.
func (r *RunRepository) Get(ctx context.Context, id string) (*WorkflowRunStatus, error) {
	query := r.rewrite(`
		SELECT id, type, payload, status, priority, run_at,
			   idempotency_key, attempt, max_attempts,
			   leased_by, lease_until, last_error, result,
			   created_at, updated_at
		FROM workflow_run
		WHERE id = $1 AND deleted_at IS NULL
	`)

	run, err := scanWorkflowRun(r.queryer().QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow run: %w", err)
	}
	return run, nil
}

// List returns workflow runs matching the given options.
func (r *RunRepository) List(ctx context.Context, opts ListOptions) ([]WorkflowRunStatus, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}

	query := "SELECT id, type, payload, status, priority, run_at, idempotency_key, attempt, max_attempts, leased_by, lease_until, last_error, result, created_at, updated_at FROM workflow_run WHERE deleted_at IS NULL"
	var args []interface{}
	argN := 1

	if opts.Type != "" {
		query += fmt.Sprintf(" AND type = %s", r.dialect.Placeholder(argN))
		args = append(args, opts.Type)
		argN++
	}
	if opts.Status != "" {
		query += fmt.Sprintf(" AND status = %s", r.dialect.Placeholder(argN))
		args = append(args, opts.Status)
		argN++
	}

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT %s OFFSET %s", r.dialect.Placeholder(argN), r.dialect.Placeholder(argN+1))
	args = append(args, opts.Limit, opts.Offset)

	rows, err := r.queryer().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow runs: %w", err)
	}
	defer rows.Close()

	var runs []WorkflowRunStatus
	for rows.Next() {
		run, err := scanWorkflowRun(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan workflow run: %w", err)
		}
		runs = append(runs, *run)
	}

	return runs, rows.Err()
}

// Claim atomically claims a pending workflow run. Returns nil if no work is available.
func (r *RunRepository) Claim(ctx context.Context, workerID string, typePrefixes []string, leaseDuration time.Duration) (*WorkflowRun, error) {
	leaseSec := int(leaseDuration.Seconds())

	var args []interface{}
	var typeCondition string

	if r.dialect.DriverName() == "postgres" {
		args = []interface{}{
			workerID,
			fmt.Sprintf("%d seconds", leaseSec),
		}
		if len(typePrefixes) > 0 {
			likes := make([]string, len(typePrefixes))
			for i, prefix := range typePrefixes {
				paramIdx := i + 3
				likes[i] = fmt.Sprintf("type LIKE $%d", paramIdx)
				args = append(args, prefix)
			}
			typeCondition = " AND (" + strings.Join(likes, " OR ") + ")"
		}
	} else {
		args = []interface{}{workerID}
		if len(typePrefixes) > 0 {
			likes := make([]string, len(typePrefixes))
			for i, prefix := range typePrefixes {
				likes[i] = "type LIKE ?"
				args = append(args, prefix)
			}
			typeCondition = " AND (" + strings.Join(likes, " OR ") + ")"
		}
	}

	query := r.dialect.ClaimRunQuery(typeCondition, leaseSec)

	var run WorkflowRun
	err := r.queryer().QueryRowContext(ctx, query, args...).Scan(
		&run.ID, &run.Type, &run.Payload,
		&run.Attempt, &run.MaxAttempts,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.logEvent(ctx, run.ID, "leased", map[string]interface{}{
		"worker_id": workerID,
		"attempt":   run.Attempt,
	})

	return &run, nil
}

// MarkSucceeded marks a workflow run as succeeded with the given result.
func (r *RunRepository) MarkSucceeded(ctx context.Context, runID string, result interface{}) error {
	resultJSON, _ := json.Marshal(result)

	query := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_run
		SET status = 'succeeded',
			result = $1,
			updated_at = %s
		WHERE id = $2
	`, r.dialect.Now()))
	_, err := r.queryer().ExecContext(ctx, query, resultJSON, runID)
	if err != nil {
		return fmt.Errorf("failed to mark workflow run %s as succeeded: %w", runID, err)
	}

	r.logEvent(ctx, runID, "succeeded", nil)
	return nil
}

// MarkFailed marks a workflow run as failed or retries it. Returns the new status.
func (r *RunRepository) MarkFailed(ctx context.Context, run *WorkflowRun, execErr error) string {
	newAttempt := run.Attempt + 1
	status := "pending"

	baseDelay := time.Duration(newAttempt*newAttempt) * time.Minute
	jitter := time.Duration(float64(baseDelay) * 0.1)
	runAt := time.Now().Add(baseDelay + jitter)

	if newAttempt >= run.MaxAttempts {
		status = "failed"
	}

	query := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_run
		SET status = $1,
			attempt = $2,
			run_at = $3,
			last_error = $4,
			updated_at = %s
		WHERE id = $5
	`, r.dialect.Now()))
	_, err := r.queryer().ExecContext(ctx, query, status, newAttempt, runAt, execErr.Error(), run.ID)
	if err != nil {
		log.Printf("Failed to mark workflow run %s as failed: %v", run.ID, err)
	} else {
		log.Printf("Workflow run %s failed (attempt %d/%d): %v", run.ID, newAttempt, run.MaxAttempts, execErr)
	}

	eventType := "retried"
	if status == "failed" {
		eventType = "failed"
	}
	r.logEvent(ctx, run.ID, eventType, map[string]interface{}{
		"attempt": newAttempt,
		"error":   execErr.Error(),
	})

	return status
}

// CountPending counts pending runs matching a type prefix.
func (r *RunRepository) CountPending(ctx context.Context, typePrefix string) (int, error) {
	var depth int
	err := r.queryer().QueryRowContext(ctx,
		r.rewrite(`SELECT COUNT(*) FROM workflow_run
		 WHERE type LIKE $1 AND status = 'pending' AND deleted_at IS NULL`),
		typePrefix,
	).Scan(&depth)
	return depth, err
}

// ExtendLease extends the lease on a workflow run.
func (r *RunRepository) ExtendLease(ctx context.Context, runID string, duration time.Duration) error {
	sec := int(duration.Seconds())
	var query string
	var args []interface{}
	if r.dialect.DriverName() == "postgres" {
		query = `
			UPDATE workflow_run
			SET lease_until = NOW() + $1::interval,
				updated_at = NOW()
			WHERE id = $2 AND status = 'leased'
		`
		args = []interface{}{fmt.Sprintf("%d seconds", sec), runID}
	} else {
		query = fmt.Sprintf(`
			UPDATE workflow_run
			SET lease_until = %s,
				updated_at = %s
			WHERE id = ? AND status = 'leased'
		`, r.dialect.TimestampAfterNow(sec), r.dialect.Now())
		args = []interface{}{runID}
	}
	result, err := r.queryer().ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to extend lease: %w", err)
	}
	if err := requireOneRow(result, "workflow run not found or not in leased state"); err != nil {
		return err
	}

	r.logEvent(ctx, runID, "heartbeat", map[string]interface{}{
		"extended_by_seconds": int(duration.Seconds()),
	})
	return nil
}

// CheckCancelled checks if a workflow run has been cancelled.
func (r *RunRepository) CheckCancelled(ctx context.Context, runID string) (bool, error) {
	var status string
	query := r.rewrite(`SELECT status FROM workflow_run WHERE id = $1`)
	err := r.queryer().QueryRowContext(ctx, query, runID).Scan(&status)
	if err != nil {
		return false, fmt.Errorf("failed to check cancellation status: %w", err)
	}
	return status == "cancelled", nil
}

// CreateFromSchedule inserts a workflow run from a schedule (within a transaction).
// Returns the created run ID, or "" if deduplicated by idempotency key.
func (r *RunRepository) CreateFromSchedule(ctx context.Context, typ string, payload json.RawMessage, priority, maxAttempts int, idempotencyKey string) (string, error) {
	runID := uuid.New().String()
	now := r.dialect.Now()

	insertQuery := r.rewrite(fmt.Sprintf(`
		INSERT INTO workflow_run (
			id, type, payload, priority, run_at, idempotency_key, max_attempts
		) VALUES ($1, $2, $3, $4, %s, $5, $6)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id
	`, now))

	var returnedID sql.NullString
	err := r.queryer().QueryRowContext(ctx, insertQuery,
		runID, typ, payload, priority, idempotencyKey, maxAttempts,
	).Scan(&returnedID)

	if err != nil && err != sql.ErrNoRows {
		return "", fmt.Errorf("failed to insert workflow_run for schedule: %w", err)
	}

	if returnedID.Valid {
		return returnedID.String, nil
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// ScheduleRepository
// ---------------------------------------------------------------------------

// ScheduleRepository encapsulates all data access for the workflow_schedule table.
type ScheduleRepository struct {
	dbHelper
	tx *sql.Tx
}

// NewScheduleRepository creates a ScheduleRepository from a db and dialect.
func NewScheduleRepository(db *sql.DB, dialect Dialect) *ScheduleRepository {
	return &ScheduleRepository{dbHelper: dbHelper{db: db, dialect: dialect}}
}

// WithTx returns a copy of the repository that executes queries within the given transaction.
func (r *ScheduleRepository) WithTx(tx *sql.Tx) *ScheduleRepository {
	return &ScheduleRepository{dbHelper: r.dbHelper, tx: tx}
}

// queryer returns the tx if set, otherwise the db.
func (r *ScheduleRepository) queryer() interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
} {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

// Create validates and inserts a new schedule. Returns the schedule ID.
func (r *ScheduleRepository) Create(ctx context.Context, workflowType, cronExpr, timezone string, payload interface{}, priority, maxAttempts int) (string, error) {
	if cronExpr == "" {
		return "", fmt.Errorf("cron expression is required")
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(cronExpr)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}

	now := time.Now().In(loc)
	nextRun := sched.Next(now)

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	id := uuid.New().String()

	query := r.rewrite(`
		INSERT INTO workflow_schedule (
			id, type, payload, schedule, timezone, next_run_at,
			priority, max_attempts
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`)

	var returnedID string
	err = r.queryer().QueryRowContext(ctx, query,
		id, workflowType, payloadJSON, cronExpr, timezone, nextRun,
		priority, maxAttempts,
	).Scan(&returnedID)
	if err != nil {
		return "", fmt.Errorf("failed to create schedule: %w", err)
	}

	return returnedID, nil
}

// SetEnabled enables or disables a schedule.
func (r *ScheduleRepository) SetEnabled(ctx context.Context, scheduleID string, enabled bool) error {
	query := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET enabled = $1, updated_at = %s
		WHERE id = $2 AND deleted_at IS NULL
	`, r.dialect.Now()))
	result, err := r.queryer().ExecContext(ctx, query, enabled, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to update schedule: %w", err)
	}
	return requireOneRow(result, "schedule not found")
}

// SoftDelete soft-deletes a schedule.
func (r *ScheduleRepository) SoftDelete(ctx context.Context, scheduleID string) error {
	now := r.dialect.Now()
	query := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET deleted_at = %s, enabled = false, updated_at = %s
		WHERE id = $1 AND deleted_at IS NULL
	`, now, now))
	result, err := r.queryer().ExecContext(ctx, query, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}
	return requireOneRow(result, "schedule not found")
}

// List returns all active (non-deleted) schedules.
func (r *ScheduleRepository) List(ctx context.Context) ([]Schedule, error) {
	query := `
		SELECT id, type, payload, schedule, timezone,
			   next_run_at, last_run_at, enabled, priority, max_attempts
		FROM workflow_schedule
		WHERE deleted_at IS NULL
		ORDER BY created_at ASC
	`

	rows, err := r.queryer().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		var payloadJSON []byte
		var nextRunAtStr string
		var lastRunAtStr sql.NullString

		err := rows.Scan(
			&s.ID, &s.Type, &payloadJSON, &s.CronExpr, &s.Timezone,
			&nextRunAtStr, &lastRunAtStr, &s.Enabled, &s.Priority, &s.MaxAttempts,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}

		s.NextRunAt, _ = parseTimestamp(nextRunAtStr)
		if lastRunAtStr.Valid {
			t, _ := parseTimestamp(lastRunAtStr.String)
			s.LastRunAt = &t
		}

		s.Payload = json.RawMessage(payloadJSON)

		schedules = append(schedules, s)
	}

	return schedules, rows.Err()
}

// DueSchedule holds the data for a schedule that is due to fire.
type DueSchedule struct {
	ID          string
	Type        string
	Payload     json.RawMessage
	CronExpr    string
	Timezone    string
	NextRunAt   time.Time
	Priority    int
	MaxAttempts int
}

// ClaimDue returns schedules that are due to fire (within a transaction).
func (r *ScheduleRepository) ClaimDue(ctx context.Context) ([]DueSchedule, error) {
	rows, err := r.queryer().QueryContext(ctx, r.dialect.ClaimSchedulesQuery())
	if err != nil {
		return nil, fmt.Errorf("failed to query due schedules: %w", err)
	}
	defer rows.Close()

	var due []DueSchedule
	for rows.Next() {
		var s DueSchedule
		var nextRunAtStr string
		if err := rows.Scan(&s.ID, &s.Type, &s.Payload, &s.CronExpr, &s.Timezone, &nextRunAtStr, &s.Priority, &s.MaxAttempts); err != nil {
			return nil, fmt.Errorf("failed to scan schedule: %w", err)
		}
		s.NextRunAt, _ = parseTimestamp(nextRunAtStr)
		due = append(due, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating schedules: %w", err)
	}
	return due, nil
}

// AdvanceNextRun updates a schedule's next run time and last run time.
func (r *ScheduleRepository) AdvanceNextRun(ctx context.Context, scheduleID string, nextRun time.Time) error {
	now := r.dialect.Now()
	updateQuery := r.rewrite(fmt.Sprintf(`
		UPDATE workflow_schedule
		SET next_run_at = $1, last_run_at = %s, updated_at = %s
		WHERE id = $2
	`, now, now))
	_, err := r.queryer().ExecContext(ctx, updateQuery, nextRun, scheduleID)
	if err != nil {
		return fmt.Errorf("failed to update schedule %s: %w", scheduleID, err)
	}
	return nil
}
