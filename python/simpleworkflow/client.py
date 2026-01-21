"""
Workflow Client for Python Producers
Creates and manages workflow runs
"""

import json
import logging
from datetime import datetime, timedelta
from typing import Optional, Dict, Any
from urllib.parse import urlparse, parse_qs, urlunparse
import psycopg2
from psycopg2 import sql
import uuid

logger = logging.getLogger(__name__)


class SubmitBuilder:
    """Fluent API for submitting workflow runs"""

    def __init__(self, client: 'Client', workflow_type: str, payload: Any):
        self.client = client
        self.workflow_type = workflow_type
        self.payload = payload
        self.idempotency_key: Optional[str] = None
        self.priority: int = 100
        self.max_attempts: int = 3
        self.run_after: Optional[datetime] = None

    def with_idempotency(self, key: str) -> 'SubmitBuilder':
        """Set idempotency key for deduplication"""
        self.idempotency_key = key
        return self

    def with_priority(self, priority: int) -> 'SubmitBuilder':
        """Set priority (lower number = higher priority). Default: 100"""
        self.priority = priority
        return self

    def with_max_attempts(self, attempts: int) -> 'SubmitBuilder':
        """Set maximum retry attempts. Default: 3"""
        self.max_attempts = attempts
        return self

    def run_after(self, dt: datetime) -> 'SubmitBuilder':
        """Schedule workflow to run after a specific time"""
        self.run_after = dt
        return self

    def run_in(self, delta: timedelta) -> 'SubmitBuilder':
        """Schedule workflow to run after a duration from now"""
        self.run_after = datetime.now() + delta
        return self

    def execute(self) -> str:
        """Submit the workflow run and return its ID"""
        return self.client._create(
            workflow_type=self.workflow_type,
            payload=self.payload,
            idempotency_key=self.idempotency_key,
            priority=self.priority,
            max_attempts=self.max_attempts,
            run_after=self.run_after
        )


class Client:
    """Client for creating and managing workflow runs"""

    def __init__(self, conn_string: str):
        """
        Create a workflow client from a PostgreSQL connection string.

        Args:
            conn_string: PostgreSQL connection string
                         Can include ?schema=name or ?search_path=name parameter
                         Example: "postgres://user:pass@localhost/db?schema=workflow"
        """
        # Parse URL to extract and handle search_path/schema
        parsed = urlparse(conn_string)
        if parsed.query:
            query_params = parse_qs(parsed.query)

            # Check for schema parameter (our custom parameter)
            if 'schema' in query_params:
                self.search_path = query_params['schema'][0]
                # Remove schema and add search_path
                filtered_params = {k: v for k, v in query_params.items() if k != 'schema'}
            elif 'search_path' in query_params:
                self.search_path = query_params['search_path'][0]
                filtered_params = {k: v for k, v in query_params.items() if k != 'search_path'}
            else:
                self.search_path = 'workflow'
                filtered_params = query_params

            # Rebuild query string
            new_query = '&'.join(f"{k}={v[0]}" for k, v in filtered_params.items())
            # Rebuild URL without search_path/schema
            self.db_url = urlunparse((
                parsed.scheme, parsed.netloc, parsed.path,
                parsed.params, new_query, parsed.fragment
            ))
        else:
            self.db_url = conn_string
            self.search_path = 'workflow'

        # Test connection
        try:
            conn = self._connect()
            conn.close()
        except Exception as e:
            raise ConnectionError(f"Failed to connect to database: {e}")

    def _connect(self):
        """Create a database connection and set search_path"""
        conn = psycopg2.connect(self.db_url)
        if self.search_path:
            with conn.cursor() as cur:
                cur.execute(f"SET search_path TO {self.search_path}")
                conn.commit()
        return conn

    def submit(self, workflow_type: str, payload: Dict[str, Any]) -> SubmitBuilder:
        """
        Submit a workflow run with fluent configuration.

        Args:
            workflow_type: Workflow type (e.g., "billing.invoice.v1")
            payload: JSON-serializable payload dictionary

        Returns:
            SubmitBuilder for method chaining

        Example:
            run_id = client.submit("billing.invoice.v1", {...}).
                with_idempotency("invoice:123").
                with_priority(10).
                execute()
        """
        return SubmitBuilder(self, workflow_type, payload)

    def _create(
        self,
        workflow_type: str,
        payload: Any,
        idempotency_key: Optional[str] = None,
        priority: int = 100,
        max_attempts: int = 3,
        run_after: Optional[datetime] = None
    ) -> str:
        """Internal method to create workflow run"""
        conn = self._connect()
        try:
            with conn.cursor() as cur:
                run_id = str(uuid.uuid4())
                payload_json = json.dumps(payload)
                run_at = run_after if run_after else datetime.now()

                query = """
                    INSERT INTO workflow_run (
                        id, type, payload, priority, run_at,
                        idempotency_key, max_attempts
                    ) VALUES (%s, %s, %s, %s, %s, %s, %s)
                    ON CONFLICT (idempotency_key) DO NOTHING
                    RETURNING id
                """

                cur.execute(query, (
                    run_id,
                    workflow_type,
                    payload_json,
                    priority,
                    run_at,
                    idempotency_key,
                    max_attempts
                ))

                result = cur.fetchone()
                conn.commit()

                if result:
                    returned_id = result[0]
                    # Log creation event (best-effort)
                    self._log_event(conn, returned_id, 'created', None)
                    return returned_id
                else:
                    # Idempotency key conflict
                    logger.info(f"Workflow run with idempotency key '{idempotency_key}' already exists")
                    return ""
        except Exception as e:
            conn.rollback()
            raise
        finally:
            conn.close()

    def cancel(self, run_id: str) -> None:
        """
        Cancel a workflow run by ID (cooperative cancellation).

        Args:
            run_id: Workflow run ID to cancel
        """
        conn = self._connect()
        try:
            with conn.cursor() as cur:
                query = """
                    UPDATE workflow_run
                    SET status = 'cancelled', updated_at = NOW()
                    WHERE id = %s AND status IN ('pending', 'leased')
                """
                cur.execute(query, (run_id,))
                rows_affected = cur.rowcount
                conn.commit()

                if rows_affected == 0:
                    raise ValueError(f"Workflow run {run_id} not found or already completed")

                # Log cancellation event (best-effort)
                self._log_event(conn, run_id, 'cancelled', None)
        except Exception as e:
            conn.rollback()
            raise
        finally:
            conn.close()

    def _log_event(self, conn, workflow_id: str, event_type: str, data: Optional[Dict[str, Any]]):
        """Log an audit event (best-effort, errors are ignored)"""
        try:
            with conn.cursor() as cur:
                data_json = json.dumps(data) if data else None
                query = """
                    INSERT INTO workflow_event (workflow_id, event_type, data)
                    VALUES (%s, %s, %s)
                """
                cur.execute(query, (workflow_id, event_type, data_json))
                conn.commit()
        except Exception as e:
            logger.debug(f"Failed to log event: {e}")
            # Ignore errors - event logging is best-effort
            pass
