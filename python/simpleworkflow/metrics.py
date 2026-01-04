"""Prometheus metrics for simple-workflow Python library"""

from abc import ABC, abstractmethod
from typing import Optional
import time
from prometheus_client import Counter, Histogram, Gauge


class MetricsCollector(ABC):
    """Base metrics collector interface"""

    @abstractmethod
    def record_intent_claimed(self, workflow_name: str, worker_id: str):
        """Record when a worker claims an intent"""
        pass

    @abstractmethod
    def record_intent_completed(
        self, workflow_name: str, worker_id: str, status: str, duration: float
    ):
        """Record when an intent execution completes (succeeded, failed, or deadletter)"""
        pass

    @abstractmethod
    def record_intent_deadletter(self, workflow_name: str, worker_id: str):
        """Record when an intent is moved to deadletter status"""
        pass

    @abstractmethod
    def record_failed_attempt(self, workflow_name: str, worker_id: str, attempt_number: int):
        """Record individual workflow execution failures before deadletter"""
        pass

    @abstractmethod
    def record_poll_cycle(self, worker_id: str):
        """Record a completed poll cycle"""
        pass

    @abstractmethod
    def record_poll_error(self, worker_id: str, error_type: str):
        """Record a polling error"""
        pass

    @abstractmethod
    def record_queue_depth(self, workflow_name: str, depth: int):
        """Record the current queue depth for a workflow"""
        pass

    @abstractmethod
    def update_worker_uptime(self, worker_id: str, uptime_seconds: float):
        """Update the worker uptime gauge"""
        pass

    @abstractmethod
    def update_last_poll_timestamp(self, worker_id: str, timestamp: float):
        """Update the timestamp of the last poll"""
        pass


class PrometheusMetrics(MetricsCollector):
    """Prometheus-based metrics collector"""

    def __init__(self):
        # Intent metrics
        self.intent_claimed_total = Counter(
            "workflow_intent_claimed_total",
            "Total workflow intents claimed",
            ["workflow_name", "worker_id"],
        )

        self.intent_completed_total = Counter(
            "workflow_intent_completed_total",
            "Total workflow intents completed",
            ["workflow_name", "worker_id", "status"],
        )

        self.intent_deadletter_total = Counter(
            "workflow_intent_deadletter_total",
            "Total workflow intents moved to deadletter",
            ["workflow_name", "worker_id"],
        )

        self.intent_failed_attempts = Counter(
            "workflow_intent_failed_attempts_total",
            "Total failed workflow attempts before deadletter",
            ["workflow_name", "worker_id", "attempt"],
        )

        self.intent_execution_duration = Histogram(
            "workflow_intent_execution_duration_seconds",
            "Workflow intent execution duration",
            ["workflow_name", "worker_id", "status"],
            buckets=[0.1, 0.5, 1, 5, 10, 30, 60, 300],
        )

        # Worker metrics
        self.poll_cycle_total = Counter(
            "workflow_worker_poll_cycle_total",
            "Total poll cycles executed",
            ["worker_id"],
        )

        self.poll_errors_total = Counter(
            "workflow_worker_poll_errors_total",
            "Total poll errors",
            ["worker_id", "error_type"],
        )

        self.queue_depth = Gauge(
            "workflow_intent_queue_depth",
            "Number of pending workflow intents",
            ["workflow_name", "status"],
        )

        self.worker_uptime = Gauge(
            "workflow_worker_uptime_seconds",
            "Worker uptime in seconds",
            ["worker_id"],
        )

        self.worker_last_poll = Gauge(
            "workflow_worker_last_poll_timestamp",
            "Unix timestamp of last poll",
            ["worker_id"],
        )

        self.start_time = time.time()

    def record_intent_claimed(self, workflow_name: str, worker_id: str):
        self.intent_claimed_total.labels(
            workflow_name=workflow_name, worker_id=worker_id
        ).inc()

    def record_intent_completed(
        self, workflow_name: str, worker_id: str, status: str, duration: float
    ):
        self.intent_completed_total.labels(
            workflow_name=workflow_name, worker_id=worker_id, status=status
        ).inc()

        self.intent_execution_duration.labels(
            workflow_name=workflow_name, worker_id=worker_id, status=status
        ).observe(duration)

    def record_intent_deadletter(self, workflow_name: str, worker_id: str):
        self.intent_deadletter_total.labels(
            workflow_name=workflow_name, worker_id=worker_id
        ).inc()

    def record_failed_attempt(self, workflow_name: str, worker_id: str, attempt_number: int):
        """Record individual failure attempts"""
        if attempt_number == 1:
            attempt_label = "1"
        elif attempt_number == 2:
            attempt_label = "2"
        else:
            attempt_label = "3+"

        self.intent_failed_attempts.labels(
            workflow_name=workflow_name,
            worker_id=worker_id,
            attempt=attempt_label
        ).inc()

    def record_poll_cycle(self, worker_id: str):
        """Record poll cycle and automatically update worker health gauges"""
        self.poll_cycle_total.labels(worker_id=worker_id).inc()
        self.worker_last_poll.labels(worker_id=worker_id).set(time.time())
        uptime = time.time() - self.start_time
        self.worker_uptime.labels(worker_id=worker_id).set(uptime)

    def record_poll_error(self, worker_id: str, error_type: str):
        self.poll_errors_total.labels(worker_id=worker_id, error_type=error_type).inc()

    def record_queue_depth(self, workflow_name: str, depth: int):
        self.queue_depth.labels(workflow_name=workflow_name, status="pending").set(
            depth
        )

    def update_worker_uptime(self, worker_id: str, uptime_seconds: float):
        self.worker_uptime.labels(worker_id=worker_id).set(uptime_seconds)

    def update_last_poll_timestamp(self, worker_id: str, timestamp: float):
        self.worker_last_poll.labels(worker_id=worker_id).set(timestamp)
