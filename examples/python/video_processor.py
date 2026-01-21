#!/usr/bin/env python3
"""
Video Processing Worker with Heartbeat Support

This example demonstrates:
- Long-running workflow execution
- Heartbeat to extend lease during processing
- Cancellation checking
- Type-prefix routing
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


class VideoProcessingExecutor(WorkflowExecutor):
    """
    Processes video files with heartbeat support for long operations.
    """

    def execute(self, run: WorkflowRun):
        payload = run.payload
        video_id = payload['video_id']
        operations = payload.get('operations', ['transcode', 'thumbnail', 'upload'])

        logger.info(f"Starting video processing for {video_id}")
        logger.info(f"Operations: {operations}")
        logger.info(f"Attempt: {run.attempt + 1}/{run.max_attempts}")

        results = {}

        for i, operation in enumerate(operations):
            logger.info(f"Executing operation {i+1}/{len(operations)}: {operation}")

            # Check for cancellation before each operation
            if run.is_cancelled():
                logger.warning(f"Video processing cancelled for {video_id}")
                raise Exception("Workflow cancelled by user")

            # Simulate long-running operation
            self._process_operation(video_id, operation, results)

            # Extend lease after each operation (prevents lease expiration)
            if i < len(operations) - 1:  # Don't extend on last operation
                logger.info("Extending lease by 30 seconds...")
                run.heartbeat(30)

        logger.info(f"Video processing completed for {video_id}")

        return {
            "status": "completed",
            "video_id": video_id,
            "results": results,
            "processing_time_seconds": sum(results.values())
        }

    def _process_operation(self, video_id: str, operation: str, results: dict):
        """Simulate a time-consuming operation"""
        start_time = time.time()

        if operation == 'transcode':
            logger.info(f"Transcoding video {video_id}...")
            time.sleep(8)  # Simulate 8 seconds of work
        elif operation == 'thumbnail':
            logger.info(f"Generating thumbnails for {video_id}...")
            time.sleep(5)  # Simulate 5 seconds of work
        elif operation == 'upload':
            logger.info(f"Uploading video {video_id} to CDN...")
            time.sleep(6)  # Simulate 6 seconds of work
        else:
            logger.warning(f"Unknown operation: {operation}")
            time.sleep(2)

        duration = time.time() - start_time
        results[operation] = duration
        logger.info(f"Operation '{operation}' completed in {duration:.1f}s")


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

    # Create poller for media processing workflows
    type_prefixes = ['media.%']  # Handles all media.* workflows
    poller = IntentPoller(
        db_url=db_url,
        type_prefixes=type_prefixes,
        worker_id='video-processor-python-1',
        poll_interval=2,
        lease_duration=30  # 30 seconds, extended via heartbeat
    )

    # Register executor
    executor = VideoProcessingExecutor()
    poller.register_executor('media.video.v1', executor)

    # Start polling
    logger.info("Starting video processing worker...")
    logger.info(f"Watching for workflows: {type_prefixes}")
    logger.info("Press Ctrl+C to stop")

    try:
        poller.start()
    except KeyboardInterrupt:
        logger.info("Shutting down worker...")
        poller.stop()
        logger.info("Worker stopped")


if __name__ == '__main__':
    main()
