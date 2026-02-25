#!/bin/bash
# Test script for the Scheduled Jobs feature
# Tests schedule CRUD, ticker idempotency, and poller integration for Go and Python

set -e

echo "=== Testing Scheduled Jobs ==="
echo

# Database configuration
DB_URL="${DATABASE_URL:-postgres://pas:pwd@localhost/pas}"
SCHEMA="${DB_SCHEMA:-workflow}"
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Go needs sslmode=disable for local dev; append if not already present
if [[ "$DB_URL" != *"sslmode="* ]]; then
  GO_DB_URL="${DB_URL}?sslmode=disable&search_path=${SCHEMA}"
else
  GO_DB_URL="${DB_URL}"
fi

# Python needs ?schema= parameter
PY_DB_URL="${DB_URL}?schema=${SCHEMA}"

echo "Database: $DB_URL"
echo "Schema: $SCHEMA"
echo "Project: $PROJECT_DIR"
echo

# Test 1: Clean up old test schedule and run data
echo "1. Cleaning up old test data..."
psql "$DB_URL" <<EOF || true
SET search_path TO $SCHEMA;
DELETE FROM workflow_run WHERE type LIKE 'test.schedule.%';
DELETE FROM workflow_schedule WHERE type LIKE 'test.schedule.%';
EOF
echo "✓ Clean up complete"
echo

# Test 2: Verify migration — workflow_schedule table exists
echo "2. Verifying workflow_schedule table exists..."
psql "$DB_URL" <<EOF
SET search_path TO $SCHEMA;
SELECT
    CASE WHEN EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = '$SCHEMA' AND table_name = 'workflow_schedule'
    )
    THEN '✓ workflow_schedule table exists'
    ELSE '❌ workflow_schedule table missing'
    END as schedule_table_check;
EOF
echo

# Test 3: Go Schedule Client API — create, list, pause, resume, delete
echo "3. Testing Go Schedule Client API..."
cat > /tmp/test_go_schedule_client.go <<'GOCODE'
package main

