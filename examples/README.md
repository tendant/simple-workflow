# Simple-Workflow Examples

This directory contains working examples demonstrating the key features of simple-workflow in both Go and Python.

## Prerequisites

1. **PostgreSQL Database**
   ```bash
   # Start PostgreSQL (if using Docker)
   docker run -d --name postgres \
     -e POSTGRES_USER=pas \
     -e POSTGRES_PASSWORD=pwd \
     -e POSTGRES_DB=pas \
     -p 5432:5432 \
     postgres:15
   ```

2. **Run Migrations**
   ```bash
   cd /path/to/simple-workflow
   make migrate-up
   ```

3. **Set Environment Variables**
   ```bash
   export DATABASE_URL="postgres://pas:pwd@localhost/pas?search_path=workflow"
   ```

## Go Examples

### Basic Worker (`go/basic_worker.go`)

**What it demonstrates:**
- Simple workflow execution
- Basic configuration
- Thumbnail generation use case

**Run it:**
```bash
cd examples/go
go run basic_worker.go
```

**Test it:**
```bash
# In another terminal, create a workflow run
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('content.thumbnail.v1', '{\"content_id\": \"img123\", \"width\": 300, \"height\": 200}');
"
```

**Expected output:**
```
Starting thumbnail worker...
Generating thumbnail for content img123 (300x200)
Thumbnail completed for content img123
```

---

### Billing Worker (`go/billing_worker.go`)

**What it demonstrates:**
- Type-prefix routing (`billing.%` matches all billing workflows)
- Multiple executors per worker
- Invoice and payment processing
- Retry logic with simulated failures

**Run it:**
```bash
cd examples/go
go run billing_worker.go
```

**Test it:**
```bash
# Create an invoice workflow
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('billing.invoice.v1', '{\"invoice_id\": \"INV-001\", \"amount\": 150.00, \"customer_id\": \"CUST-123\"}');
"

# Create a payment workflow (will fail first time, then retry)
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('billing.payment.v1', '{\"payment_id\": \"PAY-001\", \"amount\": 15000.00, \"method\": \"credit_card\"}');
"
```

**Expected output:**
```
Starting billing worker (handles all billing.* workflows)...
Generating invoice INV-001 for customer CUST-123 (amount: $150.00)
Invoice INV-001 generated successfully

Processing payment PAY-001 (amount: $15000.00, method: credit_card)
Error: payment gateway timeout (will retry)
[After backoff] Processing payment PAY-001 (amount: $15000.00, method: credit_card)
Payment PAY-001 processed successfully
```

---

## Python Examples

### Video Processor (`python/video_processor.py`)

**What it demonstrates:**
- Heartbeat support for long-running workflows
- Lease extension to prevent timeout
- Cancellation checking
- Type-prefix routing (`media.%`)

**Run it:**
```bash
cd examples/python
python3 video_processor.py
```

**Test it:**
```bash
# Create a video processing workflow
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('media.video.v1', '{\"video_id\": \"vid123\", \"operations\": [\"transcode\", \"thumbnail\", \"upload\"]}');
"

# To test cancellation (run while workflow is processing):
psql $DATABASE_URL -c "
UPDATE workflow_run SET status = 'cancelled' WHERE id = '<workflow-id>';
"
```

**Expected output:**
```
Starting video processing worker...
Starting video processing for vid123
Operations: ['transcode', 'thumbnail', 'upload']
Executing operation 1/3: transcode
Transcoding video vid123...
Extending lease by 30 seconds...
Executing operation 2/3: thumbnail
Generating thumbnails for vid123...
Extending lease by 30 seconds...
Executing operation 3/3: upload
Uploading video vid123 to CDN...
Video processing completed for vid123
```

---

### Notification Worker (`python/notification_worker.py`)

**What it demonstrates:**
- Single executor handling multiple workflow types
- Type-prefix routing (`notify.%` matches all notifications)
- Email, SMS, and push notification workflows
- Cancellation checking

**Run it:**
```bash
cd examples/python
python3 notification_worker.py
```

**Test it:**
```bash
# Send an email notification
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('notify.email.v1', '{\"to\": \"user@example.com\", \"subject\": \"Welcome!\", \"body\": \"Thanks for signing up\"}');
"

# Send an SMS notification
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('notify.sms.v1', '{\"phone\": \"+1234567890\", \"message\": \"Your code is 123456\"}');
"

# Send a push notification
psql $DATABASE_URL -c "
INSERT INTO workflow_run (type, payload)
VALUES ('notify.push.v1', '{\"user_id\": \"user123\", \"title\": \"New Message\", \"body\": \"You have a new message\"}');
"
```

