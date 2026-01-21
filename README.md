# simple-workflow

A generic, durable workflow system for asynchronous work in Go and Python.

## Overview

`simple-workflow` provides a **language-agnostic** way to declare work that should happen, with **durable persistence** using PostgreSQL and **loose coupling** between producers and workers.

**Core Principle**: Creating a workflow means **recording intent**, not running code. If intent is stored successfully, the system guarantees eventual execution.

## Features

- 🔒 **Durable persistence** - Workflow runs survive crashes and restarts
- 🔄 **Automatic retries** - Failed runs retry with exponential backoff and jitter
- 🎯 **Idempotency** - Duplicate requests don't create duplicate work
- 📊 **Observable** - Track workflow status and audit trail in PostgreSQL
- 🚀 **Language-agnostic** - Producers don't know execution runtime
- ⚖️ **Priority & scheduling** - Control execution order and timing
- 🎯 **Type-prefix routing** - Route workflows by type pattern (e.g., `billing.%`)
- 💓 **Heartbeat support** - Long-running jobs can extend their lease
- 🚫 **Cooperative cancellation** - Cancel workflows gracefully
- 📝 **Audit trail** - All lifecycle events logged to `workflow_event` table

## Installation

### Go

```bash
go get github.com/tendant/simple-workflow
```

### Python

```bash
cd python
pip install -r requirements.txt
# Or install as package
pip install -e .
```

## Quick Start

### 1. Apply Migrations

Apply the SQL migrations to your PostgreSQL database using the Makefile:

```bash
# Show help
make help

# Configure database credentials (recommended)
cp .env.example .env
# Edit .env with your database credentials

# Apply all migrations (uses .env or defaults: localhost, postgres user, workflow db)
make migrate-up

# Check migration status
make migrate-status

# Override .env with command line arguments
make migrate-up DB_HOST=myhost DB_USER=myuser DB_PASSWORD=mypass DB_NAME=mydb

# Rollback last migration
make migrate-down
```

Or manually with goose:

```bash
goose -dir migrations postgres "host=localhost user=postgres dbname=workflow password=postgres sslmode=disable search_path=workflow" up
```

### 2. Producer: Create Workflow Runs

```go
package main

import (
    "context"
    "database/sql"

    simpleworkflow "github.com/tendant/simple-workflow"
    _ "github.com/lib/pq"
)

func main() {
    db, _ := sql.Open("postgres", "postgres://user:pass@localhost/db?sslmode=disable")
    defer db.Close()

    // Create workflow client
    client := simpleworkflow.NewClient(db)

    // Create workflow run
    runID, err := client.Create(context.Background(), simpleworkflow.Intent{
        Type: "content.thumbnail.v1",  // Changed from Name to Type
        Payload: map[string]interface{}{
            "content_id": "c_123",
            "width":      300,
            "height":     300,
        },
        IdempotencyKey: "thumbnail:c_123:300x300",
    })

    if err != nil {
        panic(err)
    }

    println("Created workflow run:", runID)

    // Optional: Cancel a workflow run
    // err = client.Cancel(context.Background(), runID)
}
```

### 3. Worker: Execute Workflow Runs

