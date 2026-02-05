"""
Workflow Run Poller for Python Workers
Polls workflow_run table and executes Python workflows
"""

import json
import logging
import os
import random
import socket
import time
from abc import ABC, abstractmethod
from datetime import datetime, timedelta
from typing import Optional, Dict, Any, Callable, List
from urllib.parse import urlparse, parse_qs, urlunparse
import psycopg2
from psycopg2.extras import RealDictCursor

from .metrics import MetricsCollector

logger = logging.getLogger(__name__)


class WorkflowRun:
    """Represents a claimed workflow run with heartbeat and cancellation support"""

    def __init__(
        self,
        run_id: str,
        workflow_type: str,
        payload: bytes,
        attempt: int,
        max_attempts: int,
        db_url: str,
        search_path: str,
        heartbeat_fn: Callable[[int], None],
        is_cancelled_fn: Callable[[], bool],
    ):
        self.id = run_id
        self.type = workflow_type
        self.payload = payload
        self.attempt = attempt
        self.max_attempts = max_attempts
        self._db_url = db_url
        self._search_path = search_path
        self._heartbeat_fn = heartbeat_fn
        self._is_cancelled_fn = is_cancelled_fn

    def heartbeat(self, duration_seconds: int = 30):
        """Extend the lease on this workflow run"""
        self._heartbeat_fn(duration_seconds)

    def is_cancelled(self) -> bool:
        """Check if this workflow run has been cancelled"""
        return self._is_cancelled_fn()


class WorkflowExecutor(ABC):
    """
    Base class for workflow executors.
    Users implement this interface to execute specific workflows.
    """

    @abstractmethod
    def execute(self, run: WorkflowRun) -> Any:
        """
        Execute a workflow run and return the result.

        Args:
            run: WorkflowRun object containing id, type, payload, attempt, max_attempts,
                 plus heartbeat() and is_cancelled() methods

        Returns:
            Any result that can be JSON-serialized

        Raises:
            Exception: If the workflow execution fails
        """
        pass


class FunctionExecutorAdapter(WorkflowExecutor):
    """Adapter that wraps a function as a WorkflowExecutor"""

    def __init__(self, func: Callable[[WorkflowRun], Any]):
        self.func = func

    def execute(self, run: WorkflowRun) -> Any:
        return self.func(run)