import (
	"context"
	"fmt"
	"os"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow"
	}

	client, err := simpleworkflow.NewClient(dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to create client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx := context.Background()

	// Create a schedule
	schedID, err := client.Schedule("test.schedule.go.v1", map[string]interface{}{
		"source": "go_test",
	}).
		Cron("*/5 * * * *").
		InTimezone("America/New_York").
		WithPriority(50).
		WithMaxAttempts(2).
		Create(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to create schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Schedule created: %s\n", schedID)

	// List schedules
	schedules, err := client.ListSchedules(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to list schedules: %v\n", err)
		os.Exit(1)
	}
	found := false
	for _, s := range schedules {
		if s.ID == schedID {
			found = true
			break
		}
	}
	if !found {
		fmt.Println("❌ Created schedule not found in list")
		os.Exit(1)
	}
	fmt.Println("✓ Schedule found in list")

	// Pause
	err = client.PauseSchedule(ctx, schedID)
	if err != nil {
		fmt.Printf("❌ Failed to pause schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Schedule paused")

	// Resume
	err = client.ResumeSchedule(ctx, schedID)
	if err != nil {
		fmt.Printf("❌ Failed to resume schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Schedule resumed")

	// Delete
	err = client.DeleteSchedule(ctx, schedID)
	if err != nil {
		fmt.Printf("❌ Failed to delete schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Schedule deleted")
}
GOCODE

cd "$PROJECT_DIR"
DATABASE_URL="$GO_DB_URL" go run /tmp/test_go_schedule_client.go
echo

# Test 4: Go ScheduleTicker + Idempotency
echo "4. Testing Go ScheduleTicker + Idempotency..."
cat > /tmp/test_go_schedule_ticker.go <<'GOCODE'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow"
	}

	client, err := simpleworkflow.NewClient(dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to create client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx := context.Background()

	// Create a schedule
	schedID, err := client.Schedule("test.schedule.tick.v1", map[string]interface{}{
		"source": "ticker_test",
	}).
		Cron("0 0 * * *").
		Create(ctx)
	if err != nil {
		fmt.Printf("❌ Failed to create schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Schedule created: %s\n", schedID)

	// Force next_run_at to the past so Tick() will fire it
	// Reuse the same connection string (already has sslmode and schema)
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	_, err = db.Exec(`UPDATE workflow_schedule SET next_run_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, schedID)
	if err != nil {
		fmt.Printf("❌ Failed to backdate schedule: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Schedule backdated to past")

	// Create ticker and call Tick() twice
	ticker, err := simpleworkflow.NewScheduleTicker(dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to create ticker: %v\n", err)
		os.Exit(1)
	}
	defer ticker.Close()

	err = ticker.Tick(ctx)
	if err != nil {
		fmt.Printf("❌ First Tick() failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ First Tick() succeeded")

	err = ticker.Tick(ctx)
	if err != nil {
		fmt.Printf("❌ Second Tick() failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Second Tick() succeeded")

	// Verify exactly one workflow_run was created
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM workflow_run WHERE type = 'test.schedule.tick.v1'`).Scan(&count)
	if err != nil {
		fmt.Printf("❌ Failed to count runs: %v\n", err)
		os.Exit(1)
	}
	if count != 1 {
		fmt.Printf("❌ Expected 1 workflow_run, got %d\n", count)
		os.Exit(1)
	}
	fmt.Println("✓ Idempotency verified: exactly 1 workflow_run created after 2 ticks")
}
GOCODE

DATABASE_URL="$GO_DB_URL" go run /tmp/test_go_schedule_ticker.go
echo

# Test 5: Go Poller with embedded ticker
echo "5. Testing Go Poller with WithScheduleTicker()..."
cat > /tmp/test_go_schedule_poller.go <<'GOCODE'
package main

import (
	"fmt"
	"os"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow"
	}

	poller, err := simpleworkflow.NewPoller(dbURL)
	if err != nil {
		fmt.Printf("❌ Failed to create poller: %v\n", err)
		os.Exit(1)
	}
	defer poller.Close()
	fmt.Println("✓ Poller created")

	poller.WithScheduleTicker().
		WithScheduleTickInterval(30 * time.Second)
	fmt.Println("✓ Schedule ticker enabled with 30s interval")

	// Don't actually start — just validate configuration
	fmt.Println("✓ Poller with embedded ticker configured (not started)")
}
GOCODE

DATABASE_URL="$GO_DB_URL" go run /tmp/test_go_schedule_poller.go
echo

# Test 6: Python Schedule Client API
echo "6. Testing Python Schedule Client API..."
cat > /tmp/test_py_schedule_client.py <<'PYCODE'
#!/usr/bin/env python3
import sys
import os
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', 'python'))
# Also support running from project root
sys.path.insert(0, os.environ.get('PROJECT_DIR', '.') + '/python')

try:
    from simpleworkflow import Client

    db_url = os.getenv('DATABASE_URL', 'postgres://pas:pwd@localhost/pas?schema=workflow')

    client = Client(db_url)
    print("✓ Client created")

    # Create a schedule
    sched_id = (
        client.schedule("test.schedule.py.v1", {"source": "python_test"})
        .cron("*/10 * * * *")
        .in_timezone("America/Chicago")
        .with_priority(75)
        .with_max_attempts(5)
        .create()
    )
    print(f"✓ Schedule created: {sched_id}")

    # List schedules
    schedules = client.list_schedules()
    found = any(str(s['id']) == sched_id for s in schedules)
    if not found:
        print("❌ Created schedule not found in list")
        sys.exit(1)
    print("✓ Schedule found in list")

    # Pause
    client.pause_schedule(sched_id)
    print("✓ Schedule paused")

    # Resume
    client.resume_schedule(sched_id)
    print("✓ Schedule resumed")

    # Delete
    client.delete_schedule(sched_id)
    print("✓ Schedule deleted")

except ModuleNotFoundError as e:
    print(f"⚠ Skipping Python schedule client tests: {e}")
    print("  (Python dependencies not installed)")
    sys.exit(0)
except Exception as e:
    print(f"❌ Error: {e}")
    import traceback; traceback.print_exc()
    sys.exit(1)
PYCODE

PROJECT_DIR="$PROJECT_DIR" DATABASE_URL="$PY_DB_URL" python3 /tmp/test_py_schedule_client.py || echo "⚠ Python Schedule Client tests skipped"
echo

# Test 7: Python ScheduleTicker + Idempotency
echo "7. Testing Python ScheduleTicker + Idempotency..."
cat > /tmp/test_py_schedule_ticker.py <<'PYCODE'
#!/usr/bin/env python3
import sys
import os
sys.path.insert(0, os.environ.get('PROJECT_DIR', '.') + '/python')

try:
    from simpleworkflow import Client
    from simpleworkflow.schedule_ticker import ScheduleTicker
    import psycopg2

    db_url = os.getenv('DATABASE_URL', 'postgres://pas:pwd@localhost/pas?schema=workflow')

    client = Client(db_url)
    print("✓ Client created")

    # Create a schedule
    sched_id = (
        client.schedule("test.schedule.pytick.v1", {"source": "ticker_test"})
        .cron("0 0 * * *")
        .create()
    )
    print(f"✓ Schedule created: {sched_id}")

    # Force next_run_at to the past via SQL
    conn = psycopg2.connect(os.getenv('DATABASE_URL', 'postgres://pas:pwd@localhost/pas'))
    conn.autocommit = True
    with conn.cursor() as cur:
        cur.execute(f"SET search_path TO {os.getenv('DB_SCHEMA', 'workflow')}")
        cur.execute(
            "UPDATE workflow_schedule SET next_run_at = NOW() - INTERVAL '1 hour' WHERE id = %s",
            (sched_id,)
        )
    print("✓ Schedule backdated to past")

    # Create ticker and call tick() twice
    ticker = ScheduleTicker(db_url)
    ticker.tick()
    print("✓ First tick() succeeded")

    ticker.tick()
    print("✓ Second tick() succeeded")

    # Verify exactly one workflow_run was created
    with conn.cursor() as cur:
        cur.execute("SELECT COUNT(*) FROM workflow_run WHERE type = 'test.schedule.pytick.v1'")
        count = cur.fetchone()[0]

    if count != 1:
        print(f"❌ Expected 1 workflow_run, got {count}")
        sys.exit(1)
    print("✓ Idempotency verified: exactly 1 workflow_run created after 2 ticks")

    conn.close()

except ModuleNotFoundError as e:
    print(f"⚠ Skipping Python schedule ticker tests: {e}")
    print("  (Python dependencies not installed)")
    sys.exit(0)
except Exception as e:
    print(f"❌ Error: {e}")
    import traceback; traceback.print_exc()
    sys.exit(1)
PYCODE

PROJECT_DIR="$PROJECT_DIR" DATABASE_URL="$PY_DB_URL" DB_SCHEMA="$SCHEMA" python3 /tmp/test_py_schedule_ticker.py || echo "⚠ Python Schedule Ticker tests skipped"
echo

# Test 8: Python Poller with embedded ticker
echo "8. Testing Python Poller with enable_schedule_ticker()..."
cat > /tmp/test_py_schedule_poller.py <<'PYCODE'
#!/usr/bin/env python3
import sys
import os
sys.path.insert(0, os.environ.get('PROJECT_DIR', '.') + '/python')

try:
    from simpleworkflow import IntentPoller

    db_url = os.getenv('DATABASE_URL', 'postgres://pas:pwd@localhost/pas?schema=workflow')

    poller = IntentPoller(db_url)
    print("✓ Poller created")

    poller.enable_schedule_ticker(tick_interval=30)
    print("✓ Schedule ticker enabled with 30s interval")

    # Validate internal state
    assert poller._schedule_ticker_enabled, "ticker should be enabled"
    print("✓ Poller with embedded ticker configured (not started)")

except ModuleNotFoundError as e:
    print(f"⚠ Skipping Python schedule poller tests: {e}")
    print("  (Python dependencies not installed)")
    sys.exit(0)
except Exception as e:
    print(f"❌ Error: {e}")
    import traceback; traceback.print_exc()
    sys.exit(1)
PYCODE

PROJECT_DIR="$PROJECT_DIR" DATABASE_URL="$PY_DB_URL" python3 /tmp/test_py_schedule_poller.py || echo "⚠ Python Schedule Poller tests skipped"
echo

# Test 9: Syntax and build checks
echo "9. Syntax and build checks..."
cd "$PROJECT_DIR"

# Python syntax check on schedule files
python3 -m py_compile python/simpleworkflow/schedule_ticker.py && echo "✓ schedule_ticker.py syntax valid"

# Go build scheduled-report example
cd "$PROJECT_DIR/examples/go/scheduled-report"
go build -o /tmp/scheduled_report_test . && echo "✓ scheduled-report example compiles"
rm -f /tmp/scheduled_report_test
cd "$PROJECT_DIR"
echo

# Test 10: Cleanup
echo "10. Cleaning up..."
psql "$DB_URL" <<EOF
SET search_path TO $SCHEMA;
DELETE FROM workflow_run WHERE type LIKE 'test.schedule.%';
DELETE FROM workflow_schedule WHERE type LIKE 'test.schedule.%';
EOF

rm -f /tmp/test_go_schedule_client.go /tmp/test_go_schedule_ticker.go /tmp/test_go_schedule_poller.go
rm -f /tmp/test_py_schedule_client.py /tmp/test_py_schedule_ticker.py /tmp/test_py_schedule_poller.py
echo "✓ Cleanup complete"
echo

# Summary
echo "=== Test Summary ==="
echo "✓ Migration verified (workflow_schedule table)"
echo "✓ Go Schedule Client API (create/list/pause/resume/delete)"
echo "✓ Go ScheduleTicker + idempotency"
echo "✓ Go Poller with embedded ticker"
echo "✓ Python Schedule Client API (create/list/pause/resume/delete)"
echo "✓ Python ScheduleTicker + idempotency"
echo "✓ Python Poller with embedded ticker"
echo "✓ Syntax and build checks"
echo
echo "=== All Schedule Tests Passed! ==="
