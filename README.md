# simple-workflow

A generic, durable intent system for asynchronous work in Go.

## Overview

`simple-workflow` provides a **language-agnostic** way to declare work that should happen, with **durable persistence** using PostgreSQL and **loose coupling** between producers and workers.

**Core Principle**: Creating a workflow means **recording intent**, not running code. If intent is stored successfully, the system guarantees eventual execution.

## Features

- 🔒 **Durable persistence** - Intents survive crashes and restarts
- 🔄 **Automatic retries** - Failed intents retry with exponential backoff
- 🎯 **Idempotency** - Duplicate requests don't create duplicate work
- 📊 **Observable** - Track workflow status in PostgreSQL
- 🚀 **Language-agnostic** - Producers don't know execution runtime
- ⚖️ **Priority & scheduling** - Control execution order and timing

## Installation

```bash
go get github.com/tendant/simple-workflow
```

## Quick Start

### 1. Apply Migrations

Apply the SQL migrations to your PostgreSQL database using the Makefile:

```bash
# Show help
make help

# Apply all migrations (with defaults: localhost, pas user, pas db)
make migrate-up

# Check migration status
make migrate-status

# Custom database credentials
make migrate-up DB_HOST=myhost DB_USER=myuser DB_PASSWORD=mypass DB_NAME=mydb

# Rollback last migration
make migrate-down
```

Or manually with goose:

```bash
goose -dir migrations postgres "host=localhost user=pas dbname=pas password=pwd sslmode=disable search_path=workflow" up
```

### 2. Producer: Insert Intents

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

    // Create intent client
    client := simpleworkflow.NewClient(db)

    // Insert workflow intent
    intentID, err := client.Create(context.Background(), simpleworkflow.Intent{
        Name: "content.thumbnail.v1",
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

    println("Created intent:", intentID)
}
```

### 3. Worker: Execute Intents

```go
package main

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"

    simpleworkflow "github.com/tendant/simple-workflow"
    _ "github.com/lib/pq"
)

// ThumbnailExecutor implements simpleworkflow.WorkflowExecutor
type ThumbnailExecutor struct{}

func (e *ThumbnailExecutor) Execute(ctx context.Context, intent *simpleworkflow.WorkflowIntent) (interface{}, error) {
    var params struct {
        ContentID string `json:"content_id"`
        Width     int    `json:"width"`
        Height    int    `json:"height"`
    }
    if err := json.Unmarshal(intent.Payload, &params); err != nil {
        return nil, err
    }

    // Execute workflow logic
    fmt.Printf("Generating thumbnail for %s (%dx%d)\n", params.ContentID, params.Width, params.Height)

    return map[string]string{"status": "completed"}, nil
}

func main() {
    db, _ := sql.Open("postgres", "postgres://user:pass@localhost/db?sslmode=disable")
    defer db.Close()

    // Create poller
    supportedWorkflows := []string{"content.thumbnail.v1"}
    poller := simpleworkflow.NewPoller(db, supportedWorkflows)

    // Register executor
    poller.RegisterExecutor("content.thumbnail.v1", &ThumbnailExecutor{})

    // Start polling
    poller.Start(context.Background())
}
```

## Architecture

```
┌─────────────┐
│  Producer   │  Inserts intents
│  (PAS API)  │  - Never calls workers
└──────┬──────┘  - Never depends on worker availability
       │
       │ INSERT intent
       ↓
┌──────────────────────────┐
│  workflow_intent table   │  Durable inbox
│  (PostgreSQL)            │  - status: pending → running → succeeded
└──────┬───────────────────┘  - priority, run_after, idempotency_key
       │
       │ claim + execute
       ↓
┌──────────────┐
│   Worker     │  Polls and executes
│ (Go/Python)  │  - Claims intents
└──────────────┘  - Reports results
```

## API Reference

### Producer API

#### `Client`

```go
type Client struct { /* ... */ }

func NewClient(db *sql.DB) *Client
func (c *Client) Create(ctx context.Context, intent Intent) (string, error)
```

#### `Intent`

```go
type Intent struct {
    Name           string      // Workflow name (e.g. "content.thumbnail.v1")
    Payload        interface{} // JSON-encodable data
    Priority       int         // Lower executes first (default: 100)
    RunAfter       time.Time   // Schedule for future (default: now)
    IdempotencyKey string      // Optional deduplication key
    MaxAttempts    int         // Retry limit (default: 3)
}
```

### Worker API

#### `Poller`

```go
type Poller struct { /* ... */ }

func NewPoller(db *sql.DB, supportedWorkflows []string) *Poller
func (p *Poller) RegisterExecutor(workflowName string, executor WorkflowExecutor)
func (p *Poller) SetWorkerID(workerID string)
func (p *Poller) SetPollInterval(interval time.Duration)
func (p *Poller) Start(ctx context.Context)
func (p *Poller) Stop()
```

#### `WorkflowExecutor`

```go
type WorkflowExecutor interface {
    Execute(ctx context.Context, intent *WorkflowIntent) (interface{}, error)
}
```

## Workflow Naming

All workflows must be versioned using the pattern:

```
<domain>.<action>.vN
```

Examples:
- `content.thumbnail.v1`
- `content.ocr.v1`
- `notify.email.v1`

Rules:
- Never change behavior without bumping `vN`
- Old intents must remain executable

## Failure & Retry

- Failed intents automatically retry with exponential backoff
- After `max_attempts`, intents move to `deadletter` status
- If a worker crashes, the lease expires and another worker retries

```
Attempt 1: immediate
Attempt 2: +1 minute
Attempt 3: +4 minutes
Attempt 4: deadletter
```

## Database Schema

### `workflow_intent`

| Column | Type | Description |
|--------|------|-------------|
| `intent_id` | UUID | Primary key |
| `name` | TEXT | Workflow name (e.g. `content.thumbnail.v1`) |
| `payload` | JSONB | Workflow arguments |
| `status` | TEXT | `pending`, `running`, `succeeded`, `failed`, `deadletter` |
| `priority` | INT | Lower executes first (default: 100) |
| `run_after` | TIMESTAMPTZ | Earliest execution time |
| `idempotency_key` | TEXT | Unique deduplication key |
| `attempt_count` | INT | Number of execution attempts |
| `max_attempts` | INT | Retry limit (default: 3) |
| `claimed_by` | TEXT | Worker ID |
| `lease_expires_at` | TIMESTAMPTZ | Lease expiration |
| `last_error` | TEXT | Most recent error message |
| `result` | JSONB | Workflow result |
| `created_at` | TIMESTAMPTZ | Intent creation time |
| `updated_at` | TIMESTAMPTZ | Last update time |

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
DB_USER=pas
DB_PASSWORD=pwd
DB_NAME=pas
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
SELECT status, COUNT(*) FROM workflow_intent GROUP BY status;

-- Recent failures
SELECT name, last_error, created_at
FROM workflow_intent
WHERE status = 'failed'
ORDER BY created_at DESC
LIMIT 10;

-- Deadletter queue
SELECT * FROM workflow_intent WHERE status = 'deadletter';
```

## License

MIT