```go
package main

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"

    simpleworkflow "github.com/tendant/simple-workflow"
    _ "github.com/lib/pq"
)

// ThumbnailExecutor implements simpleworkflow.WorkflowExecutor
type ThumbnailExecutor struct{}

func (e *ThumbnailExecutor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
    var params struct {
        ContentID string `json:"content_id"`
        Width     int    `json:"width"`
        Height    int    `json:"height"`
    }
    if err := json.Unmarshal(run.Payload, &params); err != nil {
        return nil, err
    }

    // Execute workflow logic
    fmt.Printf("Generating thumbnail for %s (%dx%d)\n", params.ContentID, params.Width, params.Height)

    // For long-running jobs, extend the lease
    // run.Heartbeat(ctx, 30*time.Second)

    // Check for cancellation
    // if cancelled, _ := run.IsCancelled(ctx); cancelled {
    //     return nil, fmt.Errorf("workflow cancelled")
    // }

    return map[string]string{"status": "completed"}, nil
}

func main() {
    db, _ := sql.Open("postgres", "postgres://user:pass@localhost/db?sslmode=disable")
    defer db.Close()

    // Create poller with configuration
    config := simpleworkflow.PollerConfig{
        TypePrefixes:  []string{"content.%"},  // Type-prefix routing
        LeaseDuration: 30 * time.Second,       // Lease duration
        PollInterval:  2 * time.Second,        // Poll interval
        WorkerID:      "thumbnail-worker-1",   // Worker identifier
    }
    poller := simpleworkflow.NewPoller(db, config)

    // Register executor
    poller.RegisterExecutor("content.thumbnail.v1", &ThumbnailExecutor{})

    // Optional: Set up Prometheus metrics
    // metrics := simpleworkflow.NewPrometheusMetrics(nil)
    // poller.SetMetrics(metrics)

    // Start polling
    poller.Start(context.Background())
}
```

### 4. Python Worker (Alternative)

Python workers use the same `workflow_run` table:

```python
#!/usr/bin/env python3
from simpleworkflow import IntentPoller, WorkflowExecutor, WorkflowRun

class ThumbnailExecutor(WorkflowExecutor):
    def execute(self, run: WorkflowRun):
        payload = run.payload  # Access as attribute
        content_id = payload['content_id']
        width = payload['width']
        height = payload['height']

        # Execute workflow logic
        print(f"Generating thumbnail for {content_id} ({width}x{height})")

        # For long-running jobs, extend the lease
        # run.heartbeat(30)  # Extend by 30 seconds

        # Check for cancellation
        # if run.is_cancelled():
        #     raise Exception("Workflow cancelled")

        return {"status": "completed"}

# Database URL (must include search_path=workflow)
db_url = "postgres://user:pass@localhost/db?search_path=workflow"

# Create and configure poller
poller = IntentPoller(
    db_url=db_url,
    type_prefixes=["content.%"],  # Type-prefix routing
    worker_id="thumbnail-worker-python",
    lease_duration=30,  # Lease duration in seconds
    poll_interval=2     # Poll interval in seconds
)

# Register executor
poller.register_executor("content.thumbnail.v1", ThumbnailExecutor())

# Start polling
poller.start()
```

See `python/README.md` for complete Python documentation.

## Architecture

```
┌─────────────┐
│  Producer   │  Creates workflow runs
│ (Your API)  │  - Never calls workers
└──────┬──────┘  - Never depends on worker availability
       │
       │ INSERT workflow run
       ↓
┌──────────────────────────┐
│  workflow_run table      │  Durable inbox
│  (PostgreSQL)            │  - status: pending → leased → succeeded/failed/cancelled
└──────┬───────────────────┘  - priority, run_at, idempotency_key
       │                      - type-prefix routing support
       │
       │ claim + execute (with heartbeat)
       ↓
┌──────────────┐            ┌───────────────────────┐
│   Worker     │            │  workflow_event table │  Audit trail
│ (Go/Python)  │  ────────> │  (PostgreSQL)         │  - All lifecycle events
└──────────────┘  log       └───────────────────────┘  - created, leased, succeeded, etc.
  - Claims runs
  - Reports results
  - Extends lease (heartbeat)
  - Checks cancellation
```

## API Reference

### Producer API

#### `Client`

```go
type Client struct { /* ... */ }

func NewClient(db *sql.DB) *Client
func (c *Client) Create(ctx context.Context, intent Intent) (string, error)
func (c *Client) Cancel(ctx context.Context, runID string) error  // NEW: Cooperative cancellation
```

#### `Intent`

