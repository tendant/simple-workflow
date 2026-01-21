# API Simplification Design (Breaking Changes)

## Overview

This document outlines breaking changes to simplify the simple-workflow API while maintaining all existing functionality. The goal is to reduce boilerplate from ~40 lines to ~10 lines for common use cases.

## Design Principles

1. **Make simple things simple** - Common cases should be 1-5 lines
2. **Keep complex things possible** - Advanced features remain accessible
3. **Smart defaults everywhere** - No required configuration
4. **Connection string as source of truth** - Schema, connection details in one place
5. **Fluent APIs** - Chain methods for readability

---

## Breaking Changes Summary

| Component | Old API | New API | Breaking? |
|-----------|---------|---------|-----------|
| Client creation | `NewClient(db)` | `NewClient(connString)` or `NewClientWithDB(db)` | ✅ Yes |
| Submitting workflows | `client.Create(ctx, Intent{...})` | `client.Submit(type, payload).Execute(ctx)` | ✅ Yes |
| Poller creation | `NewPoller(db, config)` | `NewPoller(connString)` + methods | ✅ Yes |
| Handler registration | `RegisterExecutor(type, executor)` | `Handle(type, executor)` or `HandleFunc(type, fn)` | ✅ Yes |
| PollerConfig | Required, 4 fields | Optional, all defaults | ⚠️ Mostly compatible |
| WorkflowExecutor | Must implement interface | Can use functions directly | ➕ Additive |

---

## Detailed Changes

### 1. Client API Simplification

#### Current (Verbose)

```go
db, err := sql.Open("postgres", "postgres://user:pass@localhost/db?search_path=workflow")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

client := simpleworkflow.NewClient(db)

runID, err := client.Create(ctx, simpleworkflow.Intent{
    Type: "billing.invoice.v1",
    Payload: map[string]interface{}{
        "invoice_id": "INV-001",
        "amount": 99.99,
    },
    Priority: 100,
    MaxAttempts: 3,
    IdempotencyKey: "invoice:INV-001",
})
```

#### New (Simple)

```go
client := simpleworkflow.NewClient("postgres://user:pass@localhost/db?schema=workflow")
defer client.Close()

runID, err := client.Submit("billing.invoice.v1", map[string]interface{}{
    "invoice_id": "INV-001",
    "amount": 99.99,
}).
WithIdempotency("invoice:INV-001").
Execute(ctx)
```

#### Implementation

```go
// client.go

// NewClient creates a client from a connection string.
// The connection string can include ?schema=name parameter.
func NewClient(connString string) (*Client, error)

// NewClientWithDB creates a client from an existing database connection.
// Use this if you already have a connection pool.
func NewClientWithDB(db *sql.DB) *Client

// Close closes the database connection (only if opened by NewClient).
func (c *Client) Close() error

// Submit creates a new workflow run with fluent configuration.
func (c *Client) Submit(workflowType string, payload interface{}) *SubmitBuilder

// SubmitBuilder provides fluent API for workflow submission.
type SubmitBuilder struct {
    client *Client
    intent Intent
}

func (s *SubmitBuilder) WithIdempotency(key string) *SubmitBuilder
func (s *SubmitBuilder) WithPriority(priority int) *SubmitBuilder
func (s *SubmitBuilder) WithMaxAttempts(attempts int) *SubmitBuilder
func (s *SubmitBuilder) RunAfter(t time.Time) *SubmitBuilder
func (s *SubmitBuilder) RunIn(d time.Duration) *SubmitBuilder
func (s *SubmitBuilder) Execute(ctx context.Context) (string, error)

// Backward compatibility: Keep Create() but mark as deprecated
// Deprecated: Use Submit() instead.
func (c *Client) Create(ctx context.Context, intent Intent) (string, error)
```

---

### 2. Poller API Simplification

#### Current (Verbose)

