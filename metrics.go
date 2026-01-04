package simpleworkflow

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MetricsCollector interface allows optional metrics collection
// Implementations can track workflow execution metrics for observability
type MetricsCollector interface {
	// RecordIntentClaimed tracks when a workflow intent is claimed by a worker
	RecordIntentClaimed(workflowName, workerID string)

	// RecordIntentCompleted tracks workflow completion with status and duration
	// Status can be: "succeeded", "failed", "deadletter"
	RecordIntentCompleted(workflowName, workerID, status string, duration time.Duration)

	// RecordIntentDeadletter tracks workflows moved to deadletter queue (PRIORITY METRIC)
	RecordIntentDeadletter(workflowName, workerID string)

	// RecordFailedAttempt tracks individual workflow execution failures before deadletter
	RecordFailedAttempt(workflowName, workerID string, attemptNumber int)

	// RecordPollCycle tracks polling activity
	RecordPollCycle(workerID string)

	// RecordPollError tracks polling errors by type
	RecordPollError(workerID string, errorType string)

	// RecordQueueDepth updates the current queue depth gauge
	RecordQueueDepth(workflowName string, status string, depth int)
}

// PrometheusMetrics implements MetricsCollector using Prometheus
type PrometheusMetrics struct {
	// Intent metrics
	intentClaimedTotal      *prometheus.CounterVec
	intentCompletedTotal    *prometheus.CounterVec
	intentDeadletterTotal   *prometheus.CounterVec
	intentFailedAttempts    *prometheus.CounterVec
	intentExecutionDuration *prometheus.HistogramVec

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
		// Intent claimed counter
		intentClaimedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_claimed_total",
				Help: "Total number of workflow intents claimed by workers",
			},
			[]string{"workflow_name", "worker_id"},
		),

		// Intent completed counter
		intentCompletedTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_completed_total",
				Help: "Total number of workflow intents completed (succeeded/failed/deadletter)",
			},
			[]string{"workflow_name", "worker_id", "status"},
		),

		// Deadletter counter (PRIORITY METRIC for alerting)
		intentDeadletterTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_deadletter_total",
				Help: "Total number of workflow intents moved to deadletter queue",
			},
			[]string{"workflow_name", "worker_id"},
		),

		// Failed attempts counter (tracks failures before deadletter)
		intentFailedAttempts: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_failed_attempts_total",
				Help: "Total number of failed workflow attempts (before deadletter)",
			},
			[]string{"workflow_name", "worker_id", "attempt"},
		),

		// Execution duration histogram (P50, P95, P99)
		intentExecutionDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "workflow_intent_execution_duration_seconds",
				Help:    "Workflow intent execution duration in seconds",
				Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300}, // 100ms to 5min
			},
			[]string{"workflow_name", "worker_id", "status"},
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
				Name: "workflow_intent_queue_depth",
				Help: "Number of workflow intents in queue by status",
			},
			[]string{"workflow_name", "status"},
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
func (m *PrometheusMetrics) RecordIntentClaimed(workflowName, workerID string) {
	m.intentClaimedTotal.WithLabelValues(workflowName, workerID).Inc()
}

// RecordIntentCompleted tracks workflow completion with status and duration
func (m *PrometheusMetrics) RecordIntentCompleted(workflowName, workerID, status string, duration time.Duration) {
	m.intentCompletedTotal.WithLabelValues(workflowName, workerID, status).Inc()
	m.intentExecutionDuration.WithLabelValues(workflowName, workerID, status).Observe(duration.Seconds())
}

// RecordIntentDeadletter increments the deadletter counter (CRITICAL for alerts)
func (m *PrometheusMetrics) RecordIntentDeadletter(workflowName, workerID string) {
	m.intentDeadletterTotal.WithLabelValues(workflowName, workerID).Inc()
}

// RecordFailedAttempt tracks individual failure attempts
func (m *PrometheusMetrics) RecordFailedAttempt(workflowName, workerID string, attemptNumber int) {
	attemptLabel := "1"
	if attemptNumber == 2 {
		attemptLabel = "2"
	} else if attemptNumber >= 3 {
		attemptLabel = "3+"
	}
	m.intentFailedAttempts.WithLabelValues(workflowName, workerID, attemptLabel).Inc()
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
func (m *PrometheusMetrics) RecordQueueDepth(workflowName string, status string, depth int) {
	m.queueDepth.WithLabelValues(workflowName, status).Set(float64(depth))
}
