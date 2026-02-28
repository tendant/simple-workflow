# simple-workflow

A durable **system of record for intent** — not a workflow engine.

## Overview

`simple-workflow` records *what should happen*, decoupled from *how or when it happens*. Producers write intent to a `workflow_run` table. Workers poll for it. PostgreSQL (or SQLite) is the only coordination point — no scheduler, no broker, no central executor.

- A durable inbox for asynchronous work
- A contract between producers ("I need this done") and workers ("I can do this")
- A system of record you can query, audit, and monitor with plain SQL

## Features

- **Durable persistence** — workflow runs survive crashes and restarts
- **Automatic retries** — exponential backoff with jitter
- **Idempotency** — duplicate requests don't create duplicate work
- **Observable** — audit trail in PostgreSQL via `workflow_event` table
- **Type-prefix routing** — route workflows by pattern (e.g., `billing.%`)
- **Heartbeat & cancellation** — long-running jobs extend leases; cancel gracefully
- **Priority & scheduling** — control execution order and timing

## Installation

```bash
go get github.com/tendant/simple-workflow
```

Python: see [`python/README.md`](python/README.md).

## Quick Start

```go
wf, err := simpleworkflow.New("sqlite:///tmp/workflow.db")
if err != nil { log.Fatal(err) }
defer wf.Close()

wf.HandleFunc("demo.hello.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (any, error) {
    log.Printf("Hello! payload=%s", run.Payload)
    return map[string]string{"status": "done"}, nil
})

runID, _ := wf.Submit("demo.hello.v1", map[string]string{"msg": "hi"}).Execute(ctx)

wf.WithAutoMigrate().Start(ctx) // creates tables automatically
```

### Migrations

**Embedded DDL (recommended for getting started):**
```go
wf.WithAutoMigrate().Start(ctx)   // auto-migrate on Start()
wf.AutoMigrate(ctx)               // or migrate explicitly
client.AutoMigrate(ctx)           // also works on Client and Poller
```

Works for both SQLite and PostgreSQL — no external tools needed.

**Version-controlled migrations (recommended for production):**
```bash
make migrate-up       # apply all pending migrations
make migrate-status   # check status
make migrate-down     # rollback last migration
```

### Producer

```go
client, _ := simpleworkflow.NewClient("postgres://user:pass@localhost/db?schema=workflow")
defer client.Close()

runID, err := client.Submit("content.thumbnail.v1", payload).
    WithIdempotency("thumbnail:c_123:300x300").
    WithPriority(10).
    Execute(ctx)

err = client.Cancel(ctx, runID)
```

### Worker

```go
poller, _ := simpleworkflow.NewPoller("postgres://user:pass@localhost/db?schema=workflow")
defer poller.Close()

poller.HandleFunc("content.thumbnail.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (any, error) {
    var params struct {
        ContentID string `json:"content_id"`
        Width     int    `json:"width"`
    }
    json.Unmarshal(run.Payload, &params)
    fmt.Printf("Generating thumbnail for %s\n", params.ContentID)
    return map[string]string{"status": "completed"}, nil
})

poller.Start(ctx) // type prefix "content.%" auto-detected from handlers
```

## API Reference

### Connection Strings

| Database   | Format | Example |
|------------|--------|---------|
| SQLite     | `sqlite://<path>` | `sqlite:///tmp/workflow.db` or `sqlite://:memory:` |
| PostgreSQL | `postgres://user:pass@host/db?schema=workflow` | `postgres://user:pass@localhost/mydb?schema=workflow` |

The `?schema=` parameter sets the PostgreSQL `search_path` (defaults to `workflow`).

### Client (Producer)

```go
client, err := simpleworkflow.NewClient("postgres://...")   // from connection string
client := simpleworkflow.NewClientWithDB(db)                // from existing *sql.DB
defer client.Close()

runID, err := client.Submit("workflow.type.v1", payload).
    WithIdempotency("unique-key").
    WithPriority(10).
    WithMaxAttempts(5).
    RunIn(5 * time.Minute).
    Execute(ctx)

err = client.Cancel(ctx, runID)
```

### Poller (Worker)

```go
poller, err := simpleworkflow.NewPoller("postgres://...")    // from connection string
poller := simpleworkflow.NewPollerWithDB(db)                 // from existing *sql.DB
defer poller.Close()

poller.WithWorkerID("custom-worker").
    WithLeaseDuration(60 * time.Second).
    WithPollInterval(5 * time.Second).
    WithTypePrefixes("billing.%", "media.%")                 // override auto-detection

poller.HandleFunc("billing.invoice.v1", handler)             // function handler
poller.Handle("billing.payment.v1", &PaymentExecutor{})      // struct handler

poller.Start(ctx)
```