class IntentPoller:
    """Polls workflow_run table and executes Python workflows"""

    def __init__(
        self,
        db_url: str,
        type_prefixes: Optional[List[str]] = None,
        worker_id: Optional[str] = None,
        poll_interval: int = 2,
        lease_duration: int = 30,
        metrics: Optional[MetricsCollector] = None,
    ):
        """
        Initialize the workflow run poller with simplified API.

        Args:
            db_url: PostgreSQL connection string (may include ?schema= or ?search_path= parameter)
                    Example: "postgres://user:pass@localhost/db?schema=workflow"
            type_prefixes: List of type prefixes this poller handles (e.g., ["billing.%"])
                          If None, auto-detects from registered handlers (recommended)
            worker_id: Unique identifier for this worker
                      If None, defaults to "hostname-pid"
            poll_interval: Polling interval in seconds (default: 2)
            lease_duration: Lease duration in seconds (default: 30)
            metrics: Optional metrics collector for observability
        """
        # Parse URL to extract and handle search_path/schema
        parsed = urlparse(db_url)
        if parsed.query:
            query_params = parse_qs(parsed.query)

            # Check for schema parameter (our custom parameter)
            if 'schema' in query_params:
                self.search_path = query_params['schema'][0]
                filtered_params = {k: v for k, v in query_params.items() if k != 'schema'}
            elif 'search_path' in query_params:
                self.search_path = query_params['search_path'][0]
                filtered_params = {k: v for k, v in query_params.items() if k != 'search_path'}
            else:
                self.search_path = 'workflow'
                filtered_params = query_params

            # Rebuild query string
            new_query = '&'.join(f"{k}={v[0]}" for k, v in filtered_params.items())
            # Rebuild URL
            self.db_url = urlunparse((
                parsed.scheme, parsed.netloc, parsed.path,
                parsed.params, new_query, parsed.fragment
            ))
        else:
            self.db_url = db_url
            self.search_path = 'workflow'

        self.type_prefixes = type_prefixes
        self.auto_detect_prefix = type_prefixes is None

        # Generate default worker ID: hostname-pid
        if worker_id is None:
            hostname = socket.gethostname() or 'unknown'
            pid = os.getpid()
            self.worker_id = f"{hostname}-{pid}"
        else:
            self.worker_id = worker_id

        self.poll_interval = poll_interval
        self.lease_duration = lease_duration
        self.running = False
        self.executors: Dict[str, WorkflowExecutor] = {}
        self.function_handlers: Dict[str, Callable] = {}  # For function-based handlers
        self.metrics = metrics
        self.start_time = time.time()

        # Debug: Log metrics initialization
        if self.metrics:
            logger.info(f"DEBUG: Metrics collector initialized: {type(self.metrics).__name__}")
        else:
            logger.warning("Metrics collector is None - metrics will not be recorded")

    def register_executor(self, workflow_type: str, executor: WorkflowExecutor):
        """
        Register a workflow executor for a specific workflow type.

        Args:
            workflow_type: Workflow type (e.g., "myapp.process.v1")
            executor: WorkflowExecutor instance that handles this workflow
        """
        self.executors[workflow_type] = executor
        logger.info(f"Registered executor for workflow type: {workflow_type}")

    def handle_func(self, workflow_type: str, func: Callable[[WorkflowRun], Any]) -> 'IntentPoller':
        """
        Register a function handler for a workflow type (simplified API).
        No need to create an executor class.

        Args:
            workflow_type: Workflow type (e.g., "billing.invoice.v1")
            func: Function that takes a WorkflowRun and returns a result

        Returns:
            self for method chaining

        Example:
            poller.handle_func("billing.invoice.v1", lambda run: {...})

            # Or as decorator:
            @poller.handler("billing.invoice.v1")
            def process_invoice(run):
                return result
        """
        self.function_handlers[workflow_type] = func
        # Wrap function as executor
        self.executors[workflow_type] = FunctionExecutorAdapter(func)
        logger.info(f"Registered function handler for workflow type: {workflow_type}")
        return self

    def handler(self, workflow_type: str):
        """
        Decorator for registering workflow handlers.

        Example:
            @poller.handler("billing.invoice.v1")
            def process_invoice(run: WorkflowRun):
                payload = run.payload
                # Process invoice
                return {"status": "completed"}
        """
        def decorator(func: Callable[[WorkflowRun], Any]):
            self.handle_func(workflow_type, func)
            return func
        return decorator

    def _detect_type_prefixes(self) -> List[str]:
        """Auto-detect type prefixes from registered handlers"""
        prefix_set = set()

        for workflow_type in self.executors.keys():
            # Skip if already a prefix pattern
            if workflow_type.endswith('%'):
                prefix_set.add(workflow_type)
                continue

            # Extract prefix (everything up to first dot + %)
            parts = workflow_type.split('.')
            if len(parts) > 1:
                # Use domain prefix (e.g., "billing.invoice.v1" -> "billing.%")
                prefix = parts[0] + '.%'
                prefix_set.add(prefix)
            else:
                # No dots, use exact match
                prefix_set.add(workflow_type)

        return list(prefix_set)

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
        # Auto-detect type prefixes if needed
        if self.auto_detect_prefix and not self.type_prefixes:
            self.type_prefixes = self._detect_type_prefixes()

        # Validate that at least one handler is registered
        if not self.executors:
            raise ValueError("No workflow handlers registered. Use handle_func() or register_executor() to register handlers.")

        self.running = True
        logger.info(
            f"Workflow poller started, watching type prefixes: {self.type_prefixes}"
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
        logger.info("Workflow poller stopped")

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
            run = self.claim_run()
        except Exception as e:
            if self.metrics:
                try:
                    error_type = type(e).__name__
                    self.metrics.record_poll_error(self.worker_id, error_type)
                except Exception as me:
                    logger.error(f"Error recording poll error metric: {me}", exc_info=True)
            raise

        if run is None:
            # No work available - update queue depth metrics
            if self.metrics:
                try:
                    self._update_queue_depth()
                except Exception as e:
                    logger.error(f"Error updating queue depth: {e}", exc_info=True)
            return

        # Record run claimed
        logger.info(f"Claimed workflow run: {run.id} (type: {run.type})")
        if self.metrics:
            try:
                self.metrics.record_intent_claimed(run.type, self.worker_id)
                logger.info(f"DEBUG: Recorded intent_claimed metric for {run.type}")
            except Exception as e:
                logger.error(f"Error recording intent_claimed metric: {e}", exc_info=True)

        # Log started event
        self._log_event(run.id, "started", {"worker_id": self.worker_id})

        try:
            result = self.execute_run(run)
            self.mark_succeeded(run.id, run.type, result, execution_start)
        except Exception as e:
            logger.error(f"Workflow run {run.id} failed: {e}", exc_info=True)
            self.mark_failed(
                run.id,
                run.type,
                run.attempt,
                run.max_attempts,
                str(e),
                execution_start,
            )

    def claim_run(self) -> Optional[WorkflowRun]:
        """Claim a pending workflow run using atomic UPDATE...RETURNING with SKIP LOCKED"""
        conn = None
        try:
            conn = self._connect()
            with conn.cursor(cursor_factory=RealDictCursor) as cur:
                # Build parameterized type-prefix LIKE conditions
                params = [
                    self.worker_id,
                    str(self.lease_duration) + " seconds",
                ]

                type_condition = ""
                if self.type_prefixes:
                    likes = []
                    for prefix in self.type_prefixes:
                        likes.append("type LIKE %s")
                        params.append(prefix)
                    type_condition = " AND (" + " OR ".join(likes) + ")"

                # Atomic claim: UPDATE...RETURNING with SKIP LOCKED subquery
                query = f"""
                    UPDATE workflow_run
                    SET status = 'leased',
                        leased_by = %s,
                        lease_until = NOW() + (%s)::interval,
                        updated_at = NOW()
                    WHERE id = (
                        SELECT id FROM workflow_run
                        WHERE status = 'pending'
                          AND run_at <= NOW()
                          AND deleted_at IS NULL
                          {type_condition}
                        ORDER BY priority ASC, created_at ASC
                        FOR UPDATE SKIP LOCKED
                        LIMIT 1
                    )
                    RETURNING id, type, payload, attempt, max_attempts
                """
                cur.execute(query, tuple(params))

                row = cur.fetchone()
                if row is None:
                    return None

                conn.commit()

                # Create WorkflowRun object with heartbeat and cancellation functions
                run = WorkflowRun(
                    run_id=row["id"],
                    workflow_type=row["type"],
                    payload=row["payload"],
                    attempt=row["attempt"],
                    max_attempts=row["max_attempts"],
                    db_url=self.db_url,
                    search_path=self.search_path,
                    heartbeat_fn=lambda duration: self._heartbeat(row["id"], duration),
                    is_cancelled_fn=lambda: self._is_cancelled(row["id"]),
                )

                # Log leased event
                self._log_event(row["id"], "leased", {
                    "worker_id": self.worker_id,
                    "attempt": row["attempt"],
                })

                return run
        except Exception as e:
            logger.error(f"Error claiming workflow run: {e}", exc_info=True)
            if conn:
                conn.rollback()
            return None
        finally:
            if conn:
                conn.close()

    def execute_run(self, run: WorkflowRun) -> Any:
        """Execute workflow based on registered executor"""
        workflow_type = run.type

        # Find registered executor
        executor = self.executors.get(workflow_type)
        if executor is None:
            raise ValueError(
                f"No executor registered for workflow type: {workflow_type}. "
                f"Available executors: {list(self.executors.keys())}"
            )

        # Execute workflow
        logger.info(f"Executing workflow type: {workflow_type}")
        result = executor.execute(run)
        return result

    def mark_succeeded(
        self, run_id: str, workflow_type: str, result: Any, execution_start: float
    ):
        """Mark workflow run as succeeded"""
        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                cur.execute(
                    """
                    UPDATE workflow_run
                    SET status = 'succeeded',
                        result = %s,
                        updated_at = NOW()
                    WHERE id = %s
                """,
                    (json.dumps(result), run_id),
                )
                conn.commit()
                logger.info(f"Workflow run {run_id} succeeded")

                # Log succeeded event
                self._log_event(run_id, "succeeded", None)

                # Record metrics
                if self.metrics:
                    try:
                        duration = time.time() - execution_start
                        self.metrics.record_intent_completed(
                            workflow_type, self.worker_id, "succeeded", duration
                        )
                        logger.info(f"DEBUG: Recorded intent_completed metric (succeeded) for {workflow_type}, duration={duration:.3f}s")
                    except Exception as e:
                        logger.error(f"Error recording intent_completed metric: {e}", exc_info=True)
        except Exception as e:
            logger.error(
                f"Error marking workflow run {run_id} as succeeded: {e}", exc_info=True
            )
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()

    def mark_failed(
        self,
        run_id: str,
        workflow_type: str,
        attempt: int,
        max_attempts: int,
        error: str,
        execution_start: float,
    ):
        """Mark workflow run as failed with retry logic"""
        new_attempt = attempt + 1
        status = "pending" if new_attempt < max_attempts else "failed"

        # Exponential backoff with jitter (prevents thundering herd)
        base_delay_minutes = new_attempt ** 2
        jitter_minutes = base_delay_minutes * 0.1 * random.random()
        run_at = datetime.now() + timedelta(minutes=base_delay_minutes + jitter_minutes)

        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                cur.execute(
                    """
                    UPDATE workflow_run
                    SET status = %s,
                        attempt = %s,
                        run_at = %s,
                        last_error = %s,
                        updated_at = NOW()
                    WHERE id = %s
                """,
                    (status, new_attempt, run_at, error, run_id),
                )
                conn.commit()
                logger.info(
                    f"Workflow run {run_id} failed (attempt {new_attempt}/{max_attempts})"
                )

                # Log event
                event_type = "retried" if status == "pending" else "failed"
                self._log_event(run_id, event_type, {
                    "attempt": new_attempt,
                    "error": error,
                })

                # Record metrics
                if self.metrics:
                    try:
                        duration = time.time() - execution_start

                        # Record failed attempt
                        self.metrics.record_failed_attempt(
                            workflow_type, self.worker_id, new_attempt
                        )

                        # Record completion status
                        self.metrics.record_intent_completed(
                            workflow_type, self.worker_id, status, duration
                        )
                        logger.info(f"DEBUG: Recorded intent_completed metric ({status}) for {workflow_type}, duration={duration:.3f}s")

                        # Record failed metric if workflow permanently failed
                        if status == "failed":
                            self.metrics.record_intent_deadletter(
                                workflow_type, self.worker_id
                            )
                            logger.info(f"DEBUG: Recorded intent_deadletter metric for {workflow_type}")
                    except Exception as e:
                        logger.error(f"Error recording metrics in mark_failed: {e}", exc_info=True)
        except Exception as e:
            logger.error(
                f"Error marking workflow run {run_id} as failed: {e}", exc_info=True
            )
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()

    def _update_queue_depth(self):
        """Update queue depth metrics for all type prefixes"""
        if not self.metrics:
            return

        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                for prefix in self.type_prefixes:
                    cur.execute(
                        """
                        SELECT COUNT(*)
                        FROM workflow_run
                        WHERE type LIKE %s AND status = 'pending' AND deleted_at IS NULL
                    """,
                        (prefix,),
                    )
                    depth = cur.fetchone()[0]
                    self.metrics.record_queue_depth(prefix, depth)
        except Exception as e:
            logger.error(f"Error updating queue depth: {e}", exc_info=True)
        finally:
            if conn:
                conn.close()

    def _heartbeat(self, run_id: str, duration_seconds: int):
        """Extend the lease on a workflow run"""
        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                cur.execute(
                    """
                    UPDATE workflow_run
                    SET lease_until = NOW() + (%s || ' seconds')::interval,
                        updated_at = NOW()
                    WHERE id = %s AND status = 'leased'
                """,
                    (duration_seconds, run_id),
                )
                conn.commit()

                # Log heartbeat event
                self._log_event(run_id, "heartbeat", {
                    "extended_by_seconds": duration_seconds,
                })
        except Exception as e:
            logger.error(f"Error extending lease for {run_id}: {e}", exc_info=True)
            if conn:
                conn.rollback()
        finally:
            if conn:
                conn.close()

    def _is_cancelled(self, run_id: str) -> bool:
        """Check if a workflow run has been cancelled"""
        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                cur.execute(
                    "SELECT status FROM workflow_run WHERE id = %s",
                    (run_id,),
                )
                row = cur.fetchone()
                if row:
                    return row[0] == "cancelled"
                return False
        except Exception as e:
            logger.error(f"Error checking cancellation for {run_id}: {e}", exc_info=True)
            return False
        finally:
            if conn:
                conn.close()

    def _log_event(self, workflow_id: str, event_type: str, data: Optional[Dict[str, Any]]):
        """Log an audit event (best-effort, errors are ignored)"""
        conn = None
        try:
            conn = self._connect()
            with conn.cursor() as cur:
                data_json = json.dumps(data) if data else None
                cur.execute(
                    """
                    INSERT INTO workflow_event (workflow_id, event_type, data)
                    VALUES (%s, %s, %s)
                """,
                    (workflow_id, event_type, data_json),
                )
                conn.commit()
        except Exception:
            # Silently ignore event logging errors
            pass
        finally:
            if conn:
                conn.close()