```go
db, err := sql.Open("postgres", dbURL)
if err != nil {
    log.Fatal(err)
}
defer db.Close()

config := simpleworkflow.PollerConfig{
    TypePrefixes:  []string{"billing.%"},
    LeaseDuration: 30 * time.Second,
    PollInterval:  2 * time.Second,
    WorkerID:      "worker-1",
}

poller := simpleworkflow.NewPoller(db, config)

type InvoiceExecutor struct{}
func (e *InvoiceExecutor) Execute(ctx context.Context, run *WorkflowRun) (interface{}, error) {
    // Implementation
}

poller.RegisterExecutor("billing.invoice.v1", &InvoiceExecutor{})

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

go func() {
    poller.Start(ctx)
}()

// Wait for signal...
```

#### New (Simple)

```go
poller := simpleworkflow.NewPoller("postgres://user:pass@localhost/db?schema=workflow")
defer poller.Close()

poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
    // Implementation - payload already parsed in run.Payload
    return result, nil
})

poller.Start(ctx) // Blocks until ctx is cancelled
```

#### With Options

```go
poller := simpleworkflow.NewPoller(connString).
    WithWorkerID("custom-worker").
    WithLeaseDuration(60 * time.Second).
    WithPollInterval(5 * time.Second)

poller.HandleFunc("billing.invoice.v1", invoiceHandler)
poller.Handle("billing.payment.v1", &PaymentExecutor{}) // Still supports executors

poller.Start(ctx)
```

#### Implementation

```go
// poller.go

// NewPoller creates a poller from a connection string.
// Type prefixes are auto-detected from registered handlers.
func NewPoller(connString string) (*Poller, error)

// NewPollerWithDB creates a poller from an existing database connection.
func NewPollerWithDB(db *sql.DB) *Poller

// WithWorkerID sets a custom worker ID (default: hostname-pid).
func (p *Poller) WithWorkerID(id string) *Poller

// WithLeaseDuration sets lease duration (default: 30s).
func (p *Poller) WithLeaseDuration(d time.Duration) *Poller

// WithPollInterval sets poll interval (default: 2s).
func (p *Poller) WithPollInterval(d time.Duration) *Poller

// WithTypePrefixes explicitly sets type prefixes (overrides auto-detection).
func (p *Poller) WithTypePrefixes(prefixes ...string) *Poller

// HandleFunc registers a function handler for a workflow type.
func (p *Poller) HandleFunc(workflowType string, fn func(context.Context, *WorkflowRun) (interface{}, error)) *Poller

// Handle registers a WorkflowExecutor for a workflow type.
func (p *Poller) Handle(workflowType string, executor WorkflowExecutor) *Poller

// Close closes the database connection (only if opened by NewPoller).
func (p *Poller) Close() error

// Start begins polling and blocks until ctx is cancelled.
func (p *Poller) Start(ctx context.Context)

// Backward compatibility
// Deprecated: Use NewPoller(connString) or NewPollerWithDB(db) instead.
func NewPoller(db *sql.DB, config PollerConfig) *Poller
```

---

### 3. All-in-One API for Simple Apps

For applications that both submit AND process workflows (most common case):

```go
wf := simpleworkflow.New("postgres://user:pass@localhost/db?schema=workflow")
defer wf.Close()

// Register handlers
wf.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *WorkflowRun) (interface{}, error) {
    // Process invoice
    return result, nil
})

// Submit workflows
runID, err := wf.Submit("billing.invoice.v1", payload).Execute(ctx)

// Start embedded worker
wf.StartWorker(ctx)
```

#### Implementation

