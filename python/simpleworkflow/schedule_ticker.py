"""
Schedule Ticker for Python Workers
Converts due schedules into workflow_run rows
"""

import json
import logging
import time
import threading
import uuid
from datetime import datetime
from typing import Optional
from urllib.parse import urlparse, parse_qs, urlunparse

import psycopg2
from psycopg2.extras import RealDictCursor
from croniter import croniter

logger = logging.getLogger(__name__)


class ScheduleTicker:
    """
    Converts due workflow_schedule rows into workflow_run rows.

    Can run standalone or be embedded in an IntentPoller via enable_schedule_ticker().
    Uses FOR UPDATE SKIP LOCKED so multiple tickers can run safely.
    """

    def __init__(self, conn_string: str, tick_interval: int = 15):
        """
        Create a ScheduleTicker.

        Args:
            conn_string: PostgreSQL connection string (may include ?schema= parameter)
            tick_interval: How often to check for due schedules, in seconds (default: 15)
        """
        parsed = urlparse(conn_string)
        if parsed.query:
            query_params = parse_qs(parsed.query)
            if 'schema' in query_params:
                self.search_path = query_params['schema'][0]
                filtered_params = {k: v for k, v in query_params.items() if k != 'schema'}
            elif 'search_path' in query_params:
                self.search_path = query_params['search_path'][0]
                filtered_params = {k: v for k, v in query_params.items() if k != 'search_path'}
            else:
                self.search_path = 'workflow'
                filtered_params = query_params
            new_query = '&'.join(f"{k}={v[0]}" for k, v in filtered_params.items())
            self.db_url = urlunparse((
                parsed.scheme, parsed.netloc, parsed.path,
                parsed.params, new_query, parsed.fragment
            ))
        else:
            self.db_url = conn_string
            self.search_path = 'workflow'

        self.tick_interval = tick_interval
        self.running = False

    def _connect(self):
        """Create a database connection and set search_path"""
        conn = psycopg2.connect(self.db_url)
        if self.search_path:
            with conn.cursor() as cur:
                cur.execute(f"SET search_path TO {self.search_path}")
                conn.commit()
        return conn

    def start(self):
        """Start the tick loop (blocking)."""
        self.running = True
        logger.info(f"Schedule ticker started (interval: {self.tick_interval}s)")

        while self.running:
            try:
                self.tick()
            except Exception as e:
                logger.error(f"Schedule tick error: {e}", exc_info=True)
            time.sleep(self.tick_interval)

    def stop(self):
        """Stop the tick loop."""
        self.running = False
        logger.info("Schedule ticker stopped")

    def tick(self):
        """
        Perform a single tick: find due schedules and create workflow_run rows.
        Exported for testing.
        """
        conn = self._connect()
        try:
            with conn.cursor(cursor_factory=RealDictCursor) as cur:
                # Claim due schedules with SKIP LOCKED
                cur.execute("""
                    SELECT id, type, payload, schedule, timezone, next_run_at, priority, max_attempts
                    FROM workflow_schedule
                    WHERE next_run_at <= NOW()
                      AND enabled = true
                      AND deleted_at IS NULL
                    FOR UPDATE SKIP LOCKED
                    LIMIT 10
                """)
                due_schedules = cur.fetchall()

                for sched in due_schedules:
                    fire_time_unix = int(sched['next_run_at'].timestamp())
                    idempotency_key = f"schedule:{sched['id']}:{fire_time_unix}"
                    run_id = str(uuid.uuid4())

                    # Insert workflow_run with idempotency key
                    cur.execute("""
                        INSERT INTO workflow_run (
                            id, type, payload, priority, run_at, idempotency_key, max_attempts
                        ) VALUES (%s, %s, %s, %s, NOW(), %s, %s)
                        ON CONFLICT (idempotency_key) DO NOTHING
                        RETURNING id
                    """, (
                        run_id,
                        sched['type'],
                        json.dumps(sched['payload']) if isinstance(sched['payload'], dict) else sched['payload'],
                        sched['priority'],
                        idempotency_key,
                        sched['max_attempts'],
                    ))

                    result = cur.fetchone()
                    if result:
                        logger.info(
                            f"Schedule {sched['id']} fired: created workflow_run {result['id']} "
                            f"(key: {idempotency_key})"
                        )

                    # Compute next fire time
                    try:
                        cron = croniter(sched['schedule'], datetime.now())
                        next_run = cron.get_next(datetime)
                    except Exception as e:
                        logger.error(f"Invalid cron expression {sched['schedule']!r} for schedule {sched['id']}: {e}")
                        continue

                    # Update schedule with next run time
                    cur.execute("""
                        UPDATE workflow_schedule
                        SET next_run_at = %s, last_run_at = NOW(), updated_at = NOW()
                        WHERE id = %s
                    """, (next_run, sched['id']))

                conn.commit()
        except Exception as e:
            conn.rollback()
            raise
        finally:
            conn.close()