```go
type Intent struct {
    Type           string      // Workflow type (e.g. "content.thumbnail.v1") - RENAMED from Name
    Payload        interface{} // JSON-encodable data
    Priority       int         // Lower executes first (default: 100)
    RunAfter       time.Time   // Schedule for future (default: now)
    IdempotencyKey string      // Optional deduplication key
    MaxAttempts    int         // Retry limit (default: 3)
}
```

### Worker API

#### `PollerConfig`

```go
type PollerConfig struct {
    TypePrefixes  []string      // Type prefixes to match (e.g. ["billing.%", "media.%"]) - NEW
    LeaseDuration time.Duration // Lease duration (default: 30s) - NEW
    PollInterval  time.Duration // Poll interval (default: 2s)
    WorkerID      string        // Worker identifier (default: "go-worker")
}
```

#### `Poller`

```go
type Poller struct { /* ... */ }

func NewPoller(db *sql.DB, config PollerConfig) *Poller  // NEW: Takes config instead of workflow list
func (p *Poller) RegisterExecutor(workflowType string, executor WorkflowExecutor)
func (p *Poller) SetMetrics(m MetricsCollector)  // Optional: Prometheus metrics
func (p *Poller) Start(ctx context.Context)
func (p *Poller) Stop()
```

#### `WorkflowRun`

```go
type WorkflowRun struct {
    ID          string
    Type        string  // RENAMED from Name
    Payload     []byte
    Attempt     int     // RENAMED from AttemptCount
    MaxAttempts int

    // NEW: Functions for long-running workflows
    Heartbeat    HeartbeatFunc          // Extend the lease
    IsCancelled  CancellationCheckFunc  // Check if cancelled
}

// Function types
type HeartbeatFunc func(ctx context.Context, duration time.Duration) error
type CancellationCheckFunc func(ctx context.Context) (bool, error)
```

#### `WorkflowExecutor`

```go
type WorkflowExecutor interface {
    Execute(ctx context.Context, run *WorkflowRun) (interface{}, error)  // UPDATED: run instead of intent
}
```

## Workflow Naming & Type-Prefix Routing

All workflows must be versioned using dotted notation:

```
<domain>.<subdomain>.<action>.vN
```

Examples:
- `content.thumbnail.v1`
- `content.ocr.v1`
- `notify.email.v1`
- `billing.invoice.v1`
- `billing.payment.v1`

**Type-Prefix Routing** allows workers to claim workflows by pattern:

```go
// This worker handles all billing workflows
config := simpleworkflow.PollerConfig{
    TypePrefixes: []string{"billing.%"},
    // ...
}
```

Rules:
- Never change behavior without bumping `vN`
- Old workflow runs must remain executable
- Use prefixes for logical partitioning (e.g., `billing.%`, `media.%`)

## Failure & Retry

- Failed workflow runs automatically retry with exponential backoff **with jitter** (prevents thundering herd)
- After `max_attempts`, runs move to `failed` status (was `deadletter`)
- If a worker crashes, the lease expires and another worker retries
- Workers can extend their lease using `Heartbeat()` for long-running jobs

```
Attempt 1: immediate
Attempt 2: +1 minute (+10% jitter)
Attempt 3: +4 minutes (+10% jitter)
Attempt 4: failed (permanent)
```

## Long-Running Workflows

For workflows that take longer than the lease duration:

**Go:**
```go
func (e *Executor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
    // Start a goroutine to periodically extend the lease
    done := make(chan struct{})
    defer close(done)

    go func() {
        ticker := time.NewTicker(15 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                run.Heartbeat(ctx, 30*time.Second)
            case <-done:
                return
            }
        }
    }()

    // Do long-running work...
    time.Sleep(2 * time.Minute)

    return result, nil
}
```

**Python:**
```python
def execute(self, run: WorkflowRun):
    # Extend lease before long operation
    run.heartbeat(60)  # Extend by 60 seconds

    # Do long-running work...
    time.sleep(120)

    return result
```

## Cooperative Cancellation

Cancel a workflow run gracefully:

**Producer:**
```go
err := client.Cancel(ctx, runID)
```

