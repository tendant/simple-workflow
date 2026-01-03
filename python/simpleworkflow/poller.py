"""
Intent Poller for Python Workers
Polls workflow_intent table and executes Python workflows
"""

import json
import logging
import time
from abc import ABC, abstractmethod
from datetime import datetime, timedelta
from typing import Optional, Dict, Any, Callable
import psycopg2
from psycopg2.extras import RealDictCursor

logger = logging.getLogger(__name__)


class WorkflowExecutor(ABC):
    """
    Base class for workflow executors.
    Users implement this interface to execute specific workflows.
    """

    @abstractmethod
    def execute(self, intent: Dict[str, Any]) -> Any:
        """
        Execute a workflow intent and return the result.

        Args:
            intent: Dict containing 'id', 'name', 'payload', 'attempt_count', 'max_attempts'

        Returns:
            Any result that can be JSON-serialized

        Raises:
            Exception: If the workflow execution fails
        """
        pass


class IntentPoller:
    """Polls workflow_intent table and executes Python workflows"""

    def __init__(
        self,
        db_url: str,
        supported_workflows: list[str],
        worker_id: str = "python-worker",
        poll_interval: int = 2,
    ):
        """
        Initialize the intent poller.

        Args:
            db_url: PostgreSQL connection string (must include search_path=workflow)
            supported_workflows: List of workflow names this poller handles
            worker_id: Unique identifier for this worker (default: "python-worker")
            poll_interval: Polling interval in seconds (default: 2)
        """
        self.db_url = db_url
        self.supported_workflows = supported_workflows
        self.worker_id = worker_id
        self.poll_interval = poll_interval
        self.running = False
        self.executors: Dict[str, WorkflowExecutor] = {}

    def register_executor(self, workflow_name: str, executor: WorkflowExecutor):
        """
        Register a workflow executor for a specific workflow name.

        Args:
            workflow_name: Workflow name (e.g., "myapp.process.v1")
            executor: WorkflowExecutor instance that handles this workflow
        """
        self.executors[workflow_name] = executor
        logger.info(f"Registered executor for workflow: {workflow_name}")

    def start(self):
        """Start polling loop (blocking)"""
        self.running = True
        logger.info(
            f"Intent poller started, watching workflows: {self.supported_workflows}"
        )
        logger.info(f"Worker ID: {self.worker_id}")

        while self.running:
            try:
                self.poll_and_execute()
            except Exception as e:
                logger.error(f"Error in poll loop: {e}", exc_info=True)

            time.sleep(self.poll_interval)

    def stop(self):
        """Stop polling"""
        self.running = False
        logger.info("Intent poller stopped")

    def poll_and_execute(self):
        """Poll for work and execute if found"""
        intent = self.claim_intent()
        if intent is None:
            return

        logger.info(f"Claimed intent: {intent['id']} (name: {intent['name']})")

        try:
            result = self.execute_intent(intent)
            self.mark_succeeded(intent["id"], result)
        except Exception as e:
            logger.error(f"Intent {intent['id']} failed: {e}", exc_info=True)
            self.mark_failed(
                intent["id"], intent["attempt_count"], intent["max_attempts"], str(e)
            )

    def claim_intent(self) -> Optional[Dict[str, Any]]:
        """Claim a pending intent using SELECT FOR UPDATE SKIP LOCKED"""
        conn = None
        try:
            conn = psycopg2.connect(self.db_url)
            with conn.cursor(cursor_factory=RealDictCursor) as cur:
                # Claim intent
                cur.execute(
                    """
                    SELECT id, name, payload, attempt_count, max_attempts
                    FROM workflow_intent
                    WHERE status = 'pending'
                      AND name = ANY(%s)
                      AND run_after <= NOW()
                      AND deleted_at IS NULL
                    ORDER BY priority ASC, created_at ASC
                    FOR UPDATE SKIP LOCKED
                    LIMIT 1
                """,
                    (self.supported_workflows,),
                )

                intent = cur.fetchone()
                if intent is None:
                    return None

                # Mark as running
                cur.execute(
                    """
                    UPDATE workflow_intent
                    SET status = 'running',
                        claimed_by = %s,
                        lease_expires_at = NOW() + INTERVAL '5 minutes',
                        updated_at = NOW()
                    WHERE id = %s
                """,
                    (self.worker_id, intent["id"]),
                )

                conn.commit()
                return dict(intent)
        except Exception as e:
            logger.error(f"Error claiming intent: {e}", exc_info=True)
            if conn:
                conn.rollback()
            return None
        finally:
            if conn:
                conn.close()

    def execute_intent(self, intent: Dict[str, Any]) -> Any:
        """Execute workflow based on registered executor"""
        workflow_name = intent["name"]

        # Find registered executor
        executor = self.executors.get(workflow_name)
        if executor is None:
            raise ValueError(
                f"No executor registered for workflow: {workflow_name}. "
                f"Available executors: {list(self.executors.keys())}"
            )

        # Execute workflow
        logger.info(f"Executing workflow: {workflow_name}")
        result = executor.execute(intent)
        return result

    def mark_succeeded(self, intent_id: str, result: Any):
        """Mark intent as succeeded"""
        conn = None
        try:
            conn = psycopg2.connect(self.db_url)
            with conn.cursor() as cur:
                cur.execute(
                    """
                    UPDATE workflow_intent
                    SET status = 'succeeded',
                        result = %s,
                        updated_at = NOW()
                    WHERE id = %s
                """,
                    (json.dumps(result), intent_id),
                )
                conn.commit()
                logger.info(f"Intent {intent_id} succeeded")
        except Exception as e:
            logger.error(
                f"Error marking intent {intent_id} as succeeded: {e}", exc_info=True
            )
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()

    def mark_failed(
        self, intent_id: str, attempt_count: int, max_attempts: int, error: str
    ):
        """Mark intent as failed with retry logic"""
        new_attempt_count = attempt_count + 1
        status = "pending" if new_attempt_count < max_attempts else "deadletter"

        # Exponential backoff
        backoff_minutes = new_attempt_count**2
        run_after = datetime.now() + timedelta(minutes=backoff_minutes)

        conn = None
        try:
            conn = psycopg2.connect(self.db_url)
            with conn.cursor() as cur:
                cur.execute(
                    """
                    UPDATE workflow_intent
                    SET status = %s,
                        attempt_count = %s,
                        run_after = %s,
                        last_error = %s,
                        updated_at = NOW()
                    WHERE id = %s
                """,
                    (status, new_attempt_count, run_after, error, intent_id),
                )
                conn.commit()
                logger.info(
                    f"Intent {intent_id} failed (attempt {new_attempt_count}/{max_attempts})"
                )
        except Exception as e:
            logger.error(
                f"Error marking intent {intent_id} as failed: {e}", exc_info=True
            )
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()
