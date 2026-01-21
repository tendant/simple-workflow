#!/usr/bin/env python3
"""
Notification Worker with Cancellation Support

This example demonstrates:
- Multiple workflow types with single executor
- Cooperative cancellation checking
- Type-prefix routing for all notifications
- Error handling and retry
"""

import os
import sys
import time
import logging
from pathlib import Path

# Add parent directory to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent.parent / 'python'))

from simpleworkflow import IntentPoller, WorkflowExecutor, WorkflowRun

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


class NotificationExecutor(WorkflowExecutor):
    """
    Handles various notification types: email, SMS, push notifications.
    """

    def execute(self, run: WorkflowRun):
        workflow_type = run.type
        payload = run.payload

        logger.info(f"Processing notification: {workflow_type}")
        logger.info(f"Attempt: {run.attempt + 1}/{run.max_attempts}")

        # Route to appropriate handler
        if workflow_type == 'notify.email.v1':
            return self._send_email(run, payload)
        elif workflow_type == 'notify.sms.v1':
            return self._send_sms(run, payload)
        elif workflow_type == 'notify.push.v1':
            return self._send_push(run, payload)
        else:
            raise ValueError(f"Unknown notification type: {workflow_type}")

    def _send_email(self, run: WorkflowRun, payload: dict):
        """Send email notification"""
        to = payload['to']
        subject = payload['subject']
        body = payload.get('body', '')

        logger.info(f"Sending email to {to}: {subject}")

        # Simulate email sending with cancellation checks
        for i in range(5):
            time.sleep(1)  # Simulate work

            # Check for cancellation periodically
            if run.is_cancelled():
                logger.warning("Email sending cancelled")
                raise Exception("Email sending cancelled by user")

        logger.info(f"Email sent successfully to {to}")

        return {
            "status": "sent",
            "method": "email",
            "recipient": to,
            "subject": subject
        }

    def _send_sms(self, run: WorkflowRun, payload: dict):
        """Send SMS notification"""
        phone = payload['phone']
        message = payload['message']

        logger.info(f"Sending SMS to {phone}")

        # Simulate SMS API call
        time.sleep(2)

        # Check for cancellation
        if run.is_cancelled():
            logger.warning("SMS sending cancelled")
            raise Exception("SMS sending cancelled by user")

        logger.info(f"SMS sent successfully to {phone}")

        return {
            "status": "sent",
            "method": "sms",
            "recipient": phone,
            "message_length": len(message)
        }

    def _send_push(self, run: WorkflowRun, payload: dict):
        """Send push notification"""
        user_id = payload['user_id']
        title = payload['title']
        body = payload.get('body', '')

        logger.info(f"Sending push notification to user {user_id}: {title}")

        # Simulate push notification service
        time.sleep(1)

        # Check for cancellation
        if run.is_cancelled():
            logger.warning("Push notification cancelled")
            raise Exception("Push notification cancelled by user")

        logger.info(f"Push notification sent to user {user_id}")

        return {
            "status": "sent",
            "method": "push",
            "user_id": user_id,
            "title": title
        }


def main():
    # Database configuration
    db_url = os.getenv('DATABASE_URL')
    if not db_url:
        db_url = 'postgres://pas:pwd@localhost/pas'

    # Ensure search_path is set
    if '?' in db_url:
        db_url += '&search_path=workflow'
    else:
        db_url += '?search_path=workflow'

    # Create poller for ALL notification workflows
    type_prefixes = ['notify.%']  # Handles notify.email.v1, notify.sms.v1, notify.push.v1, etc.
    poller = IntentPoller(
        db_url=db_url,
        type_prefixes=type_prefixes,
        worker_id='notification-worker-python-1',
        poll_interval=2,
        lease_duration=30
    )

    # Register single executor for all notification types
    executor = NotificationExecutor()
    poller.register_executor('notify.email.v1', executor)
    poller.register_executor('notify.sms.v1', executor)
    poller.register_executor('notify.push.v1', executor)

    # Start polling
    logger.info("Starting notification worker...")
    logger.info(f"Watching for workflows: {type_prefixes}")
    logger.info("Handles: email, SMS, and push notifications")
    logger.info("Press Ctrl+C to stop")

    try:
        poller.start()
    except KeyboardInterrupt:
        logger.info("Shutting down worker...")
        poller.stop()
        logger.info("Worker stopped")


if __name__ == '__main__':
    main()
