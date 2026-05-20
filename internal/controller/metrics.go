package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// taskCreatedTotal counts the total number of Tasks for which a Job was created.
	taskCreatedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_created_total",
			Help: "Total number of Tasks for which a Job was created",
		},
		[]string{"namespace", "type"},
	)

	// taskCompletedTotal counts the total number of Tasks that reached a terminal phase.
	taskCompletedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_completed_total",
			Help: "Total number of Tasks that reached a terminal phase",
		},
		[]string{"namespace", "type", "phase"},
	)

	// taskDurationSeconds records the duration of Task execution.
	taskDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kelos_task_duration_seconds",
			Help:    "Duration of Task execution from start to completion",
			Buckets: []float64{30, 60, 120, 300, 600, 1200, 1800, 3600},
		},
		[]string{"namespace", "type", "phase"},
	)

	// reconcileErrorsTotal counts the total number of reconciliation errors.
	reconcileErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_reconcile_errors_total",
			Help: "Total number of reconciliation errors",
		},
		[]string{"controller"},
	)

	// taskCostUSD records the cost in USD of completed Tasks.
	taskCostUSD = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_cost_usd_total",
			Help: "Total cost in USD of completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)

	// taskInputTokens records the total input tokens consumed by completed Tasks.
	taskInputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_input_tokens_total",
			Help: "Total input tokens consumed by completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)

	// taskOutputTokens records the total output tokens consumed by completed Tasks.
	taskOutputTokens = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_task_output_tokens_total",
			Help: "Total output tokens consumed by completed Tasks",
		},
		[]string{"namespace", "type", "spawner", "model"},
	)

	// sessionPodsReady tracks the number of ready session pods per spawner.
	sessionPodsReady = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kelos_session_pods_ready",
			Help: "Number of ready session pods",
		},
		[]string{"namespace", "spawner"},
	)

	// sessionPodsBusy tracks the number of session pods with assigned tasks.
	sessionPodsBusy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kelos_session_pods_busy",
			Help: "Number of session pods with assigned tasks",
		},
		[]string{"namespace", "spawner"},
	)

	// sessionPodsIdle tracks the number of session pods without assigned tasks.
	sessionPodsIdle = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kelos_session_pods_idle",
			Help: "Number of session pods without assigned tasks",
		},
		[]string{"namespace", "spawner"},
	)

	// sessionTasksQueued tracks the number of queued tasks per spawner.
	sessionTasksQueued = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kelos_session_tasks_queued",
			Help: "Number of tasks in Queued phase for persistent-mode spawners",
		},
		[]string{"namespace", "spawner"},
	)

	// sessionDesiredReplicas tracks the computed desired replica count.
	sessionDesiredReplicas = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kelos_session_desired_replicas",
			Help: "Computed desired replica count for session StatefulSet",
		},
		[]string{"namespace", "spawner"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		taskCreatedTotal,
		taskCompletedTotal,
		taskDurationSeconds,
		reconcileErrorsTotal,
		taskCostUSD,
		taskInputTokens,
		taskOutputTokens,
		sessionPodsReady,
		sessionPodsBusy,
		sessionPodsIdle,
		sessionTasksQueued,
		sessionDesiredReplicas,
	)
}
