#!/bin/bash
# Integration test script for simple-workflow

set -e

echo "=================================================="
echo "Simple-Workflow Integration Test"
echo "=================================================="
echo ""

# Load .env if it exists
if [ -f .env ]; then
    echo "Loading configuration from .env..."
    export $(cat .env | grep -v '^#' | xargs)
fi

# Database connection (use env vars or defaults)
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_USER="${DB_USER:-postgres}"
DB_PASSWORD="${DB_PASSWORD:-postgres}"
DB_NAME="${DB_NAME:-workflow}"

export PGPASSWORD="$DB_PASSWORD"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Helper functions
check_table() {
    local table=$1
    echo -n "Checking table ${table}... "
    if psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "\d workflow.${table}" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC}"
        return 0
    else
        echo -e "${RED}✗${NC}"
        return 1
    fi
}

query_db() {
    psql -h $DB_HOST -U $DB_USER -d $DB_NAME -c "$1"
}

echo "1. Verifying Database Schema"
echo "----------------------------"
check_table "workflow_run"
check_table "workflow_event"
check_table "workflow_registry"
echo ""

echo "2. Checking Workflow Registry"
echo "-----------------------------"
query_db "SELECT workflow_name, runtime, is_enabled FROM workflow.workflow_registry ORDER BY workflow_name;"
echo ""

echo "3. Checking Workflow Runs"
echo "-------------------------"
RUN_COUNT=$(query_db "SELECT COUNT(*) FROM workflow.workflow_run WHERE deleted_at IS NULL;" -t | tr -d ' ')
echo "Total active workflow runs: $RUN_COUNT"
if [ "$RUN_COUNT" -gt 0 ]; then
    echo ""
    query_db "SELECT id, type, status, attempt, created_at FROM workflow.workflow_run WHERE deleted_at IS NULL ORDER BY created_at DESC LIMIT 10;"
fi
echo ""

echo "4. Test: Create a Thumbnail Workflow Run"
echo "-----------------------------------------"
CONTENT_ID="test-$(date +%s)"
echo "Creating workflow run for content_id: $CONTENT_ID"

query_db "
INSERT INTO workflow.workflow_run (type, payload, idempotency_key)
VALUES (
    'content.thumbnail.v1',
    '{\"content_id\": \"$CONTENT_ID\", \"width\": 300, \"height\": 300}'::jsonb,
    'test:thumbnail:$CONTENT_ID:300x300'
)
RETURNING id, type, status;
"
echo ""

echo "5. Verify Workflow Run Created"
echo "-------------------------------"
query_db "SELECT id, type, status, payload, deleted_at FROM workflow.workflow_run WHERE payload->>'content_id' = '$CONTENT_ID';"
echo ""

echo "6. Check Workflow Events"
echo "------------------------"
query_db "SELECT event_type, COUNT(*) as count FROM workflow.workflow_event GROUP BY event_type ORDER BY count DESC;"
echo ""

echo "=================================================="
echo "Integration test complete!"
echo ""
echo "Next steps:"
echo "1. Start pipeline-worker to process thumbnail workflow runs"
echo "2. Upload an image via PAS API to trigger automatic thumbnail generation"
echo "3. Verify workflow runs are claimed and executed"
echo "4. Check workflow_event table for audit trail"
echo "=================================================="
