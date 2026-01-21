# Simple-Workflow Python Library

Python implementation of the simple-workflow worker (poller).

## Installation

```bash
pip install -r requirements.txt
```

Or install the package:

```bash
pip install -e .
```

## Quick Start

### 1. Implement a Workflow Executor

```python
from simpleworkflow import WorkflowExecutor, WorkflowRun

class EmailExecutor(WorkflowExecutor):
    def execute(self, run: WorkflowRun):
        # Access workflow run data
        payload = run.payload
        to_email = payload['to']
        subject = payload['subject']
        body = payload['body']

        # Send email (your implementation)
        print(f"Sending email to {to_email}: {subject}")
        # send_email(to_email, subject, body)

        return {"sent": True, "to": to_email}
```

### 2. Create and Start the Poller

```python
import os
from simpleworkflow import IntentPoller

# Database connection (must include search_path parameter)
db_url = os.getenv('DATABASE_URL', 'postgres://postgres:postgres@localhost/workflow')
if '?' in db_url:
    db_url += '&search_path=workflow'
else:
    db_url += '?search_path=workflow'

# Create poller with type-prefix routing
type_prefixes = ['notify.%']  # Handles all notification workflows
poller = IntentPoller(
    db_url=db_url,
    type_prefixes=type_prefixes,
    worker_id='email-worker-1',
    poll_interval=2,         # seconds
    lease_duration=30        # seconds
)

# Register executor
email_executor = EmailExecutor()
poller.register_executor('notify.email.v1', email_executor)

# Start polling (blocking)
poller.start()
```

### 3. Long-Running Workflows with Heartbeat

```python
import time
from simpleworkflow import WorkflowExecutor, WorkflowRun

class VideoProcessingExecutor(WorkflowExecutor):
    def execute(self, run: WorkflowRun):
        payload = run.payload
        video_id = payload['video_id']

        # Long processing task
        for i in range(10):
            print(f"Processing video {video_id}... step {i+1}/10")
            time.sleep(5)  # Simulate work

            # Extend lease every 5 seconds (lease_duration is 30s)
            run.heartbeat(30)

            # Check for cancellation
            if run.is_cancelled():
                print("Workflow cancelled, stopping...")
                raise Exception("Workflow cancelled by user")

        return {"video_id": video_id, "status": "processed"}
```

### 4. Run in Background Thread

```python
import threading

# Start poller in background thread
poller_thread = threading.Thread(target=poller.start, daemon=True)
poller_thread.start()

# Your application continues here
print("Poller running in background...")

# Keep application alive
try:
    while True:
        time.sleep(1)
except KeyboardInterrupt:
    poller.stop()
    print("Poller stopped")
```

## Type-Prefix Routing

Workers use type-prefix patterns to claim specific workflows:

```python
# Worker handles all billing workflows
type_prefixes = ['billing.%']

# Worker handles billing and payment workflows
type_prefixes = ['billing.%', 'payment.%']

# Worker handles all notifications
type_prefixes = ['notify.%']
```

This enables:
- **Workload partitioning** - Separate workers for different domains
- **Resource isolation** - Dedicated workers for heavy tasks
- **Team boundaries** - Teams own their workflow prefixes

## Heartbeat & Cancellation

### Heartbeat for Long-Running Workflows

```python
def execute(self, run: WorkflowRun):
    # Extend lease before long operation
    run.heartbeat(60)  # Extend by 60 seconds

    # Do long-running work...
    process_large_file()

    # Extend again if needed
    run.heartbeat(60)

    return result
```

### Cooperative Cancellation

```python
def execute(self, run: WorkflowRun):
    for i in range(100):
        # Check for cancellation
        if run.is_cancelled():
            print("Workflow cancelled, cleaning up...")
            cleanup()
            raise Exception("Workflow cancelled")

        # Continue work...
        process_chunk(i)

    return result
```

## Configuration

### Environment Variables

Create a `.env` file:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost/workflow
WORKER_ID=my-python-worker
WORKER_TYPE_PREFIXES=billing.%,payment.%
WORKER_LEASE_DURATION=30
POLL_INTERVAL=2
```

### Database URL Format

The database URL must include `search_path` parameter:

```python
# Correct - using workflow schema
db_url = "postgres://user:pass@host/dbname?search_path=workflow"

# Correct - using custom schema
db_url = "postgres://user:pass@host/dbname?search_path=myschema"