**Worker:**
```go
func (e *Executor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
    // Check for cancellation periodically
    if cancelled, _ := run.IsCancelled(ctx); cancelled {
        return nil, fmt.Errorf("workflow cancelled")
    }

    // Continue work...
    return result, nil
}
```

## Database Schema

### `workflow_run`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `type` | TEXT | Workflow type (e.g. `content.thumbnail.v1`) - **RENAMED from `name`** |
| `payload` | JSONB | Workflow arguments |
| `status` | TEXT | `pending`, `leased`, `succeeded`, `failed`, `cancelled` - **UPDATED** |
| `priority` | INT | Lower executes first (default: 100) |
| `run_at` | TIMESTAMPTZ | Earliest execution time - **RENAMED from `run_after`** |
| `idempotency_key` | TEXT | Unique deduplication key |
| `attempt` | INT | Number of execution attempts - **RENAMED from `attempt_count`** |
| `max_attempts` | INT | Retry limit (default: 3) |
| `leased_by` | TEXT | Worker ID - **RENAMED from `claimed_by`** |
| `lease_until` | TIMESTAMPTZ | Lease expiration - **RENAMED from `lease_expires_at`** |
| `last_error` | TEXT | Most recent error message |
| `result` | JSONB | Workflow result |
| `created_at` | TIMESTAMPTZ | Run creation time |
| `updated_at` | TIMESTAMPTZ | Last update time |
| `deleted_at` | TIMESTAMPTZ | Soft delete timestamp (NULL = active) |

**Indexes:**
- `idx_workflow_run_claim`: On `(status, run_at, priority, created_at)` for efficient claiming
- `idx_workflow_run_type_prefix`: On `(type text_pattern_ops, status, run_at)` for type-prefix routing - **NEW**
- `idx_workflow_run_idempotency`: On `(idempotency_key)` for deduplication
- `idx_workflow_run_type_status`: On `(type, status, created_at DESC)` for monitoring
- `idx_workflow_run_deleted_at`: On `(deleted_at)` for soft delete queries

### `workflow_event` (NEW)

Audit trail of all workflow run lifecycle events.

| Column | Type | Description |
|--------|------|-------------|
| `id` | BIGSERIAL | Primary key |
| `workflow_id` | UUID | Reference to `workflow_run.id` |
| `event_type` | TEXT | Event type: `created`, `leased`, `started`, `heartbeat`, `succeeded`, `failed`, `retried`, `cancelled` |
| `data` | JSONB | Additional event metadata |
| `created_at` | TIMESTAMPTZ | Event timestamp |

**Indexes:**
- `idx_workflow_event_workflow_id`: On `(workflow_id, created_at DESC)` for querying by workflow
- `idx_workflow_event_type`: On `(event_type, created_at DESC)` for analytics

**Example queries:**
```sql
-- Get all events for a workflow run
SELECT event_type, data, created_at
FROM workflow_event
WHERE workflow_id = 'run-uuid'
ORDER BY created_at;

-- Count events by type
SELECT event_type, COUNT(*)
FROM workflow_event
GROUP BY event_type;
```

### `workflow_registry`

| Column | Type | Description |
|--------|------|-------------|
| `workflow_name` | TEXT | Primary key (e.g. `content.thumbnail.v1`) |
| `runtime` | TEXT | `go` or `python` |
| `is_enabled` | BOOLEAN | Whether workflow is enabled |
| `description` | TEXT | Human-readable description |

## Design Principles

### 1. Intent, not execution

Creating a workflow means **recording intent**, not running code. If intent is stored successfully, the system guarantees eventual execution.

### 2. Producers never depend on workers

**Producers**:
- Insert intents
- Optionally query status
- Never call workers

**Workers**:
- Pull work
- Execute logic
- Report results

### 3. Execution runtime is an implementation detail

The same workflow may be implemented in Go today, Python tomorrow. Producers never care.

## Makefile Commands

The project includes a Makefile for convenient database migration management:

