#!/usr/bin/env python3
"""
Example worker demonstrating simple-workflow Python usage
"""

import os
import logging
from simpleworkflow import IntentPoller, WorkflowExecutor

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


class ExampleExecutor(WorkflowExecutor):
    """Example workflow executor"""

    def execute(self, intent):
        """Execute the workflow based on intent name"""
        workflow_name = intent['name']
        payload = intent['payload']

        logger.info(f"Executing workflow: {workflow_name}")
        logger.info(f"Payload: {payload}")

        if workflow_name == 'example.hello.v1':
            return self.say_hello(payload)
        elif workflow_name == 'example.process.v1':
            return self.process_data(payload)
        else:
            raise ValueError(f"Unknown workflow: {workflow_name}")

    def say_hello(self, payload):
        """Simple hello workflow"""
        name = payload.get('name', 'World')
        message = f"Hello, {name}!"
        logger.info(message)

        return {
            "message": message,
            "processed_by": "ExampleExecutor"
        }

    def process_data(self, payload):
        """Data processing workflow"""
        data = payload.get('data', [])
        operation = payload.get('operation', 'count')

        if operation == 'count':
            result = len(data)
        elif operation == 'sum':
            result = sum(data)
        elif operation == 'average':
            result = sum(data) / len(data) if data else 0
        else:
            raise ValueError(f"Unknown operation: {operation}")

        logger.info(f"Operation: {operation}, Result: {result}")

        return {
            "operation": operation,
            "result": result,
            "count": len(data)
        }


def main():
    """Main entry point"""
    # Load configuration from environment
    db_url = os.getenv(
        'DATABASE_URL',
        'postgres://postgres:postgres@localhost/workflow'
    )

    # Ensure search_path=workflow is in URL
    if '?' in db_url:
        db_url += '&search_path=workflow'
    else:
        db_url += '?search_path=workflow'

    worker_id = os.getenv('WORKER_ID', 'example-worker-1')
    poll_interval = int(os.getenv('POLL_INTERVAL', '2'))

    # Create poller
    supported_workflows = [
        'example.hello.v1',
        'example.process.v1'
    ]

    poller = IntentPoller(
        db_url=db_url,
        supported_workflows=supported_workflows,
        worker_id=worker_id,
        poll_interval=poll_interval
    )

    # Register executor
    executor = ExampleExecutor()
    poller.register_executor('example.hello.v1', executor)
    poller.register_executor('example.process.v1', executor)

    # Start polling
    logger.info("="*50)
    logger.info("Example Worker Started")
    logger.info(f"Worker ID: {worker_id}")
    logger.info(f"Supported Workflows: {supported_workflows}")
    logger.info(f"Poll Interval: {poll_interval}s")
    logger.info("="*50)

    try:
        poller.start()
    except KeyboardInterrupt:
        logger.info("Stopping worker...")
        poller.stop()
        logger.info("Worker stopped")


if __name__ == '__main__':
    main()
