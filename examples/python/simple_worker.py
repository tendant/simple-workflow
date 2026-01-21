#!/usr/bin/env python3
"""
Simple Worker Example - Simplified API

This example demonstrates the simplified Python API:
- No executor class needed (use functions directly)
- Auto-detected type prefixes
- Smart defaults for worker_id
- Decorator-style handler registration
"""

import os
import sys
import logging
from pathlib import Path

# Add parent directory to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent.parent / 'python'))

from simpleworkflow import IntentPoller, WorkflowRun

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


def main():
    # Database configuration
    db_url = os.getenv('DATABASE_URL', 'postgres://pas:pwd@localhost/pas?schema=workflow')

    # Create poller with simplified API (auto-detects type prefix!)
    poller = IntentPoller(db_url)

    # Register handler using decorator
    @poller.handler("content.thumbnail.v1")
    def generate_thumbnail(run: WorkflowRun):
        payload = run.payload
        content_id = payload['content_id']
        width = payload['width']
        height = payload['height']

        logger.info(f"Generating thumbnail for {content_id} ({width}x{height})")

        # Simulate work
        import time
        time.sleep(2)

        logger.info(f"Thumbnail completed for {content_id}")

        return {
            "status": "completed",
            "content_id": content_id,
            "url": f"https://cdn.example.com/{content_id}_thumb_{width}x{height}.jpg"
        }

    # Register another handler using handle_func
    poller.handle_func("content.resize.v1", lambda run: {
        "status": "completed",
        "content_id": run.payload['content_id']
    })

    # Start polling (type prefix "content.%" is auto-detected!)
    logger.info("Starting simple worker...")
    logger.info("Handlers registered for content.thumbnail.v1 and content.resize.v1")
    logger.info("Type prefix auto-detected: content.%")
    logger.info("Press Ctrl+C to stop")

    try:
        poller.start()
    except KeyboardInterrupt:
        logger.info("Shutting down worker...")
        poller.stop()
        logger.info("Worker stopped")


if __name__ == '__main__':
    main()