```bash
# Show all available commands
make help

# Database Migrations
make migrate-up         # Apply all pending migrations
make migrate-down       # Rollback the last migration
make migrate-status     # Show current migration status
make migrate-reset      # Rollback all and reapply (WARNING: destructive)
make migrate-create NAME=my_migration  # Create a new migration file

# Build and Test
make build             # Build the library
make test              # Run integration tests
make clean             # Clean build artifacts
```

### Environment Variables

Override database connection settings:

```bash
# Default values
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=postgres
DB_NAME=workflow
DB_SSLMODE=disable

# Example with custom values
make migrate-up DB_USER=myuser DB_PASSWORD=mypass DB_NAME=production
```

### Common Workflows

**Initial Setup:**
```bash
make migrate-up
make migrate-status
```

**Development Iteration:**
```bash
# Create new migration
make migrate-create NAME=add_new_feature

# Edit the generated migration file
# migrations/YYYYMMDDHHMMSS_add_new_feature.sql

# Apply it
make migrate-up
```

**Rollback and Retry:**
```bash
# Rollback last migration
make migrate-down

# Make fixes to the migration file
# Reapply
make migrate-up
```

## Monitoring

Query workflow status:

```sql
-- Count by status
SELECT status, COUNT(*) FROM workflow.workflow_run GROUP BY status;

-- Recent failures
SELECT type, last_error, created_at, attempt
FROM workflow.workflow_run
WHERE status = 'failed'
ORDER BY created_at DESC
LIMIT 10;

-- Permanently failed workflows
SELECT * FROM workflow.workflow_run WHERE status = 'failed';

-- Cancelled workflows
SELECT * FROM workflow.workflow_run WHERE status = 'cancelled';

-- Workflow runs by type prefix (e.g., all billing workflows)
SELECT type, status, COUNT(*)
FROM workflow.workflow_run
WHERE type LIKE 'billing.%'
GROUP BY type, status;

-- Recent workflow events
SELECT w.type, e.event_type, e.data, e.created_at
FROM workflow.workflow_event e
JOIN workflow.workflow_run w ON e.workflow_id = w.id
ORDER BY e.created_at DESC
LIMIT 20;

-- Workflows with heartbeats (long-running)
SELECT w.id, w.type, COUNT(*) as heartbeat_count
FROM workflow.workflow_event e
JOIN workflow.workflow_run w ON e.workflow_id = w.id
WHERE e.event_type = 'heartbeat'
GROUP BY w.id, w.type
ORDER BY heartbeat_count DESC;
```

## Prometheus Metrics

The library exports Prometheus metrics for observability:

**Workflow Run Metrics:**
- `workflow_run_claimed_total{workflow_type, worker_id}` - Total runs claimed
- `workflow_run_completed_total{workflow_type, worker_id, status}` - Total runs completed
- `workflow_run_failed_total{workflow_type, worker_id}` - Total runs permanently failed
- `workflow_run_failed_attempts_total{workflow_type, worker_id, attempt}` - Failed attempts
- `workflow_run_execution_duration_seconds{workflow_type, worker_id, status}` - Execution duration histogram

**Worker Metrics:**
- `workflow_worker_poll_cycle_total{worker_id}` - Total poll cycles
- `workflow_worker_poll_errors_total{worker_id, error_type}` - Poll errors
- `workflow_run_queue_depth{workflow_type, status}` - Queue depth gauge
- `workflow_worker_uptime_seconds{worker_id}` - Worker uptime
- `workflow_worker_last_poll_timestamp{worker_id}` - Last poll timestamp

**Example Prometheus queries:**
```promql
# Rate of successful workflow completions
rate(workflow_run_completed_total{status="succeeded"}[5m])

# Failed workflow rate by type
rate(workflow_run_failed_total[5m])

# P95 execution duration
histogram_quantile(0.95, rate(workflow_run_execution_duration_seconds_bucket[5m]))

# Queue depth by workflow type
workflow_run_queue_depth{status="pending"}
```

## License

MIT