# With SSL
db_url = "postgres://user:pass@host/dbname?sslmode=require&search_path=workflow"
```

## Complete Example

```python
#!/usr/bin/env python3
"""
Example worker that processes notification workflows
"""

import os
import logging
from simpleworkflow import IntentPoller, WorkflowExecutor, WorkflowRun

logging.basicConfig(level=logging.INFO)

class NotificationExecutor(WorkflowExecutor):
    """Handles notification workflows"""

    def execute(self, run: WorkflowRun):
        workflow_type = run.type
        payload = run.payload

        if workflow_type == 'notify.email.v1':
            return self.send_email(payload)
        elif workflow_type == 'notify.sms.v1':
            return self.send_sms(payload)
        else:
            raise ValueError(f"Unknown workflow: {workflow_type}")

    def send_email(self, payload):
        to = payload['to']
        subject = payload['subject']
        body = payload['body']

        # Your email sending logic here
        print(f"Sending email to {to}: {subject}")

        return {
            "sent": True,
            "to": to,
            "method": "email"
        }

    def send_sms(self, payload):
        phone = payload['phone']
        message = payload['message']

        # Your SMS sending logic here
        print(f"Sending SMS to {phone}: {message}")

        return {
            "sent": True,
            "to": phone,
            "method": "sms"
        }

def main():
    # Database configuration
    db_url = os.getenv('DATABASE_URL', 'postgres://postgres:postgres@localhost/workflow')
    if '?' in db_url:
        db_url += '&search_path=workflow'
    else:
        db_url += '?search_path=workflow'

    # Create poller with type-prefix routing
    type_prefixes = ['notify.%']  # Handles all notify.* workflows
    poller = IntentPoller(
        db_url=db_url,
        type_prefixes=type_prefixes,
        worker_id='notification-worker',
        poll_interval=2,
        lease_duration=30
    )

    # Register executor
    executor = NotificationExecutor()
    poller.register_executor('notify.email.v1', executor)
    poller.register_executor('notify.sms.v1', executor)

    # Start polling
    print("Starting notification worker...")
    poller.start()

if __name__ == '__main__':
    main()
```

## API Reference

### IntentPoller

```python
IntentPoller(
    db_url: str,
    type_prefixes: list[str],
    worker_id: str = "python-worker",
    poll_interval: int = 2,
    lease_duration: int = 30,
    metrics: Optional[MetricsCollector] = None
)
```

**Parameters:**
- `db_url` - PostgreSQL connection string (must include `search_path` parameter)
- `type_prefixes` - List of type prefixes to match (e.g., `["billing.%", "notify.%"]`)
- `worker_id` - Unique identifier for this worker
- `poll_interval` - Polling interval in seconds (default: 2)
- `lease_duration` - Lease duration in seconds (default: 30)
- `metrics` - Optional Prometheus metrics collector

**Methods:**
- `register_executor(workflow_type: str, executor: WorkflowExecutor)` - Register an executor
- `start()` - Start polling (blocking)
- `stop()` - Stop polling

### WorkflowRun

Represents a claimed workflow run with heartbeat and cancellation support.

```python
class WorkflowRun:
    id: str                 # Workflow run ID
    type: str               # Workflow type (e.g., 'billing.invoice.v1')
    payload: dict           # JSON payload
    attempt: int            # Current attempt number (0-indexed)
    max_attempts: int       # Maximum retry attempts

    def heartbeat(self, duration_seconds: int = 30):
        """Extend the lease by duration_seconds"""

    def is_cancelled(self) -> bool:
        """Check if workflow has been cancelled"""
```

### WorkflowExecutor

Base class for implementing workflow executors.

```python
class WorkflowExecutor(ABC):
    @abstractmethod
    def execute(self, run: WorkflowRun) -> Any:
        """Execute workflow and return result"""
        pass
```

## Error Handling & Retry

The poller automatically handles failures:
- Failed executions are retried with **exponential backoff + jitter**
- After `max_attempts`, workflows move to `failed` status
- All exceptions are logged with full traceback
- Worker crashes are handled via lease expiration

**Retry Schedule:**
```
Attempt 1: immediate
Attempt 2: ~1 minute (+10% jitter)
Attempt 3: ~4 minutes (+10% jitter)
Attempt 4+: permanently failed
```

## License

MIT
