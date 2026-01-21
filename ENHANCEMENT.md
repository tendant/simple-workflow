# Simple Workflow – Persistence & Execution Design

## Overview

This document describes a **minimal, durable, and low‑maintenance persistence design** for `simple-workflow`.

The goals are:

- Avoid frequent database schema changes
- Keep durability and correctness
- Support multiple workers safely
- Allow future integrations without coupling
- Stay boring and understandable

The core idea is:

> **Postgres is the system of record. Workflow-specific data lives in JSON. Leasing prevents double execution. Events are optional and external.**

---

## Design Principles

1. **Stable schema** – schema changes only when the *engine* changes, not when new workflows are added
2. **Durable intent** – once a workflow is accepted, it will eventually run
3. **Stateless workers** – workers can crash, restart, or scale freely
4. **Idempotent execution** – retries are safe
5. **Time‑bound coordination** – no global locks, no leader election

---

## High‑Level Architecture

```
Client
  │
  ▼
Postgres (System of Record)
  │
  ├─ workflow_run   ← durable intent + scheduling + leasing
  ├─ workflow_event ← optional audit trail
  └─ outbox_event   ← optional integration events
  │
  ▼
Workers (pollers)
  │
  ├─ claim (lease)
  ├─ execute
  └─ update status
```

---

## Core Concepts

### Workflow Run

A **workflow run** represents one durable unit of work.

It contains:

- scheduling metadata
- execution status
- retry state
- workflow‑specific payload (JSON)

### Leasing

**Leasing** is a time‑bound claim on a workflow run by a worker.

- Only one worker can lease a run at a time
- Leases automatically expire
- No explicit unlock is required

This prevents double execution while remaining crash‑safe.

### Status & State Machine

This design uses a small, explicit state machine.

**Statuses** (minimal set):

- `pending` — eligible to be leased when `run_at <= now()`
- `leased` — currently claimed by a worker until `lease_until`
- `succeeded` — completed successfully
- `failed` — exhausted retries or hard failure

Optional statuses (if you want them):

- `cancelled`

**Valid transitions**

```
pending   → leased
leased    → succeeded
leased    → pending    (retry)
leased    → failed
pending   → cancelled  (optional)
leased    → cancelled  (optional; cooperative)
```

Notes:

- If a worker crashes, the run remains `leased` but becomes eligible for re-claim when `lease_until < now()`.
- Cancellation is intentionally cooperative: the engine can mark a run cancelled, but the worker must check and stop.

---

## Database Schema (Minimal & Stable)

### Soft delete policy

This design supports **soft deletes** for operational safety and auditability.

- Soft delete is represented by `deleted_at IS NOT NULL`.
- Soft-deleted runs are **never leased or executed**.
- Soft-deleted rows may be retained for audit/debugging and later hard-deleted by a separate retention job.

Recommended operation:

```sql
UPDATE workflow_run
SET deleted_at = now(),
    delete_reason = $reason
WHERE id = $id;
```

Notes:
- Soft delete is orthogonal to `status`. A run can be soft-deleted regardless of current status.
- If you soft-delete a `leased` run, it will stop being re-leased; the currently running worker may still finish unless you also `cancel` it (cooperative).



### `workflow_run`

```sql
CREATE TABLE workflow_run (
  id                UUID PRIMARY KEY,
  type              TEXT NOT NULL,
  status            TEXT NOT NULL,

  priority          INT NOT NULL DEFAULT 0,

  idempotency_key   TEXT,

  payload           JSONB NOT NULL,
  result            JSONB,
  error             JSONB,
  last_error        TEXT,

  attempt           INT NOT NULL DEFAULT 0,
  max_attempts      INT NOT NULL DEFAULT 3,

  run_at            TIMESTAMPTZ NOT NULL,
  lease_until       TIMESTAMPTZ,
  leased_by         TEXT,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- Soft delete
  deleted_at        TIMESTAMPTZ,
  delete_reason     TEXT,

  CONSTRAINT workflow_run_status_check
    CHECK (status IN ('pending', 'leased', 'succeeded', 'failed', 'cancelled'))
);

-- Keep updated_at accurate
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workflow_run_set_updated_at
BEFORE UPDATE ON workflow_run
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

CREATE UNIQUE INDEX IF NOT EXISTS workflow_run_idempotency_idx
  ON workflow_run(idempotency_key)
  WHERE idempotency_key IS NOT NULL
    AND deleted_at IS NULL;

-- General polling (filters by status/run_at, then orders by priority)
CREATE INDEX workflow_run_poll_idx
  ON workflow_run(status, run_at, priority DESC, lease_until)
  WHERE deleted_at IS NULL;

-- If you want workers to poll specific type prefixes efficiently
-- Note: for `LIKE 'prefix.%'`, consider `text_pattern_ops` and verify with EXPLAIN ANALYZE
CREATE INDEX workflow_run_type_poll_idx
  ON workflow_run(type text_pattern_ops, status, run_at, priority DESC, lease_until)
  WHERE deleted_at IS NULL;
```

