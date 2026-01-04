package simpleworkflow

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// MetricsCollector defines the interface for collecting workflow metrics
// This allows metrics to be optional (pass nil to disable) and supports custom implementations
type MetricsCollector interface {
	// RecordIntentClaimed records when a worker claims an intent
	RecordIntentClaimed(workflowName, workerID string)

	// RecordIntentCompleted records when an intent execution completes (succeeded, failed, or deadletter)
	RecordIntentCompleted(workflowName, workerID, status string, duration time.Duration)

	// RecordIntentDeadletter records when an intent is moved to deadletter status
	RecordIntentDeadletter(workflowName, workerID string)

	// RecordPollCycle records a completed poll cycle
	RecordPollCycle(workerID string)

	// RecordPollError records a polling error
	RecordPollError(workerID string, errorType string)

	// RecordQueueDepth records the current queue depth for a workflow
	RecordQueueDepth(workflowName string, depth int)

	// UpdateWorkerUptime updates the worker uptime gauge
	UpdateWorkerUptime(workerID string, uptimeSeconds float64)

	// UpdateLastPollTimestamp updates the timestamp of the last poll
	UpdateLastPollTimestamp(workerID string, timestamp float64)
}

// PrometheusMetrics implements MetricsCollector using Prometheus client library
type PrometheusMetrics struct {
	intentClaimedTotal      *prometheus.CounterVec
	intentCompletedTotal    *prometheus.CounterVec
	intentDeadletterTotal   *prometheus.CounterVec
	intentExecutionDuration *prometheus.HistogramVec
	pollCycleTotal          *prometheus.CounterVec
	pollErrorsTotal         *prometheus.CounterVec
	queueDepth              *prometheus.GaugeVec
	workerUptime            *prometheus.GaugeVec
	workerLastPoll          *prometheus.GaugeVec
}

// NewPrometheusMetrics creates a new Prometheus metrics collector
// Pass nil for registry to use the default Prometheus registry
func NewPrometheusMetrics(registry prometheus.Registerer) *PrometheusMetrics {
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}

	m := &PrometheusMetrics{
		intentClaimedTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_claimed_total",
				Help: "Total number of workflow intents claimed by workers",
			},
			[]string{"workflow_name", "worker_id"},
		),

		intentCompletedTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_completed_total",
				Help: "Total number of workflow intents completed (succeeded, failed, or deadletter)",
			},
			[]string{"workflow_name", "worker_id", "status"},
		),

		intentDeadletterTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_intent_deadletter_total",
				Help: "Total number of workflow intents moved to deadletter status",
			},
			[]string{"workflow_name", "worker_id"},
		),

		intentExecutionDuration: promauto.With(registry).NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "workflow_intent_execution_duration_seconds",
				Help:    "Histogram of workflow intent execution duration in seconds",
				Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300}, // 100ms to 5 minutes
			},
			[]string{"workflow_name", "worker_id", "status"},
		),

		pollCycleTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_worker_poll_cycle_total",
				Help: "Total number of poll cycles executed by workers",
			},
			[]string{"worker_id"},
		),

		pollErrorsTotal: promauto.With(registry).NewCounterVec(
			prometheus.CounterOpts{
				Name: "workflow_worker_poll_errors_total",
				Help: "Total number of poll errors encountered by workers",
			},
			[]string{"worker_id", "error_type"},
		),

		queueDepth: promauto.With(registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_intent_queue_depth",
				Help: "Number of pending workflow intents in the queue",
			},
			[]string{"workflow_name", "status"},
		),

		workerUptime: promauto.With(registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_worker_uptime_seconds",
				Help: "Worker uptime in seconds",
			},
			[]string{"worker_id"},
		),

		workerLastPoll: promauto.With(registry).NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "workflow_worker_last_poll_timestamp",
				Help: "Unix timestamp of the last successful poll",
			},
			[]string{"worker_id"},
		),
	}

	return m
}

// RecordIntentClaimed implements MetricsCollector
func (m *PrometheusMetrics) RecordIntentClaimed(workflowName, workerID string) {
	m.intentClaimedTotal.WithLabelValues(workflowName, workerID).Inc()
}

// RecordIntentCompleted implements MetricsCollector
func (m *PrometheusMetrics) RecordIntentCompleted(workflowName, workerID, status string, duration time.Duration) {
	m.intentCompletedTotal.WithLabelValues(workflowName, workerID, status).Inc()
	m.intentExecutionDuration.WithLabelValues(workflowName, workerID, status).Observe(duration.Seconds())
}

// RecordIntentDeadletter implements MetricsCollector
func (m *PrometheusMetrics) RecordIntentDeadletter(workflowName, workerID string) {
	m.intentDeadletterTotal.WithLabelValues(workflowName, workerID).Inc()
}

// RecordPollCycle implements MetricsCollector
func (m *PrometheusMetrics) RecordPollCycle(workerID string) {
	m.pollCycleTotal.WithLabelValues(workerID).Inc()
}

// RecordPollError implements MetricsCollector
func (m *PrometheusMetrics) RecordPollError(workerID string, errorType string) {
	m.pollErrorsTotal.WithLabelValues(workerID, errorType).Inc()
}

// RecordQueueDepth implements MetricsCollector
func (m *PrometheusMetrics) RecordQueueDepth(workflowName string, depth int) {
	m.queueDepth.WithLabelValues(workflowName, "pending").Set(float64(depth))
}

// UpdateWorkerUptime implements MetricsCollector
func (m *PrometheusMetrics) UpdateWorkerUptime(workerID string, uptimeSeconds float64) {
	m.workerUptime.WithLabelValues(workerID).Set(uptimeSeconds)
}

// UpdateLastPollTimestamp implements MetricsCollector
func (m *PrometheusMetrics) UpdateLastPollTimestamp(workerID string, timestamp float64) {
	m.workerLastPoll.WithLabelValues(workerID).Set(timestamp)
}

// classifyError classifies an error into a type for metrics
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}

	errStr := err.Error()

	// Database errors
	if contains(errStr, "connection", "timeout", "deadline") {
		return "db_connection"
	}
	if contains(errStr, "syntax", "query") {
		return "db_query"
	}

	// General errors
	return "other"
}

// contains checks if any of the substrings are in the string
func contains(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				match := true
				for j := 0; j < len(sub); j++ {
					if s[i+j] != sub[j] {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
		}
	}
	return false
}