### Workflow (All-in-One)

Combines Client + Poller in a single object — ideal for small services or getting started.

```go
wf, err := simpleworkflow.New("sqlite:///tmp/workflow.db")
wf := simpleworkflow.NewWithDB(db, dialect)
defer wf.Close()

wf.HandleFunc("demo.hello.v1", handler)
runID, err := wf.Submit("demo.hello.v1", payload).Execute(ctx)
wf.WithAutoMigrate().Start(ctx)
```

### WithAutoMigrate

```go
wf.WithAutoMigrate().Start(ctx)       // on Workflow
poller.WithAutoMigrate().Start(ctx)   // on Poller
```

Runs idempotent `CREATE TABLE IF NOT EXISTS` on `Start()`. Works for both SQLite and PostgreSQL.

### Smart Defaults

- **Worker ID:** `hostname-pid` (e.g., `my-server-12345`)
- **Lease duration:** 30 seconds
- **Poll interval:** 2 seconds
- **Type prefixes:** auto-detected from registered handlers
- **Schema:** `workflow` (or from `?schema=` parameter)

### Core Types

#### `Intent`

```go
type Intent struct {
    Type           string      // Workflow type (e.g. "content.thumbnail.v1")
    Payload        interface{} // JSON-encodable data
    Priority       int         // Lower executes first (default: 100)
    RunAfter       time.Time   // Schedule for future (default: now)
    IdempotencyKey string      // Optional deduplication key
    MaxAttempts    int         // Retry limit (default: 3)
}
```

#### `WorkflowRun`

```go
type WorkflowRun struct {
    ID          string
    Type        string
    Payload     []byte
    Attempt     int
    MaxAttempts int
    Heartbeat    HeartbeatFunc         // Extend the lease
    IsCancelled  CancellationCheckFunc // Check if cancelled
}
```

#### `WorkflowExecutor`

```go
type WorkflowExecutor interface {
    Execute(ctx context.Context, run *WorkflowRun) (interface{}, error)
}
```

## Type-Prefix Routing

Workers claim workflows by pattern using SQL `LIKE`:

```go
poller.HandleFunc("billing.invoice.v1", invoiceHandler)
poller.HandleFunc("billing.payment.v1", paymentHandler)
// → auto-detects "billing.%" prefix

poller.WithTypePrefixes("billing.%") // or set explicitly
```

## Failure & Retry

- Failed runs automatically retry with exponential backoff and jitter
- After `max_attempts`, runs move to `failed` status
- If a worker crashes, the lease expires and another worker retries

```
Attempt 1: immediate
Attempt 2: +1 minute (±10% jitter)
Attempt 3: +4 minutes (±10% jitter)
Attempt 4: failed (permanent)
```

## Heartbeat & Cancellation

For workflows that outlast the lease duration, extend the lease periodically. Check for cancellation to stop gracefully.

```go
func (e *Executor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (any, error) {
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

    // Check for cancellation periodically
    if cancelled, _ := run.IsCancelled(ctx); cancelled {
        return nil, fmt.Errorf("workflow cancelled")
    }

    // Do long-running work...
    return result, nil
}
```

Cancel from the producer side:

```go
err := client.Cancel(ctx, runID)
```

## Database Schema

### `workflow_run`

| Column | Type | Description |
|--------|------|-------------|
| `id` | UUID | Primary key |
| `type` | TEXT | Workflow type (e.g. `content.thumbnail.v1`) |
| `payload` | JSONB | Workflow arguments |
| `status` | TEXT | `pending`, `leased`, `succeeded`, `failed`, `cancelled` |
| `priority` | INT | Lower executes first (default: 100) |
| `run_at` | TIMESTAMPTZ | Earliest execution time |
| `idempotency_key` | TEXT | Unique deduplication key |
| `attempt` | INT | Current attempt number |
| `max_attempts` | INT | Retry limit (default: 3) |
| `leased_by` | TEXT | Worker ID holding the lease |
| `lease_until` | TIMESTAMPTZ | Lease expiration |
| `last_error` | TEXT | Most recent error message |
| `result` | JSONB | Workflow result |
| `created_at` | TIMESTAMPTZ | Run creation time |
| `updated_at` | TIMESTAMPTZ | Last update time |
| `deleted_at` | TIMESTAMPTZ | Soft delete (NULL = active) |

`workflow_event` stores an audit trail of all lifecycle events (created, leased, succeeded, failed, etc.). `workflow_registry` tracks registered workflow types and their runtime.

## License

MIT
