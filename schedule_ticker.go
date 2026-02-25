package simpleworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// ScheduleTicker converts due schedules into workflow_run rows.
// It can run standalone or be embedded in a Poller via WithScheduleTicker().
type ScheduleTicker struct {
	db           *sql.DB
	tickInterval time.Duration
	stopCh       chan struct{}
}

// NewScheduleTicker creates a new ScheduleTicker from a PostgreSQL connection string.
func NewScheduleTicker(connString string) (*ScheduleTicker, error) {
	modifiedConn, _, err := ParseConnString(connString, DefaultSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	db, err := sql.Open("postgres", modifiedConn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &ScheduleTicker{
		db:           db,
		tickInterval: 15 * time.Second,
		stopCh:       make(chan struct{}),
	}, nil
}

// newScheduleTickerFromDB creates a ScheduleTicker using an existing *sql.DB (for embedding in Poller).
func newScheduleTickerFromDB(db *sql.DB) *ScheduleTicker {
	return &ScheduleTicker{
		db:           db,
		tickInterval: 15 * time.Second,
		stopCh:       make(chan struct{}),
	}
}

// WithTickInterval sets how often the ticker checks for due schedules.
// Default: 15 seconds
func (t *ScheduleTicker) WithTickInterval(d time.Duration) *ScheduleTicker {
	t.tickInterval = d
	return t
}

// Start begins the schedule tick loop. It blocks until Stop() is called or ctx is cancelled.
func (t *ScheduleTicker) Start(ctx context.Context) {
	ticker := time.NewTicker(t.tickInterval)
	defer ticker.Stop()

	log.Printf("Schedule ticker started (interval: %s)", t.tickInterval)

	for {
		select {
		case <-ticker.C:
			if err := t.Tick(ctx); err != nil {
				log.Printf("Schedule tick error: %v", err)
			}
		case <-t.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the ticker loop.
func (t *ScheduleTicker) Stop() {
	close(t.stopCh)
}

// Close closes the database connection (only for standalone tickers).
func (t *ScheduleTicker) Close() error {
	if t.db != nil {
		return t.db.Close()
	}
	return nil
}

// Tick performs a single tick: finds due schedules and creates workflow_run rows.
// Exported for testing.
func (t *ScheduleTicker) Tick(ctx context.Context) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Claim due schedules with SKIP LOCKED to allow concurrent tickers
	rows, err := tx.QueryContext(ctx, `
		SELECT id, type, payload, schedule, timezone, next_run_at, priority, max_attempts
		FROM workflow_schedule
		WHERE next_run_at <= NOW()
		  AND enabled = true
		  AND deleted_at IS NULL
		FOR UPDATE SKIP LOCKED
		LIMIT 10
	`)
	if err != nil {
		return fmt.Errorf("failed to query due schedules: %w", err)
	}
	defer rows.Close()

	type dueSchedule struct {
		id          string
		typ         string
		payload     []byte
		cronExpr    string
		timezone    string
		nextRunAt   time.Time
		priority    int
		maxAttempts int
	}

	var due []dueSchedule
	for rows.Next() {
		var s dueSchedule
		if err := rows.Scan(&s.id, &s.typ, &s.payload, &s.cronExpr, &s.timezone, &s.nextRunAt, &s.priority, &s.maxAttempts); err != nil {
			return fmt.Errorf("failed to scan schedule: %w", err)
		}
		due = append(due, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating schedules: %w", err)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	for _, s := range due {
		// Generate idempotency key from schedule ID and fire time
		fireTimeUnix := s.nextRunAt.Unix()
		idempotencyKey := fmt.Sprintf("schedule:%s:%d", s.id, fireTimeUnix)

		runID := uuid.New().String()

		// Insert workflow_run with idempotency key
		var returnedID sql.NullString
		err := tx.QueryRowContext(ctx, `
			INSERT INTO workflow_run (
				id, type, payload, priority, run_at, idempotency_key, max_attempts
			) VALUES ($1, $2, $3, $4, NOW(), $5, $6)
			ON CONFLICT (idempotency_key) DO NOTHING
			RETURNING id
		`, runID, s.typ, json.RawMessage(s.payload), s.priority, idempotencyKey, s.maxAttempts,
		).Scan(&returnedID)

		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to insert workflow_run for schedule %s: %w", s.id, err)
		}

		if returnedID.Valid {
			log.Printf("Schedule %s fired: created workflow_run %s (key: %s)", s.id, returnedID.String, idempotencyKey)
		}

		// Compute next fire time
		loc, err := time.LoadLocation(s.timezone)
		if err != nil {
			log.Printf("Invalid timezone %q for schedule %s, using UTC", s.timezone, s.id)
			loc = time.UTC
		}

		sched, err := parser.Parse(s.cronExpr)
		if err != nil {
			log.Printf("Invalid cron expression %q for schedule %s: %v", s.cronExpr, s.id, err)
			continue
		}

		now := time.Now().In(loc)
		nextRun := sched.Next(now)

		// Update schedule with next run time
		_, err = tx.ExecContext(ctx, `
			UPDATE workflow_schedule
			SET next_run_at = $1, last_run_at = NOW(), updated_at = NOW()
			WHERE id = $2
		`, nextRun, s.id)
		if err != nil {
			return fmt.Errorf("failed to update schedule %s: %w", s.id, err)
		}
	}

	return tx.Commit()
}