```go
// workflow.go (NEW FILE)

// Workflow combines Client and Poller for applications that do both.
type Workflow struct {
    client *Client
    poller *Poller
    db     *sql.DB
    ownsDB bool
}

// New creates a Workflow from a connection string.
func New(connString string) (*Workflow, error)

// NewWithDB creates a Workflow from an existing database connection.
func NewWithDB(db *sql.DB) *Workflow

// AutoMigrate runs database migrations on Build().
func (w *Workflow) AutoMigrate() *Workflow

// HandleFunc registers a workflow handler.
func (w *Workflow) HandleFunc(workflowType string, fn func(context.Context, *WorkflowRun) (interface{}, error)) *Workflow

// Handle registers a WorkflowExecutor.
func (w *Workflow) Handle(workflowType string, executor WorkflowExecutor) *Workflow

// Submit creates a workflow run.
func (w *Workflow) Submit(workflowType string, payload interface{}) *SubmitBuilder

// Cancel cancels a workflow run.
func (w *Workflow) Cancel(ctx context.Context, runID string) error

// StartWorker starts the embedded worker (blocks).
func (w *Workflow) StartWorker(ctx context.Context)

// Close closes database connections.
func (w *Workflow) Close() error
```

---

### 4. Auto-Migration Support

Add optional auto-migration that runs on startup:

```go
// Client with auto-migration
client := simpleworkflow.NewClient(connString).WithAutoMigrate()

// Poller with auto-migration
poller := simpleworkflow.NewPoller(connString).WithAutoMigrate()

// Workflow with auto-migration
wf := simpleworkflow.New(connString).AutoMigrate()
```

Implementation uses embedded SQL files:

```go
// migrate.go (NEW FILE)

import _ "embed"

//go:embed migrations/20260103000001_create_workflow_run.sql
var migration001 string

//go:embed migrations/20260121000001_create_workflow_event.sql
var migration002 string

func runMigrations(db *sql.DB, schema string) error {
    // Check if migrations table exists
    // Run pending migrations
    // Return error if any migration fails
}
```

---

### 5. Smart Defaults

All configuration becomes optional with smart defaults:

```go
// PollerConfig - all fields now optional
type PollerConfig struct {
    TypePrefixes  []string      // Default: auto-detect from handlers
    LeaseDuration time.Duration // Default: 30s
    PollInterval  time.Duration // Default: 2s
    WorkerID      string        // Default: hostname-pid
}

// DefaultPollerConfig returns config with smart defaults
func DefaultPollerConfig() PollerConfig {
    hostname, _ := os.Hostname()
    return PollerConfig{
        TypePrefixes:  nil, // Auto-detect
        LeaseDuration: 30 * time.Second,
        PollInterval:  2 * time.Second,
        WorkerID:      fmt.Sprintf("%s-%d", hostname, os.Getpid()),
    }
}
```

---

### 6. Connection String Handling

Support both explicit schema parameter and search_path:

```go
// Option 1: Custom schema parameter (simpler)
"postgres://user:pass@localhost/db?schema=workflow"

// Option 2: Standard search_path (compatible with psql)
"postgres://user:pass@localhost/db?search_path=workflow"

// Option 3: No schema specified (defaults to "workflow")
"postgres://user:pass@localhost/db"
```

Implementation:

```go
// connstring.go (NEW FILE)

// ParseConnString extracts schema and returns modified connection string.
func ParseConnString(connString, defaultSchema string) (modifiedConn, schema string, err error) {
    // Parse URL
    // Look for ?schema= or ?search_path=
    // Inject search_path if not present
    // Return (connection string with search_path, schema name, error)
}
```

---

## Migration Path for Existing Users

### Breaking Changes

1. **`NewClient(db)` → `NewClient(connString)` or `NewClientWithDB(db)`**
   - Migration: Use `NewClientWithDB(db)` to keep existing behavior

2. **`client.Create(ctx, Intent{...})` → `client.Submit(...).Execute(ctx)`**
   - Migration: Keep deprecated `Create()` method for 1 version

3. **`NewPoller(db, config)` → `NewPoller(connString)`**
   - Migration: Add new signature, keep old as deprecated

4. **`poller.RegisterExecutor()` → `poller.Handle()`**
   - Migration: Keep `RegisterExecutor()` as alias

### Deprecation Timeline

**Version 2.0 (Breaking Release)**
- Introduce all new APIs
- Mark old APIs as deprecated
- Keep both working

