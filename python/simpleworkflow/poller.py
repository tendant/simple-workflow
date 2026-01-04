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
from urllib.parse import urlparse, parse_qs, urlunparse
import psycopg2
from psycopg2.extras import RealDictCursor

from .metrics import MetricsCollector

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
        metrics: Optional[MetricsCollector] = None,
    ):
        """
        Initialize the intent poller.

        Args:
            db_url: PostgreSQL connection string (may include search_path= query parameter)
            supported_workflows: List of workflow names this poller handles
            worker_id: Unique identifier for this worker (default: "python-worker")
            poll_interval: Polling interval in seconds (default: 2)
            metrics: Optional metrics collector for observability
        """
        # Parse URL to extract and remove search_path (psycopg2 doesn't support it as query param)
        parsed = urlparse(db_url)
        if parsed.query:
            query_params = parse_qs(parsed.query)
            self.search_path = query_params.get('search_path', ['public'])[0]
            # Remove search_path from query params
            filtered_params = {k: v for k, v in query_params.items() if k != 'search_path'}
            # Rebuild query string
            new_query = '&'.join(f"{k}={v[0]}" for k, v in filtered_params.items())
            # Rebuild URL
            self.db_url = urlunparse((
                parsed.scheme, parsed.netloc, parsed.path,
                parsed.params, new_query, parsed.fragment
            ))
        else:
            self.db_url = db_url
            self.search_path = 'public'

        self.supported_workflows = supported_workflows
        self.worker_id = worker_id
        self.poll_interval = poll_interval
        self.running = False
        self.executors: Dict[str, WorkflowExecutor] = {}
        self.metrics = metrics
        self.start_time = time.time()

        # Debug: Log metrics initialization
        if self.metrics:
            logger.info(f"DEBUG: Metrics collector initialized: {type(self.metrics).__name__}")
        else:
            logger.warning("Metrics collector is None - metrics will not be recorded")

    def register_executor(self, workflow_name: str, executor: WorkflowExecutor):
        """
        Register a workflow executor for a specific workflow name.

        Args:
            workflow_name: Workflow name (e.g., "myapp.process.v1")
            executor: WorkflowExecutor instance that handles this workflow
        """
        self.executors[workflow_name] = executor
        logger.info(f"Registered executor for workflow: {workflow_name}")

    def _connect(self):
        """Create a database connection and set search_path"""
        conn = psycopg2.connect(self.db_url)
        if self.search_path:
            with conn.cursor() as cur:
                cur.execute(f"SET search_path TO {self.search_path}")
                conn.commit()
        return conn

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
        # Record poll cycle and update worker metrics
        if self.metrics:
            try:
                self.metrics.record_poll_cycle(self.worker_id)
                uptime = time.time() - self.start_time
                self.metrics.update_worker_uptime(self.worker_id, uptime)
                self.metrics.update_last_poll_timestamp(self.worker_id, time.time())
            except Exception as e:
                logger.error(f"Error recording poll metrics: {e}", exc_info=True)

        execution_start = time.time()

        try:
            intent = self.claim_intent()
        except Exception as e:
            if self.metrics:
                try:
                    error_type = type(e).__name__
                    self.metrics.record_poll_error(self.worker_id, error_type)
                except Exception as me:
                    logger.error(f"Error recording poll error metric: {me}", exc_info=True)
            raise

        if intent is None:
            # No work available - update queue depth metrics
            if self.metrics:
                try:
                    self._update_queue_depth()
                except Exception as e:
                    logger.error(f"Error updating queue depth: {e}", exc_info=True)
            return

        # Record intent claimed
        logger.info(f"Claimed intent: {intent['id']} (name: {intent['name']})")
        if self.metrics:
            try:
                self.metrics.record_intent_claimed(intent["name"], self.worker_id)
                logger.info(f"DEBUG: Recorded intent_claimed metric for {intent['name']}")
            except Exception as e:
                logger.error(f"Error recording intent_claimed metric: {e}", exc_info=True)

        try:
            result = self.execute_intent(intent)
            self.mark_succeeded(intent["id"], intent["name"], result, execution_start)
        except Exception as e:
            logger.error(f"Intent {intent['id']} failed: {e}", exc_info=True)
            self.mark_failed(
                intent["id"],
                intent["name"],
                intent["attempt_count"],
                intent["max_attempts"],
                str(e),
                execution_start,
            )

    def claim_intent(self) -> Optional[Dict[str, Any]]:
        """Claim a pending intent using SELECT FOR UPDATE SKIP LOCKED"""
        conn = None
        try:
            conn = self._connect()
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

    def mark_succeeded(
        self, intent_id: str, workflow_name: str, result: Any, execution_start: float
    ):
        """Mark intent as succeeded"""
        conn = None
        try:
            conn = self._connect()
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

                # Record metrics
                if self.metrics:
                    try:
                        duration = time.time() - execution_start
                        self.metrics.record_intent_completed(
                            workflow_name, self.worker_id, "succeeded", duration
                        )
                        logger.info(f"DEBUG: Recorded intent_completed metric (succeeded) for {workflow_name}, duration={duration:.3f}s")
                    except Exception as e:
                        logger.error(f"Error recording intent_completed metric: {e}", exc_info=True)
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
        self,
        intent_id: str,
        workflow_name: str,
        attempt_count: int,
        max_attempts: int,
        error: str,
        execution_start: float,
    ):
        """Mark intent as failed with retry logic"""
        new_attempt_count = attempt_count + 1
        status = "pending" if new_attempt_count < max_attempts else "deadletter"

        # Exponential backoff
        backoff_minutes = new_attempt_count**2
        run_after = datetime.now() + timedelta(minutes=backoff_minutes)

        conn = None
        try:
            conn = self._connect()
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

                # Record metrics
                if self.metrics:
                    try:
                        duration = time.time() - execution_start

                        # Record failed attempt
                        self.metrics.record_failed_attempt(
                            workflow_name, self.worker_id, new_attempt_count
                        )

                        # Record completion status
                        self.metrics.record_intent_completed(
                            workflow_name, self.worker_id, status, duration
                        )
                        logger.info(f"DEBUG: Recorded intent_completed metric ({status}) for {workflow_name}, duration={duration:.3f}s")

                        # Record deadletter metric if workflow permanently failed
                        if status == "deadletter":
                            self.metrics.record_intent_deadletter(
                                workflow_name, self.worker_id
                            )
                            logger.info(f"DEBUG: Recorded intent_deadletter metric for {workflow_name}")
                    except Exception as e:
                        logger.error(f"Error recording metrics in mark_failed: {e}", exc_info=True)
        except Exception as e:
            logger.error(
                f"Error marking intent {intent_id} as failed: {e}", exc_info=True
            )
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()

    def _update_queue_depth(self):
        """Update queue depth metrics for all supported workflows"""
        if not self.metrics:
            return

        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                for workflow_name in self.supported_workflows:
                    cur.execute(
                        """
                        SELECT COUNT(*)
                        FROM workflow_intent
                        WHERE name = %s AND status = 'pending' AND deleted_at IS NULL
                    """,
                        (workflow_name,),
                    )
                    depth = cur.fetchone()[0]
                    self.metrics.record_queue_depth(workflow_name, depth)
        except Exception as e:
            logger.error(f"Error updating queue depth: {e}", exc_info=True)
        finally:
            if conn:
                conn.close()
