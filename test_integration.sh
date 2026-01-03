#!/bin/bash
# Integration test script for simple-workflow

set -e

echo "=================================================="
echo "Simple-Workflow Integration Test"
echo "=================================================="
echo ""

# Database connection
export PGPASSWORD=pwd
DB_HOST="localhost"
DB_USER="pas"
DB_NAME="pas"

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
check_table "workflow_intent"
check_table "workflow_registry"
echo ""

echo "2. Checking Workflow Registry"
echo "-----------------------------"
query_db "SELECT workflow_name, runtime, is_enabled FROM workflow.workflow_registry ORDER BY workflow_name;"
echo ""

echo "3. Checking Workflow Intents"
echo "---------------------------"
INTENT_COUNT=$(query_db "SELECT COUNT(*) FROM workflow.workflow_intent WHERE deleted_at IS NULL;" -t | tr -d ' ')
echo "Total active intents: $INTENT_COUNT"
if [ "$INTENT_COUNT" -gt 0 ]; then
    echo ""
    query_db "SELECT id, name, status, attempt_count, created_at FROM workflow.workflow_intent WHERE deleted_at IS NULL ORDER BY created_at DESC LIMIT 10;"
fi
echo ""

echo "4. Test: Create a Thumbnail Intent"
echo "-----------------------------------"
CONTENT_ID="test-$(date +%s)"
echo "Creating intent for content_id: $CONTENT_ID"

query_db "
INSERT INTO workflow.workflow_intent (name, payload, idempotency_key)
VALUES (
    'content.thumbnail.v1',
    '{\"content_id\": \"$CONTENT_ID\", \"width\": 300, \"height\": 300}'::jsonb,
    'test:thumbnail:$CONTENT_ID:300x300'
)
RETURNING id, name, status;
"
echo ""

echo "5. Verify Intent Created"
echo "------------------------"
query_db "SELECT id, name, status, payload, deleted_at FROM workflow.workflow_intent WHERE payload->>'content_id' = '$CONTENT_ID';"
echo ""

echo "=================================================="
echo "Integration test complete!"
echo ""
echo "Next steps:"
echo "1. Start pipeline-worker to process thumbnail intents"
echo "2. Upload an image via PAS API to trigger automatic thumbnail generation"
echo "3. Verify intents are claimed and executed"
echo "=================================================="
