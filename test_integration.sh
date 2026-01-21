#!/bin/bash
# End-to-end integration test
# Actually starts a worker and processes workflows

set -e

echo "=== End-to-End Integration Test ==="
echo

# Database configuration
DB_URL="${DATABASE_URL:-postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow}"

echo "Database: $DB_URL"
echo

# Clean up any existing test workflows
echo "1. Cleaning up test data..."
psql "postgres://pas:pwd@localhost/pas" <<EOF
SET search_path TO workflow;
DELETE FROM workflow_run WHERE type LIKE 'integration.%';
EOF
echo "✓ Clean up complete"
echo

# Test 1: Start Go worker in background
echo "2. Starting Go worker in background..."
cat > /tmp/integration_worker.go <<'GOCODE'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	simpleworkflow "github.com/tendant/simple-workflow"
	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	poller, err := simpleworkflow.NewPoller(dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer poller.Close()

	// Register handler that logs to file
	poller.HandleFunc("integration.test.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
		var payload map[string]interface{}
		json.Unmarshal(run.Payload, &payload)

		msg := fmt.Sprintf("Processed workflow %s with payload: %v", run.ID, payload)
		fmt.Println(msg)

		// Write result to file so test can verify
		f, _ := os.Create("/tmp/integration_test_result.txt")
		defer f.Close()
		f.WriteString(msg)

		return map[string]string{
			"status": "completed",
			"message": "Integration test successful",
		}, nil
	})

	ctx, _ := context.WithTimeout(context.Background(), 30*time.Second)
	fmt.Println("Worker started, waiting for workflows...")
	poller.Start(ctx)
}
GOCODE

cd /Users/lei/workspace/pas/simple-workflow
go run /tmp/integration_worker.go &
WORKER_PID=$!
echo "✓ Worker started (PID: $WORKER_PID)"
echo

# Wait for worker to initialize
sleep 2

# Test 2: Submit workflow
echo "3. Submitting workflow..."
cat > /tmp/integration_client.go <<'GOCODE'
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
	client, err := simpleworkflow.NewClient(dbURL)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	runID, err := client.Submit("integration.test.v1", map[string]interface{}{
		"test_id": "integration-001",
		"timestamp": "2024-01-20",
	}).Execute(context.Background())

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s\n", runID)
}
GOCODE

RUN_ID=$(go run /tmp/integration_client.go)
echo "✓ Workflow submitted: $RUN_ID"
echo

# Test 3: Wait for processing (max 15 seconds)
echo "4. Waiting for workflow to be processed..."
MAX_WAIT=15
WAITED=0
while [ $WAITED -lt $MAX_WAIT ]; do
	STATUS=$(psql "postgres://pas:pwd@localhost/pas" -t -A -c "SET search_path TO workflow; SELECT status FROM workflow_run WHERE id = '$RUN_ID';" | grep -v "^SET$")
	STATUS=$(echo $STATUS | tr -d ' \n')

	if [ "$STATUS" = "succeeded" ]; then
		echo "✓ Workflow processed successfully!"
		break
	elif [ "$STATUS" = "failed" ]; then
		echo "❌ Workflow failed"
		psql "postgres://pas:pwd@localhost/pas" -c "SET search_path TO workflow; SELECT id, status, last_error FROM workflow_run WHERE id = '$RUN_ID';"
		kill $WORKER_PID 2>/dev/null || true
		exit 1
	fi

	echo "  Status: $STATUS (waiting...)"
	sleep 1
	WAITED=$((WAITED + 1))
done

if [ $WAITED -eq $MAX_WAIT ]; then
	echo "❌ Timeout waiting for workflow to complete"
	kill $WORKER_PID 2>/dev/null || true
	exit 1
fi
echo

# Test 4: Verify result
echo "5. Verifying workflow result..."
psql "postgres://pas:pwd@localhost/pas" <<EOF
SET search_path TO workflow;
SELECT
	id,
	type,
	status,
	result,
	attempt
FROM workflow_run
WHERE id = '$RUN_ID';
EOF

if [ -f /tmp/integration_test_result.txt ]; then
	echo
	echo "Worker output:"
	cat /tmp/integration_test_result.txt
	echo
	echo "✓ Worker processed the workflow successfully"
else
	echo "⚠ Worker output file not found"
fi
echo

# Test 5: Verify events
echo "6. Verifying audit events..."
psql "postgres://pas:pwd@localhost/pas" <<EOF
SET search_path TO workflow;
SELECT
	event_type,
	created_at
FROM workflow_event
WHERE workflow_id = '$RUN_ID'
ORDER BY created_at;
EOF
echo

# Cleanup
echo "7. Cleaning up..."
kill $WORKER_PID 2>/dev/null || true
wait $WORKER_PID 2>/dev/null || true
rm -f /tmp/integration_worker.go /tmp/integration_client.go /tmp/integration_test_result.txt
echo "✓ Cleanup complete"
echo

echo "=== Integration Test Passed! 🎉 ==="
