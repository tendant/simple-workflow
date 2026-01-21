package simpleworkflow

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MetricsCollector interface allows optional metrics collection
// Implementations can track workflow execution metrics for observability
type MetricsCollector interface {
	// RecordIntentClaimed tracks when a workflow run is claimed by a worker
	RecordIntentClaimed(workflowType, workerID string)

	// RecordIntentCompleted tracks workflow completion with status and duration
	// Status can be: "succeeded", "failed", "cancelled"
	RecordIntentCompleted(workflowType, workerID, status string, duration time.Duration)

	// RecordIntentDeadletter tracks workflows that permanently failed (PRIORITY METRIC)
	RecordIntentDeadletter(workflowType, workerID string)

	// RecordFailedAttempt tracks individual workflow execution failures before permanent failure
	RecordFailedAttempt(workflowType, workerID string, attemptNumber int)

	// RecordPollCycle tracks polling activity
	RecordPollCycle(workerID string)

	// RecordPollError tracks polling errors by type
	RecordPollError(workerID string, errorType string)

	// RecordQueueDepth updates the current queue depth gauge
	RecordQueueDepth(workflowType string, status string, depth int)
}

// PrometheusMetrics implements MetricsCollector using Prometheus
type PrometheusMetrics struct {
	// Workflow run metrics
	runClaimedTotal      *prometheus.CounterVec
	runCompletedTotal    *prometheus.CounterVec
	runFailedTotal       *prometheus.CounterVec
	runFailedAttempts    *prometheus.CounterVec
	runExecutionDuration *prometheus.HistogramVec

	// Worker metrics
	pollCycleTotal  *prometheus.CounterVec
	pollErrorsTotal *prometheus.CounterVec
	queueDepth      *prometheus.GaugeVec
	workerUptime    *prometheus.GaugeVec
	workerLastPoll  *prometheus.GaugeVec

	startTime time.Time
}

// NewPrometheusMetrics creates a new Prometheus metrics collector
// Pass nil for registry to use the default Prometheus registry
func NewPrometheusMetrics(registry prometheus.Registerer) *PrometheusMetrics {
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}

	factory := promauto.With(registry)

	metrics := &PrometheusMetrics{
		// Workflow run claimed counter
		runClaimedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_run_claimed_total",
				Help: "Total number of workflow runs claimed by workers",
			},
			[]string{"workflow_type", "worker_id"},
		),

		// Workflow run completed counter
		runCompletedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_run_completed_total",
				Help: "Total number of workflow runs completed (succeeded/failed/cancelled)",
			},
			[]string{"workflow_type", "worker_id", "status"},
		),

		// Failed counter (PRIORITY METRIC for alerting)
		runFailedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_run_failed_total",
				Help: "Total number of workflow runs that permanently failed",
			},
			[]string{"workflow_type", "worker_id"},
		),

		// Failed attempts counter (tracks failures before permanent failure)
		runFailedAttempts: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_run_failed_attempts_total",
				Help: "Total number of failed workflow attempts (before permanent failure)",
			},
			[]string{"workflow_type", "worker_id", "attempt"},
		),

		// Execution duration histogram (P50, P95, P99)
		runExecutionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "workflow_run_execution_duration_seconds",
				Help:    "Workflow run execution duration in seconds",
				Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300}, // 100ms to 5min
			},
			[]string{"workflow_type", "worker_id", "status"},
		),

		// Poll cycle counter
		pollCycleTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_worker_poll_cycle_total",
				Help: "Total number of poll cycles executed by worker",
			},
			[]string{"worker_id"},
		),

		// Poll errors counter
		pollErrorsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_worker_poll_errors_total",
				Help: "Total number of poll errors by type",
			},
			[]string{"worker_id", "error_type"},
		),

		// Queue depth gauge
		queueDepth: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_run_queue_depth",
				Help: "Number of workflow runs in queue by status",
			},
			[]string{"workflow_type", "status"},
		),

		// Worker uptime gauge
		workerUptime: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_worker_uptime_seconds",
				Help: "Worker uptime in seconds",
			},
			[]string{"worker_id"},
		),

		// Last poll timestamp gauge
		workerLastPoll: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_worker_last_poll_timestamp",
				Help: "Unix timestamp of last successful poll",
			},
			[]string{"worker_id"},
		),

		startTime: time.Now(),
	}

	return metrics
}

// RecordIntentClaimed increments the claimed counter
func (m *PrometheusMetrics) RecordIntentClaimed(workflowType, workerID string) {
	m.runClaimedTotal.WithLabelValues(workflowType, workerID).Inc()
}

// RecordIntentCompleted tracks workflow completion with status and duration
func (m *PrometheusMetrics) RecordIntentCompleted(workflowType, workerID, status string, duration time.Duration) {
	m.runCompletedTotal.WithLabelValues(workflowType, workerID, status).Inc()
	m.runExecutionDuration.WithLabelValues(workflowType, workerID, status).Observe(duration.Seconds())
}

// RecordIntentDeadletter increments the failed counter (CRITICAL for alerts)
func (m *PrometheusMetrics) RecordIntentDeadletter(workflowType, workerID string) {
	m.runFailedTotal.WithLabelValues(workflowType, workerID).Inc()
}

// RecordFailedAttempt tracks individual failure attempts
func (m *PrometheusMetrics) RecordFailedAttempt(workflowType, workerID string, attemptNumber int) {
	attemptLabel := "1"
	if attemptNumber == 2 {
		attemptLabel = "2"
	} else if attemptNumber >= 3 {
		attemptLabel = "3+"
	}
	m.runFailedAttempts.WithLabelValues(workflowType, workerID, attemptLabel).Inc()
}

// RecordPollCycle increments poll cycle counter and updates worker health gauges
func (m *PrometheusMetrics) RecordPollCycle(workerID string) {
	m.pollCycleTotal.WithLabelValues(workerID).Inc()
	m.workerLastPoll.WithLabelValues(workerID).Set(float64(time.Now().Unix()))
	m.workerUptime.WithLabelValues(workerID).Set(time.Since(m.startTime).Seconds())
}

// RecordPollError increments poll error counter
func (m *PrometheusMetrics) RecordPollError(workerID string, errorType string) {
	m.pollErrorsTotal.WithLabelValues(workerID, errorType).Inc()
}

// RecordQueueDepth sets the current queue depth gauge
func (m *PrometheusMetrics) RecordQueueDepth(workflowType string, status string, depth int) {
	m.queueDepth.WithLabelValues(workflowType, status).Set(float64(depth))
}
