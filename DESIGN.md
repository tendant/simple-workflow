# simple-workflow — Design Document

## 1. Purpose

`simple-workflow` is a **generic, durable intent system** for asynchronous work.

It provides:
- A **language-agnostic** way to declare work that should happen
- **Durable persistence** of intent using Postgres
- **Loose coupling** between producers and workers
- Support for **multiple execution runtimes** (Go, Python)
- A foundation for workflows, notifications, webhooks, and future async tasks

It explicitly does **not**:
- Execute business logic itself
- Replace DBOS
- Require workers to be running at intent creation time

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
        | INSERT intent
        v
workflow_intent (Postgres durable inbox)
        |
        | claim + execute
        v
Workers (Go / Python, DBOS or non-DBOS)
```

---

## 4. Data Model

### 4.1 `workflow_intent`

The core durable inbox for all async work.

**Key columns**:
- `intent_id` – primary key
- `intent_type` – `workflow`, `email`, `webhook`, etc.
- `name` – versioned intent name (e.g. `content.thumbnail.v1`)
- `payload` – JSON arguments
- `status` – `pending`, `running`, `succeeded`, `failed`, `deadletter`
- `priority` – lower executes earlier
- `run_after` – scheduling / retry backoff
- `idempotency_key` – unique deduplication key
- `attempt_count`, `max_attempts`
- `claimed_by`, `lease_expires_at`
- `last_error`
- `created_at`, `updated_at`

**Invariant**:
> If a row exists with `status = pending`, the work is guaranteed to eventually execute when a worker is available.

---

### 4.2 `workflow_registry`

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

## 5. Workflow Naming & Versioning

All workflows must be versioned:

```
<domain>.<action>.vN
```

Examples:
- `content.thumbnail.v1`
- `content.ocr.v1`
- `notify.email.v1`
- `notify.webhook.v1`

Rules:
- Never change behavior without bumping `vN`
- Old intents must remain executable

---

## 6. Producer Responsibilities

Producers:
- Insert intents
- Ensure idempotency
- Never depend on worker availability

**Conceptual example**:

```sql
insert into workflow_intent (
  intent_type,
  name,
  payload,
  idempotency_key
) values (
  'workflow',
  'content.thumbnail.v1',
  '{"content_id":"c_123"}',
  'content:c_123:thumbnail:v1'
)
on conflict (idempotency_key) do nothing;
```

Success means **intent accepted**, not executed.

---

## 7. Worker Responsibilities

Workers:
- Are runtime-specific (Go or Python)
- Claim intents
- Execute logic (DBOS optional)
- Update intent status

### 7.1 Worker startup

Workers query the registry to discover supported workflows:

```sql
select workflow_name
from workflow_registry
where runtime = 'python'
  and is_enabled = true;
```

---

### 7.2 Claiming logic

Workers claim work using row locking:

```sql
select *
from workflow_intent
where status = 'pending'
  and intent_type = 'workflow'
  and name = any(:supported_workflows)
  and run_after <= now()
order by priority asc, created_at asc
for update skip locked
limit 1;
```

---

### 7.3 Execution

- `intent_type = workflow`: execute DBOS workflow (optional)
- `intent_type = email`: send email
- `intent_type = webhook`: call webhook

DBOS is **used only inside workers**, never by producers.

---

## 8. Failure & Retry Model

- On failure:
  - increment `attempt_count`
  - set `run_after` using backoff
- If `attempt_count >= max_attempts`:
  - mark as `deadletter`
- If a worker crashes:
  - lease expires
  - another worker retries

---

## 9. Email & Notification Intents

Email is just another intent:

```
intent_type = 'email'
name        = 'notify.email.v1'
payload     = { template, to, variables, attachments }
```

Same durability, retry, and audit guarantees apply.

---

## 10. Non-goals (Intentionally Avoided)

- Worker heartbeats
- Load-balancing heuristics
- Pattern-based routing
- Distributed schedulers
- Tight DBOS coupling

These can be added later without breaking data.

---

## 11. Why This Design Works Long-term

- Producers are stable
- Execution can evolve
- Runtimes can change
- Workers can disappear
- New intent types require no schema changes

This aligns with:
- stateless services
- Postgres-first durability
- idempotent workflows
- DBOS’s execution model

---

## 12. Summary

**simple-workflow** is the durable contract between:

> “Something should happen”  →  “Something did happen”.

Everything else — DBOS, Go vs Python, email providers — is an implementation detail.

