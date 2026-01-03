# Simple-Workflow Python Library

Python implementation of the simple-workflow intent poller.

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
from simpleworkflow import WorkflowExecutor

class EmailExecutor(WorkflowExecutor):
    def execute(self, intent):
        # Parse payload
        payload = intent['payload']
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

# Database connection (must include search_path=workflow)
db_url = os.getenv('DATABASE_URL', 'postgres://postgres:postgres@localhost/workflow')
if '?' in db_url:
    db_url += '&search_path=workflow'
else:
    db_url += '?search_path=workflow'

# Create poller
supported_workflows = ['notify.email.v1']
poller = IntentPoller(
    db_url=db_url,
    supported_workflows=supported_workflows,
    worker_id='email-worker-1',
    poll_interval=2  # seconds
)

# Register executor
email_executor = EmailExecutor()
poller.register_executor('notify.email.v1', email_executor)

# Start polling (blocking)
poller.start()
```

### 3. Run in Background Thread

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

## Configuration

### Environment Variables

Create a `.env` file:

```bash
DATABASE_URL=postgres://postgres:postgres@localhost/workflow
WORKER_ID=my-python-worker
POLL_INTERVAL=2
```

### Database URL Format

The database URL must include `search_path=workflow`:

```python
# Correct
db_url = "postgres://user:pass@host/dbname?search_path=workflow"

# Also correct
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
from simpleworkflow import IntentPoller, WorkflowExecutor

logging.basicConfig(level=logging.INFO)

class NotificationExecutor(WorkflowExecutor):
    """Handles notification workflows"""

    def execute(self, intent):
        workflow_name = intent['name']
        payload = intent['payload']

        if workflow_name == 'notify.email.v1':
            return self.send_email(payload)
        elif workflow_name == 'notify.sms.v1':
            return self.send_sms(payload)
        else:
            raise ValueError(f"Unknown workflow: {workflow_name}")

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

    # Create poller
    supported_workflows = ['notify.email.v1', 'notify.sms.v1']
    poller = IntentPoller(
        db_url=db_url,
        supported_workflows=supported_workflows,
        worker_id='notification-worker',
        poll_interval=2
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
    supported_workflows: list[str],
    worker_id: str = "python-worker",
    poll_interval: int = 2
)
```

**Methods:**
- `register_executor(workflow_name: str, executor: WorkflowExecutor)` - Register an executor for a workflow
- `start()` - Start polling (blocking)
- `stop()` - Stop polling

### WorkflowExecutor

Base class for implementing workflow executors.

```python
class WorkflowExecutor(ABC):
    @abstractmethod
    def execute(self, intent: Dict[str, Any]) -> Any:
        """Execute workflow and return result"""
        pass
```

**Intent Structure:**
```python
{
    'id': 'uuid-string',
    'name': 'workflow.name.v1',
    'payload': {...},  # Your workflow-specific data
    'attempt_count': 0,
    'max_attempts': 3
}
```

## Error Handling

The poller automatically handles failures:
- Failed executions are retried with exponential backoff
- After max_attempts, intents move to 'deadletter' status
- All exceptions are logged with full traceback

## License

MIT