**Expected output:**
```
Starting notification worker...
Watching for workflows: ['notify.%']
Handles: email, SMS, and push notifications

Processing notification: notify.email.v1
Sending email to user@example.com: Welcome!
Email sent successfully to user@example.com

Processing notification: notify.sms.v1
Sending SMS to +1234567890
SMS sent successfully to +1234567890

Processing notification: notify.push.v1
Sending push notification to user user123: New Message
Push notification sent to user user123
```

---

## Key Concepts Demonstrated

### Type-Prefix Routing

Workers claim workflows based on type prefixes using SQL `LIKE` patterns:

```go
// Go: Worker handles all billing workflows
config := simpleworkflow.PollerConfig{
    TypePrefixes: []string{"billing.%"},
}
```

```python
# Python: Worker handles all notification workflows
type_prefixes = ['notify.%']
```

This enables:
- **Workload partitioning** - Different workers for different domains
- **Resource isolation** - Heavy tasks on dedicated workers
- **Team boundaries** - Teams own their workflow prefixes

### Heartbeat Extension

For long-running workflows, extend the lease to prevent timeout:

```python
def execute(self, run: WorkflowRun):
    for operation in long_operations:
        process(operation)
        run.heartbeat(30)  # Extend lease by 30 seconds
```

### Cooperative Cancellation

Check for cancellation periodically and stop gracefully:

```python
def execute(self, run: WorkflowRun):
    for i in range(100):
        if run.is_cancelled():
            cleanup()
            raise Exception("Workflow cancelled")
        process_chunk(i)
```

---

## Monitoring Workflows

### Check workflow status
```sql
SELECT id, type, status, attempt, created_at, updated_at
FROM workflow_run
ORDER BY created_at DESC
LIMIT 10;
```

### View workflow events (audit trail)
```sql
SELECT e.event_type, e.created_at, e.data
FROM workflow_event e
WHERE e.workflow_id = '<workflow-id>'
ORDER BY e.created_at;
```

### Find failed workflows
```sql
SELECT id, type, last_error, attempt, max_attempts
FROM workflow_run
WHERE status = 'failed';
```

### Cancel a running workflow
```sql
UPDATE workflow_run
SET status = 'cancelled', updated_at = NOW()
WHERE id = '<workflow-id>' AND status IN ('pending', 'leased');
```

---

## Architecture

```
┌─────────────────┐
│  Application    │
│                 │
│  Creates runs   │
└────────┬────────┘
         │
         ▼
┌─────────────────────────────────────┐
│      PostgreSQL Database            │
│                                     │
│  ┌───────────────────────────────┐ │
│  │     workflow_run table        │ │
│  │  - pending workflows          │ │
│  │  - type-prefix indexed        │ │
│  └───────────────────────────────┘ │
│                                     │
│  ┌───────────────────────────────┐ │
│  │    workflow_event table       │ │
│  │  - audit trail of all events  │ │
│  └───────────────────────────────┘ │
└──────────┬──────────────────────────┘
           │
           │ (poll & claim with SKIP LOCKED)
           │
    ┌──────┴──────┬──────────┬──────────┐
    ▼             ▼          ▼          ▼
┌─────────┐  ┌─────────┐ ┌─────────┐ ┌─────────┐
│Worker 1 │  │Worker 2 │ │Worker 3 │ │Worker N │
│billing.%│  │notify.% │ │media.%  │ │custom.% │
└─────────┘  └─────────┘ └─────────┘ └─────────┘
```

Each worker:
1. Polls for workflows matching its type prefixes
2. Claims workflows using row-level locking
3. Executes the workflow
4. Extends lease via heartbeat if needed
5. Checks for cancellation periodically
6. Updates status and logs events

---

## Troubleshooting

### Worker not claiming workflows

**Check type-prefix matching:**
```sql
-- See what workflows exist
SELECT type, status FROM workflow_run;

-- Test your prefix pattern
SELECT * FROM workflow_run
WHERE type LIKE 'billing.%' AND status = 'pending';
```

### Workflow stuck in "leased" status

**Check for expired leases:**
```sql
SELECT id, type, status, lease_until, leased_by
FROM workflow_run
WHERE status = 'leased' AND lease_until < NOW();
```

Workers will automatically reclaim expired leases.

### Heartbeat not extending lease

**Verify heartbeat is being called:**
```sql
-- Check for heartbeat events
SELECT workflow_id, event_type, created_at
FROM workflow_event
WHERE event_type = 'heartbeat'
ORDER BY created_at DESC;
```

---

## Next Steps

1. **Customize examples** - Adapt these examples to your use cases
2. **Add your executors** - Implement your business logic
3. **Configure metrics** - Enable Prometheus metrics for monitoring
4. **Deploy workers** - Run workers as separate services
5. **Monitor production** - Use the audit trail and metrics to monitor health

For more details, see:
- [Main README](../README.md) - Full API documentation
- [Python README](../python/README.md) - Python-specific guide
- [DESIGN.md](../DESIGN.md) - Architecture and design decisions