#### Notes

- `priority` allows urgent workflows to preempt others (higher = more urgent)
- `payload` holds **all workflow-specific input**
- `result` / `error` are optional and unstructured
- `last_error` is a fast, human-readable summary for dashboards
- Adding new workflow types **does not require migrations**

Operational note: keep `payload` reasonably sized (e.g., store large blobs in object storage and reference them in JSON).

---

### Optional: `workflow_event` (Audit / Debugging)

Append-only event log for observability.

```sql
CREATE TABLE workflow_event (
  id            BIGSERIAL PRIMARY KEY,
  workflow_id   UUID NOT NULL,
  event_type    TEXT NOT NULL,
  data          JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX workflow_event_workflow_idx
  ON workflow_event(workflow_id);
```

Examples:

- created
- leased
- started
- succeeded
- failed
- retried
- cancelled

This table is **optional** but useful for UI and debugging.

Operational note (retention): consider deleting old events after an interval, or partitioning by time.

---

### Optional: `outbox_event` (Integration Boundary)

Used only if external systems need reliable events.

```sql
CREATE TABLE outbox_event (
  id            BIGSERIAL PRIMARY KEY,
  aggregate_id  UUID NOT NULL,
  event_type    TEXT NOT NULL,
  payload       JSONB NOT NULL,
  published_at  TIMESTAMPTZ
);

CREATE INDEX outbox_event_unpublished_idx
  ON outbox_event(id)
  WHERE published_at IS NULL;
```

This enables a **transactional outbox** pattern.

**Outbox polling strategy (dispatcher)**

A small dispatcher can publish events without blocking workflow execution:

```sql
SELECT *
FROM outbox_event
WHERE published_at IS NULL
ORDER BY id
LIMIT 100
FOR UPDATE SKIP LOCKED;

-- After successful publish
UPDATE outbox_event
SET published_at = now()
WHERE id = $id;
```

(If publish fails, leave `published_at` NULL and retry later.)

Operational note (retention): periodically delete old published rows, for example:

```sql
DELETE FROM outbox_event
WHERE published_at < now() - interval '7 days';
```

---

## Worker Types & Routing (Option B: type-prefix workers)

This design supports different worker types by **partitioning work via type prefixes** (e.g., `billing.*`, `media.*`).

### Naming convention

Use a dotted namespace pattern:

- `billing.invoice_charge.v1`
- `billing.refund.v1`
- `media.thumbnail.v1`
- `email.send.v1`
- `default.cleanup.v1`

Workers can then be deployed as groups that each claim a subset of types.

### Worker configuration

A worker process is configured with one or more `type` prefixes:

- `WORKER_TYPE_PREFIXES=billing.`
- `WORKER_TYPE_PREFIXES=media.,email.`
- `WORKER_TYPE_PREFIXES=default.`

A single codebase/binary can run different worker roles by changing only environment variables.

### Poll query with type-prefix filter

```sql
UPDATE workflow_run
SET
  status = 'leased',
  leased_by = $worker_id,
  lease_until = now() + interval '30 seconds'
WHERE id = (
  SELECT id
  FROM workflow_run
  WHERE status IN ('pending', 'leased')
    AND deleted_at IS NULL
    AND run_at <= now()
    AND (lease_until IS NULL OR lease_until < now())
    AND (
      type LIKE $prefix1
      OR type LIKE $prefix2
      OR type LIKE $prefix3
    )
  ORDER BY priority DESC, run_at
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
RETURNING *;
```

Notes:

- Pass `$prefixN` as e.g. `'billing.%'`.
- If a worker has no prefixes configured, it can default to `default.%`.
- Index usage for `LIKE 'prefix.%'` can be collation-dependent; confirm with `EXPLAIN ANALYZE` on realistic data.
- The `workflow_run_type_poll_idx` uses `text_pattern_ops` to improve prefix matching in common setups.

### Handler registration

Workers should register handlers explicitly by `type`.

If a worker leases a run but **no handler is registered** for that `type`, treat it as an operational misconfiguration:

- Mark the run `failed`
- Set `last_error = 'no_handler_registered'`
- Optionally emit an outbox/CloudEvent for alerting

This avoids infinite retry loops and makes the failure mode obvious.

