# simple-workflow — Design Document

## 1. Purpose

`simple-workflow` is a **generic, durable workflow system** for asynchronous work.

It provides:
- A **language-agnostic** way to declare work that should happen
- **Durable persistence** of workflow runs using Postgres
- **Loose coupling** between producers and workers
- Support for **multiple execution runtimes** (Go, Python)
- **Type-prefix routing** for workload partitioning
- **Heartbeat support** for long-running jobs
- **Cooperative cancellation** mechanism
- **Full audit trail** via event logging
- A foundation for workflows, notifications, webhooks, and future async tasks

It explicitly does **not**:
- Execute business logic itself
- Replace DBOS
- Require workers to be running at workflow creation time

---

## 2. Core Design Principles

### 2.1 Intent, not execution
Creating a workflow means **recording intent**, not running code.

If intent is stored successfully:
- The system guarantees **eventual execution**
- Worker availability is irrelevant at creation time

---

### 2.2 Producers never depend on workers

**Producers**:
- Insert intents
- Optionally query status
- Never call DBOS
- Never call workers

**Workers**:
- Pull work
- Execute logic
- Report results

---

### 2.3 Execution runtime is an implementation detail

The same workflow may be implemented in:
- Go today
- Python tomorrow

Producers never care.

---

## 3. High-level Architecture

```
Producer (any service)
        |
        | INSERT workflow run
        v
workflow_run (Postgres durable inbox)
        |  - status: pending → leased → succeeded/failed/cancelled
        |  - type-prefix routing support
        |
        | claim + execute (with heartbeat & cancellation)
        v
Workers (Go / Python, DBOS or non-DBOS)
        |
        | log lifecycle events
        v
workflow_event (audit trail)
```

---

## 4. Data Model

### 4.1 `workflow_run`

The core durable inbox for all async work.

**Key columns**:
- `id` – primary key (UUID)
- `type` – versioned workflow type (e.g. `content.thumbnail.v1`)
- `payload` – JSON arguments
- `status` – `pending`, `leased`, `succeeded`, `failed`, `cancelled`
- `priority` – lower executes earlier (default: 100)
- `run_at` – scheduling / retry backoff
- `idempotency_key` – unique deduplication key
- `attempt`, `max_attempts`
- `leased_by` – worker ID holding the lease
- `lease_until` – lease expiration timestamp
- `last_error`
- `result` – workflow result (JSONB)
- `created_at`, `updated_at`
- `deleted_at` – soft delete support

**Key indexes**:
- `idx_workflow_run_claim` – efficient claiming by status, run_at, priority
- `idx_workflow_run_type_prefix` – **type-prefix routing** using `text_pattern_ops`
- `idx_workflow_run_idempotency` – deduplication
- `idx_workflow_run_deleted_at` – soft delete queries

**Invariant**:
> If a row exists with `status = pending`, the work is guaranteed to eventually execute when a worker is available.

---

### 4.2 `workflow_event`

Audit trail of all workflow run lifecycle events.

**Key columns**:
- `id` – auto-incrementing primary key
- `workflow_id` – reference to `workflow_run.id`
- `event_type` – `created`, `leased`, `started`, `heartbeat`, `succeeded`, `failed`, `retried`, `cancelled`
- `data` – additional event metadata (JSONB)
- `created_at` – event timestamp

**Purpose**:
- Complete audit trail for debugging
- Compliance and observability
- Analytics on workflow execution patterns
- Track heartbeat activity for long-running jobs

---

### 4.3 `workflow_registry`

Declarative mapping of workflow name → execution runtime.

**Purpose**:
- Decouple producers from execution language
- Allow moving workflows between Go and Python without changing producers

**Columns**:
- `workflow_name` – e.g. `content.thumbnail.v1`
- `intent_type` – defaults to `workflow`
- `runtime` – `go` or `python`
- `is_enabled`
- `description`
- `created_at`, `updated_at`

Only **one runtime per workflow** at a time.

---

## 5. Workflow Naming & Type-Prefix Routing

All workflows must be versioned using dotted notation:

```
<domain>.<subdomain>.<action>.vN
```

Examples:
- `content.thumbnail.v1`
- `content.ocr.v1`
- `notify.email.v1`
- `notify.webhook.v1`
- `billing.invoice.v1`
- `billing.payment.v1`

**Type-Prefix Routing**:

Workers can claim workflows by pattern using SQL `LIKE`:

```sql
-- Worker handles all billing workflows
WHERE type LIKE 'billing.%'

-- Worker handles all content processing
WHERE type LIKE 'content.%'
```

This enables:
- **Workload partitioning** (separate billing and media workers)
- **Resource isolation** (dedicated workers for heavy jobs)
- **Team boundaries** (teams own workflow prefixes)

Rules:
- Never change behavior without bumping `vN`
- Old workflow runs must remain executable
- Use prefixes for logical grouping

---

## 6. Producer Responsibilities

Producers:
- Create workflow runs
- Ensure idempotency
- Optionally cancel workflow runs
- Never depend on worker availability

**Create workflow run**:

```sql
insert into workflow.workflow_run (
  type,
  payload,
  idempotency_key
) values (
  'content.thumbnail.v1',
  '{"content_id":"c_123", "width": 300, "height": 300}',
  'content:c_123:thumbnail:300x300'
)
on conflict (idempotency_key) do nothing
returning id;
```

