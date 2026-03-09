package simpleworkflow

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// ScheduleTicker converts due schedules into workflow_run rows.
// It can run standalone or be embedded in a Poller via WithScheduleTicker().
type ScheduleTicker struct {
	db           *sql.DB
	dialect      Dialect
	runs         *RunRepository
	sched        *ScheduleRepository
	tickInterval time.Duration
	stopCh       chan struct{}
	stopOnce     sync.Once
	metrics      MetricsCollector
}

// NewScheduleTicker creates a new ScheduleTicker from a connection string.
func NewScheduleTicker(connString string) (*ScheduleTicker, error) {
	dialect, dsn, err := DetectDialect(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	db, err := dialect.OpenDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &ScheduleTicker{
		db:           db,
		dialect:      dialect,
		runs:         NewRunRepository(db, dialect),
		sched:        NewScheduleRepository(db, dialect),
		tickInterval: 15 * time.Second,
		stopCh:       make(chan struct{}),
	}, nil
}

// newScheduleTickerFromDB creates a ScheduleTicker using an existing *sql.DB and Dialect (for embedding in Poller).
func newScheduleTickerFromDB(db *sql.DB, dialect Dialect) *ScheduleTicker {
	return &ScheduleTicker{
		db:           db,
		dialect:      dialect,
		runs:         NewRunRepository(db, dialect),
		sched:        NewScheduleRepository(db, dialect),
		tickInterval: 15 * time.Second,
		stopCh:       make(chan struct{}),
	}
}

// SetMetrics sets the metrics collector for the schedule ticker (optional).
func (t *ScheduleTicker) SetMetrics(m MetricsCollector) {
	t.metrics = m
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

	slog.Info("schedule ticker started", "interval", t.tickInterval)

	for {
		select {
		case <-ticker.C:
			if t.metrics != nil {
				t.metrics.RecordPollCycle("schedule-ticker")
			}
			if err := t.Tick(ctx); err != nil {
				slog.Error("schedule tick error", "error", err)
				if t.metrics != nil {
					t.metrics.RecordPollError("schedule-ticker", "tick_error")
				}
			}
		case <-t.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the ticker loop. Safe to call multiple times.
func (t *ScheduleTicker) Stop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
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

	// Create transactional views of repositories
	schedTx := t.sched.WithTx(tx)
	runsTx := t.runs.WithTx(tx)

	// Claim due schedules
	due, err := schedTx.ClaimDue(ctx)
	if err != nil {
		return err
	}

	cronParser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	for _, s := range due {
		// Generate idempotency key from schedule ID and fire time
		fireTimeUnix := s.NextRunAt.Unix()
		idempotencyKey := fmt.Sprintf("schedule:%s:%d", s.ID, fireTimeUnix)

		// Insert workflow_run with idempotency key
		returnedID, err := runsTx.CreateFromSchedule(ctx, s.Type, s.Payload, s.Priority, s.MaxAttempts, idempotencyKey)
		if err != nil {
			return fmt.Errorf("failed to insert workflow_run for schedule %s: %w", s.ID, err)
		}

		if returnedID != "" {
			slog.Info("schedule fired", "schedule_id", s.ID, "run_id", returnedID, "idempotency_key", idempotencyKey)
		}

		// Compute next fire time
		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			slog.Warn("invalid timezone, using UTC", "timezone", s.Timezone, "schedule_id", s.ID)
			loc = time.UTC
		}

		sched, err := cronParser.Parse(s.CronExpr)
		if err != nil {
			slog.Warn("invalid cron expression", "cron", s.CronExpr, "schedule_id", s.ID, "error", err)
			continue
		}

		goNow := time.Now().In(loc)
		nextRun := sched.Next(goNow).UTC() // Store in UTC for consistent SQLite datetime() comparisons

		// Update schedule with next run time
		if err := schedTx.AdvanceNextRun(ctx, s.ID, nextRun); err != nil {
			return err
		}
	}

	return tx.Commit()
}