### Scaling model

- Scale worker groups independently (e.g., more `media.*` workers during peak image processing).
- Keep a small `default.*` pool for catch-all / housekeeping.

---

## Worker Execution Flow

**Transaction isolation:** The `FOR UPDATE SKIP LOCKED` leasing pattern works correctly under Postgres default **READ COMMITTED** isolation. No `SERIALIZABLE` isolation is required.

### 1. Poll & Lease (crash-safe)

Workers atomically claim work. To be crash-safe, the poll must also consider **expired leases**.

This document **chooses a single approach**:

- `leased` is a real status
- A run can be re-claimed when `lease_until < now()` even if its status is `leased`

Operational note: under very high contention (many workers, few eligible rows), `FOR UPDATE SKIP LOCKED` may scan multiple rows before finding an unlocked one. If polling becomes slow, check lock contention via `pg_stat_activity` and `pg_locks`.

The polling query below assumes this model.

### 2. Execute

- Worker dispatches based on `type`
- Payload is passed as JSON
- Execution must be idempotent

**Cooperative cancellation (while leased)**

Cancellation is cooperative: the engine can set `status='cancelled'`, but the worker must check and stop.

Recommended check points:

- before starting any side effect
- between major steps
- on each heartbeat for long-running work

Example pattern:

```sql
-- Worker checks before continuing long work
SELECT status
FROM workflow_run
WHERE id = $id;

-- If status = 'cancelled', worker aborts and clears lease.
-- The worker must NOT change `status`; the canceller owns that decision.
UPDATE workflow_run
SET lease_until = NULL,
    leased_by = NULL
WHERE id = $id AND leased_by = $worker_id;
```

### 3. Heartbeat / Lease extension (optional)

For long‑running jobs:

```sql
UPDATE workflow_run
SET lease_until = now() + interval '30 seconds'
WHERE id = $id AND leased_by = $worker_id;
```

Lease duration should be configurable (global default + optional per-type override). Common pattern:

- default lease: 30s
- heartbeat every: 10s
- per-type lease config in code (or in payload/config table)

Operational note: long-running steps should either heartbeat and check cancellation, or split into smaller idempotent steps.

### 4. Completion

**Success**

```sql
UPDATE workflow_run
SET status = 'succeeded',
    result = $result,
    lease_until = NULL,
    leased_by = NULL
WHERE id = $id;
```

**Failure / Retry**

- Compute `backoff` in application logic (recommended) using exponential backoff + jitter.
- Example policy: `base=1s`, `cap=5m`, `raw = min(cap, base * 2^attempt)`
- Jitter example: `jitter = random(0, 0.5 * raw)` and `final = raw + jitter`

Jitter prevents a thundering herd when many runs retry at once.

```sql
UPDATE workflow_run
SET status = 'pending',
    attempt = attempt + 1,
    run_at = now() + $backoff,
    lease_until = NULL,
    leased_by = NULL,
    error = $error,
    last_error = $last_error
WHERE id = $id;
```

**After max_attempts exceeded**

- Mark as `failed`.
- Operational expectation: failed runs are not retried automatically.
- Optional: emit an outbox/CloudEvent for alerting or send to a dead-letter workflow type.

```sql
UPDATE workflow_run
SET status = 'failed',
    lease_until = NULL,
    leased_by = NULL,
    error = $error,
    last_error = $last_error
WHERE id = $id;
```

---

## Why This Avoids Schema Churn

- No per‑workflow tables
- No per‑workflow columns
- New workflows = new `type` + new JSON payload
- DB migrations only happen when:
  - scheduling changes
  - retry model changes
  - leasing logic evolves

This keeps the DB boring and stable.

---

## Where CloudEvents Fit (Optional)

CloudEvents are **not** the persistence layer.

They are useful for:

- notifying other services
- analytics
- external side effects
- alerting / dead-letter flows

Pattern:

1. DB transaction updates workflow state
2. Insert `outbox_event`
3. Dispatcher publishes a CloudEvent

Postgres remains the source of truth.

If you adopt CloudEvents, use them as a contract boundary (e.g., `invoice.validate.v1`) so you can version handlers without schema changes.

---

## Non‑Goals

This design intentionally avoids:

- exactly‑once distributed execution
- workflow DSLs
- dynamic schema generation
- external coordination systems
- event‑sourcing everywhere

Those can be added later if truly needed.

---

## Summary

- **Postgres is the durable backbone**
- **Leasing prevents double execution safely**
- **JSONB avoids schema churn**
- **Workers are stateless**
- **Events are optional and decoupled**

This keeps `simple-workflow` simple, predictable, and maintainable.

