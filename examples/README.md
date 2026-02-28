# Simple-Workflow Examples

Working examples demonstrating simple-workflow in Go and Python.

## Prerequisites

**SQLite examples** (e.g., `sqlite-worker`): no setup needed — just `go run`.

**PostgreSQL examples** (e.g., `basic-worker`, `billing-worker`):

1. **PostgreSQL Database**
   ```bash
   docker run -d --name postgres \
     -e POSTGRES_USER=pas -e POSTGRES_PASSWORD=pwd -e POSTGRES_DB=pas \
     -p 5432:5432 postgres:15
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

Simple workflow execution with a thumbnail generation use case.

```bash
cd examples/go && go run basic_worker.go
```

Worker picks up `content.thumbnail.v1` runs and logs thumbnail generation.

---

### SQLite Worker (`go/sqlite-worker/main.go`)

All-in-one `Workflow` API with SQLite — no database setup required.

```bash
cd examples/go/sqlite-worker && go run main.go
```

Demonstrates `AutoMigrate()`, fluent `Submit().Execute()`, and producer + worker in one process.

---

### Billing Worker (`go/billing_worker.go`)

Type-prefix routing (`billing.%`), multiple executors, and retry with simulated failures.

```bash
cd examples/go && go run billing_worker.go
```

Handles `billing.invoice.v1` and `billing.payment.v1` — payment fails on first attempt then succeeds on retry.

---

## Python Examples

### Video Processor (`python/video_processor.py`)

Heartbeat support, lease extension, and cancellation checking for long-running workflows.

```bash
cd examples/python && python3 video_processor.py
```

Processes `media.video.v1` runs through transcode → thumbnail → upload steps, extending the lease between each.

---

### Notification Worker (`python/notification_worker.py`)

Single executor handling multiple workflow types via `notify.%` prefix — email, SMS, and push.

```bash
cd examples/python && python3 notification_worker.py
```

---

## Monitoring Queries

```sql
-- Count by status
SELECT status, COUNT(*) FROM workflow.workflow_run GROUP BY status;

-- Recent failures
SELECT type, last_error, attempt, created_at
FROM workflow.workflow_run
WHERE status = 'failed'
ORDER BY created_at DESC LIMIT 10;

-- Workflows by type prefix
SELECT type, status, COUNT(*)
FROM workflow.workflow_run
WHERE type LIKE 'billing.%'
GROUP BY type, status;

-- Recent events (audit trail)
SELECT w.type, e.event_type, e.data, e.created_at
FROM workflow.workflow_event e
JOIN workflow.workflow_run w ON e.workflow_id = w.id
ORDER BY e.created_at DESC LIMIT 20;

-- Long-running workflows (heartbeat count)
SELECT w.id, w.type, COUNT(*) as heartbeats
FROM workflow.workflow_event e
JOIN workflow.workflow_run w ON e.workflow_id = w.id
WHERE e.event_type = 'heartbeat'
GROUP BY w.id, w.type
ORDER BY heartbeats DESC;
```

---

For full API docs see the [main README](../README.md). For Python details see [`python/README.md`](../python/README.md).