**Version 2.1-2.x**
- Encourage migration via documentation
- Both APIs fully supported

**Version 3.0 (Optional Future Breaking Release)**
- Remove deprecated APIs
- Clean up codebase

---

## Example Transformations

### Before (Current API - 40 lines)

```go
package main

import (
    "context"
    "database/sql"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    simpleworkflow "github.com/tendant/simple-workflow"
    _ "github.com/lib/pq"
)

type InvoiceExecutor struct{}

func (e *InvoiceExecutor) Execute(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
    log.Printf("Processing invoice")
    return map[string]string{"status": "completed"}, nil
}

func main() {
    dbURL := os.Getenv("DATABASE_URL")
    db, _ := sql.Open("postgres", dbURL)
    defer db.Close()

    config := simpleworkflow.PollerConfig{
        TypePrefixes:  []string{"billing.%"},
        LeaseDuration: 30 * time.Second,
        PollInterval:  2 * time.Second,
        WorkerID:      "worker-1",
    }

    poller := simpleworkflow.NewPoller(db, config)
    poller.RegisterExecutor("billing.invoice.v1", &InvoiceExecutor{})

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go func() {
        poller.Start(ctx)
    }()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
    <-sigChan
    cancel()
}
```

### After (Simplified API - 15 lines)

```go
package main

import (
    "context"
    "log"

    simpleworkflow "github.com/tendant/simple-workflow"
)

func main() {
    poller, _ := simpleworkflow.NewPoller("postgres://pas:pwd@localhost/pas?schema=workflow")
    defer poller.Close()

    poller.HandleFunc("billing.invoice.v1", func(ctx context.Context, run *simpleworkflow.WorkflowRun) (interface{}, error) {
        log.Printf("Processing invoice")
        return map[string]string{"status": "completed"}, nil
    })

    poller.Start(context.Background()) // Blocks, handles Ctrl+C
}
```

**Reduction: 40 lines → 15 lines (62% reduction)**

---

## Implementation Order

1. **Phase 1: Core Simplifications** (Week 1)
   - Add `NewClient(connString)` and `NewClientWithDB(db)`
   - Add `Client.Submit()` with `SubmitBuilder`
   - Add `NewPoller(connString)` and `NewPollerWithDB(db)`
   - Add connection string parsing
   - Add smart defaults to `PollerConfig`

2. **Phase 2: Handler Simplifications** (Week 1)
   - Add `Poller.HandleFunc()` for function handlers
   - Add `Poller.Handle()` (rename from `RegisterExecutor`)
   - Auto-detect type prefixes from handlers
   - Keep `RegisterExecutor()` as deprecated alias

3. **Phase 3: All-in-One API** (Week 2)
   - Create `Workflow` struct combining Client + Poller
   - Add `New()`, `AutoMigrate()`, `StartWorker()`
   - Test embedded worker scenarios

4. **Phase 4: Auto-Migration** (Week 2)
   - Embed SQL migrations in binary
   - Add migration runner
   - Add `WithAutoMigrate()` options

5. **Phase 5: Documentation & Examples** (Week 3)
   - Update README with new API first
   - Update all examples to use new API
   - Create migration guide
   - Add deprecation notices to old API

---

## Testing Strategy

1. **Backward Compatibility Tests**
   - Old API still works with deprecation warnings
   - Mixed usage (old Client + new Poller) works

2. **New API Tests**
   - Connection string parsing
   - Smart defaults
   - Auto-migration
   - Handler registration (both function and executor)
   - Type-prefix auto-detection

3. **Integration Tests**
   - End-to-end with simplified API
   - Embedded worker scenarios
   - Auto-migration in clean database

---

## Success Criteria

1. **Code Reduction**: 60%+ reduction in boilerplate for common cases
2. **Backward Compatibility**: Old API still works (with deprecation warnings)
3. **Feature Parity**: All existing features accessible via new API
4. **Documentation**: README shows new API first, old API in "Advanced" section
5. **Examples Updated**: All examples use new simplified API

