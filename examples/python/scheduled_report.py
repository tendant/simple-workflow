"""
Scheduled Report Example

Demonstrates creating a recurring schedule and running a worker
with the embedded schedule ticker.
"""

import json
import logging
import os
import signal
import sys

from simpleworkflow import Client, IntentPoller, WorkflowRun

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

DB_URL = os.getenv("DATABASE_URL", "postgres://pas:pwd@localhost/pas?sslmode=disable&schema=workflow")


def create_schedule():
    """Create a recurring weekly report schedule."""
    client = Client(DB_URL)

    schedule_id = (
        client.schedule("report.weekly.v1", {
            "report_type": "sales_summary",
            "recipients": ["team@example.com"],
        })
        .cron("0 9 * * 1")                  # Every Monday at 9:00 AM
        .in_timezone("America/New_York")     # Eastern time
        .with_priority(50)                   # Higher priority than default
        .create()
    )

    logger.info(f"Created schedule: {schedule_id}")

    # List all schedules
    schedules = client.list_schedules()
    for s in schedules:
        logger.info(f"  Schedule {s['id']}: type={s['type']} cron={s['schedule']} next={s['next_run_at']}")

    return schedule_id


def run_worker():
    """Run a worker with embedded schedule ticker."""
    poller = IntentPoller(DB_URL)

    # Enable the schedule ticker so the poller also fires due schedules
    poller.enable_schedule_ticker(tick_interval=15)

    @poller.handler("report.weekly.v1")
    def process_report(run: WorkflowRun):
        payload = json.loads(run.payload) if isinstance(run.payload, (str, bytes)) else run.payload
        logger.info(f"Generating {payload['report_type']} report for {payload['recipients']}")

        # Simulate report generation
        import time
        time.sleep(3)

        return {
            "status": "sent",
            "report_type": payload["report_type"],
            "recipients": payload["recipients"],
        }

    # Graceful shutdown
    def shutdown(sig, frame):
        logger.info("Shutting down...")
        poller.stop()
        sys.exit(0)

    signal.signal(signal.SIGINT, shutdown)
    signal.signal(signal.SIGTERM, shutdown)

    logger.info("Starting scheduled report worker...")
    logger.info("Press Ctrl+C to stop")
    poller.start()


if __name__ == "__main__":
    create_schedule()
    run_worker()