**Cancel workflow run** (cooperative):

```sql
update workflow.workflow_run
set status = 'cancelled', updated_at = now()
where id = :run_id
  and status in ('pending', 'leased');
```

Success means **workflow accepted**, not executed.

---

## 7. Worker Responsibilities

Workers:
- Are runtime-specific (Go or Python)
- Claim workflow runs using type-prefix matching
- Execute logic (DBOS optional)
- Extend lease via heartbeat (long-running jobs)
- Check for cancellation
- Update workflow run status
- Log lifecycle events

### 7.1 Worker startup

Workers configure type prefixes to match:

**Go:**
```go
config := simpleworkflow.PollerConfig{
    TypePrefixes:  []string{"billing.%", "payment.%"},
    LeaseDuration: 30 * time.Second,
    WorkerID:      "billing-worker-1",
}
```

**Python:**
```python
poller = IntentPoller(
    type_prefixes=["billing.%", "payment.%"],
    lease_duration=30,
    worker_id="billing-worker-python-1"
)
```

---

### 7.2 Claiming logic with type-prefix routing

Workers claim work using row locking with pattern matching:

```sql
select id, type, payload, attempt, max_attempts
from workflow.workflow_run
where status = 'pending'
  and (type like 'billing.%' or type like 'payment.%')
  and run_at <= now()
  and deleted_at is null
order by priority asc, created_at asc
for update skip locked
limit 1;
```

Then mark as leased:

```sql
update workflow.workflow_run
set status = 'leased',
    leased_by = :worker_id,
    lease_until = now() + interval '30 seconds',
    updated_at = now()
where id = :run_id;
```

---

### 7.3 Execution with heartbeat & cancellation

**Heartbeat** (extend lease for long-running jobs):

```sql
update workflow.workflow_run
set lease_until = now() + interval '30 seconds',
    updated_at = now()
where id = :run_id and status = 'leased';
```

**Check cancellation**:

```sql
select status from workflow.workflow_run where id = :run_id;
-- if status = 'cancelled', stop execution gracefully
```

**Event logging** (audit trail):

```sql
insert into workflow.workflow_event (workflow_id, event_type, data)
values (:run_id, 'heartbeat', '{"extended_by_seconds": 30}');
```

DBOS is **used only inside workers**, never by producers.

---

## 8. Failure & Retry Model

**On failure**:
- Increment `attempt`
- Set `run_at` using **exponential backoff with jitter** (prevents thundering herd)
- Log `retried` event to `workflow_event`

**Exponential backoff with jitter**:
```
base_delay = attempt² minutes
jitter = base_delay × 10% × random()
run_at = now() + base_delay + jitter
```

Example:
- Attempt 1: immediate
- Attempt 2: ~1 minute (+10% jitter)
- Attempt 3: ~4 minutes (+10% jitter)
- Attempt 4: permanently `failed`

**If `attempt >= max_attempts`**:
- Mark as `failed` (terminal state)
- Log `failed` event to `workflow_event`

**If a worker crashes**:
- Lease expires (`lease_until` passes)
- Status automatically becomes claimable again
- Another worker retries from `attempt` count

**Cooperative cancellation**:
- Producer calls `Cancel(runID)`
- Status set to `cancelled`
- Worker checks `IsCancelled()` and stops gracefully

---

## 9. Email & Notification Workflows

Email is just another workflow type:

```
type    = 'notify.email.v1'
payload = { template, to, variables, attachments }
```

Same durability, retry, audit, and cancellation guarantees apply.

Workers can be dedicated to notifications:

```go
config := PollerConfig{
    TypePrefixes: []string{"notify.%"},  // Handles all notifications
    WorkerID:     "notification-worker",
}
```

---

## 10. Features Implemented

**Originally considered non-goals, but now implemented**:
- ✅ **Worker heartbeats** – Long-running jobs can extend their lease
- ✅ **Pattern-based routing** – Type-prefix matching with `LIKE 'billing.%'`
- ✅ **Audit trail** – Full event logging via `workflow_event` table
- ✅ **Cooperative cancellation** – Graceful workflow cancellation

**Current non-goals (intentionally avoided)**:
- Automatic load-balancing heuristics
- Complex distributed schedulers
- Tight DBOS coupling
- Worker health monitoring (use Prometheus metrics instead)

These design choices keep the system simple and focused on durability.

---

## 11. Why This Design Works Long-term

**Stability guarantees**:
- Producers are stable (simple INSERT/UPDATE operations)
- Execution can evolve (workers are independent)
- Runtimes can change (Go ↔ Python)
- Workers can disappear (leases auto-expire)
- Workload can be repartitioned (type-prefix routing)

**New capabilities added without breaking changes**:
- Type-prefix routing (uses existing `type` column + index)
- Heartbeat (updates existing `lease_until` column)
- Cancellation (uses existing `status` column)
- Audit trail (separate `workflow_event` table)

**This aligns with**:
- Stateless services
- Postgres-first durability
- Idempotent workflows
- Observable systems (full audit trail)
- DBOS's execution model

---

## 12. Summary

**simple-workflow** is the durable contract between:

> “Something should happen”  →  “Something did happen”.

Everything else — DBOS, Go vs Python, email providers — is an implementation detail.

