// Copyright (c) 2025 Reliant Labs
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// ─── HTTP Metrics ───────────────────────────────────────────────────────────

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total HTTP requests by method, path, and status code.",
		},
		[]string{"method", "path", "status"},
	)
	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)
	HTTPInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "reliant",
			Subsystem: "http",
			Name:      "in_flight_requests",
			Help:      "Number of in-flight HTTP requests.",
		},
	)
)

// ─── gRPC / ConnectRPC Metrics ──────────────────────────────────────────────

var (
	GRPCRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "grpc",
			Name:      "requests_total",
			Help:      "Total gRPC/ConnectRPC requests by service, method, and status code.",
		},
		[]string{"service", "method", "code"},
	)
	GRPCRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "grpc",
			Name:      "request_duration_seconds",
			Help:      "gRPC/ConnectRPC request latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"service", "method", "code"},
	)
	GRPCInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "reliant",
			Subsystem: "grpc",
			Name:      "in_flight_requests",
			Help:      "Number of in-flight gRPC/ConnectRPC requests.",
		},
	)
	SlowRPCTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "grpc",
			Name:      "slow_rpc_total",
			Help:      "Unary RPCs flagged by the slow-RPC watchdog, by procedure and stage (in_flight = still running past the threshold, completed = finished but slow).",
		},
		[]string{"procedure", "stage"},
	)
)

// ─── NATS Metrics ───────────────────────────────────────────────────────────

var (
	NATSPublishTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "nats",
			Name:      "publish_total",
			Help:      "Total NATS messages published by subject pattern.",
		},
		[]string{"subject"},
	)
	NATSReceiveTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "nats",
			Name:      "receive_total",
			Help:      "Total NATS messages received by subject pattern.",
		},
		[]string{"subject"},
	)
	NATSRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "nats",
			Name:      "request_duration_seconds",
			Help:      "NATS request-reply latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"subject"},
	)
	NATSErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "nats",
			Name:      "errors_total",
			Help:      "Total NATS operation errors by subject and error type.",
		},
		[]string{"subject", "error_type"},
	)
)

// ─── Database Metrics ───────────────────────────────────────────────────────

var (
	DBQueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "db",
			Name:      "query_duration_seconds",
			Help:      "Database query latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"operation", "driver"},
	)
	DBErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "db",
			Name:      "errors_total",
			Help:      "Total database errors by operation.",
		},
		[]string{"operation", "driver"},
	)
	DBPendingWrites = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "reliant",
			Subsystem: "db",
			Name:      "pending_writes",
			Help:      "Number of pending database write operations.",
		},
	)
	DBPeakPendingWrites = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "reliant",
			Subsystem: "db",
			Name:      "peak_pending_writes",
			Help:      "Peak number of pending database write operations.",
		},
	)
)

// ─── LLM Metrics ────────────────────────────────────────────────────────────

var (
	LLMRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total LLM API requests by provider and status.",
		},
		[]string{"provider", "status"},
	)
	LLMRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "LLM API request latency in seconds.",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		},
		[]string{"provider"},
	)
	LLMTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total LLM tokens used by provider and direction.",
		},
		[]string{"provider", "direction"}, // direction: "input" or "output"
	)
	LLMStreamDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "llm",
			Name:      "stream_duration_seconds",
			Help:      "LLM streaming response duration in seconds.",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"provider"},
	)
)

// ─── Dead-End Error Metrics ─────────────────────────────────────────────────

var (
	DeadEndErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Name:      "dead_end_errors_total",
			Help:      "Errors that are logged but not propagated to callers.",
		},
		[]string{"level", "package", "message"},
	)
	StreamingErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "streaming",
			Name:      "errors_total",
			Help:      "Streaming delta errors by type.",
		},
		[]string{"error_type"},
	)
	ToolExecutionErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "tool_execution",
			Name:      "errors_total",
			Help:      "Tool execution errors by type.",
		},
		[]string{"error_type"},
	)
)

// ─── Workflow Reconciler Metrics ────────────────────────────────────────────

var (
	// ReconcilerAnomaliesTotal counts anomalies the workflow reconciler
	// detected and/or repaired, labeled by anomaly class:
	//   stuck_reset              - lost task recovered via workflow reset
	//   wedge_terminated         - workflow task failing forever; terminated
	//   lost_workflow_repaired   - workflow gone from Temporal; DB repaired
	//   progress_stall_detected  - running workflow with no pending work and
	//                              no history growth past the detection window
	//   progress_stall_confirmed - stall persisted through the confirmation
	//                              window; terminated + marked failed
	//   reset_failed_terminated  - stuck-task reset failed; terminate fallback
	//   reset_attempts_exhausted - reset-attempt guard gave up (repeated resets
	//                              made no progress); terminate + coarse-restart
	//   orphaned_agent_messages_resolved
	//                            - mailbox rows queued for a thread that exited
	//                              before draining them; marked undelivered
	//   orphan_thread_reaped     - thread whose workflow is terminal but the
	//                              thread itself was still running/paused;
	//                              moved to the workflow's status
	//   silent_terminal_drift    - run ended terminally in Temporal (typically
	//                              a hard terminate) without ever reporting
	//                              it; DB repaired and the user notified
	// Every increment is paired with a Sentry-visible ERROR log; alert on any
	// sustained non-zero rate.
	ReconcilerAnomaliesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "reconciler",
			Name:      "anomalies_total",
			Help:      "Workflow reconciler anomalies by class (stuck_reset, wedge_terminated, lost_workflow_repaired, progress_stall_detected, progress_stall_confirmed, reset_failed_terminated, reset_attempts_exhausted, orphaned_agent_messages_resolved, stranded_background_spawn_repaired, stranded_background_spawn_undeliverable, orphan_thread_reaped, silent_terminal_drift).",
		},
		[]string{"class"},
	)
)

// ─── Workflow Resume Metrics ────────────────────────────────────────────────

var (
	// WorkflowResumeOutcomeTotal counts how an interrupted run's resume was
	// served, by outcome:
	//   reset_replay              - reset-and-replay SUCCEEDED. Temporal
	//                               replayed the recorded history and rebuilt
	//                               the entire nested engine stack, including
	//                               in-memory node outputs. The good path.
	//   history_limit_exceeded    - the run was at Temporal's per-execution
	//                               history cap. Reset structurally CANNOT
	//                               help: it forks from inside the oversized
	//                               history. Fell back to coarse restart.
	//   no_replayable_history     - ghost execution: past retention, never
	//                               recorded, or not in an eligible state.
	//   reset_attempts_exhausted  - the bounded guard gave up after repeated
	//                               resets made no forward progress.
	//   reset_error               - the reset itself failed unexpectedly.
	//
	// THIS COUNTER DECIDES WHETHER THE POSITION STACK'S READ SIDE IS BUILT.
	// The position stack is only worth reading for runs that replay cannot
	// serve, which is precisely the three non-reset_replay fallback labels.
	// If reset_replay dominates in practice, the correct outcome is to keep
	// reset-and-replay as THE resume mechanism and leave the stack as
	// diagnostics. Read it as a ratio, not an absolute:
	//
	//   sum(reliant_workflow_resume_outcome_total{outcome!="reset_replay"})
	//     / sum(reliant_workflow_resume_outcome_total)
	//
	// Every increment is paired with a structured log carrying the same
	// outcome label, so the same ratio is recoverable from logs alone where
	// Prometheus is not scraped (single-user desktop installs).
	WorkflowResumeOutcomeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "workflow",
			Name:      "resume_outcome_total",
			Help:      "Interrupted-workflow resume attempts by outcome (reset_replay, history_limit_exceeded, no_replayable_history, reset_attempts_exhausted, reset_error). Non-reset_replay outcomes are the cases a position stack would have to serve.",
		},
		[]string{"outcome"},
	)
)

// ─── Temporal Metrics ───────────────────────────────────────────────────────

var (
	TemporalWorkflowsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "reliant",
			Subsystem: "temporal",
			Name:      "workflows_total",
			Help:      "Total Temporal workflow executions by type and status.",
		},
		[]string{"workflow_type", "status"},
	)
	TemporalActivityDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "reliant",
			Subsystem: "temporal",
			Name:      "activity_duration_seconds",
			Help:      "Temporal activity execution duration in seconds.",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
		},
		[]string{"activity_type"},
	)
)

// initMetrics registers all Prometheus collectors with the global registry.
func initMetrics() {
	// Standard Go runtime and process collectors.
	Registry.MustRegister(collectors.NewGoCollector())
	Registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Application metrics.
	Registry.MustRegister(
		// HTTP
		HTTPRequestsTotal,
		HTTPRequestDuration,
		HTTPInFlight,
		// gRPC
		GRPCRequestsTotal,
		GRPCRequestDuration,
		GRPCInFlight,
		SlowRPCTotal,
		// NATS
		NATSPublishTotal,
		NATSReceiveTotal,
		NATSRequestDuration,
		NATSErrorsTotal,
		// DB
		DBQueryDuration,
		DBErrorsTotal,
		DBPendingWrites,
		DBPeakPendingWrites,
		// LLM
		LLMRequestsTotal,
		LLMRequestDuration,
		LLMTokensTotal,
		LLMStreamDuration,
		// Dead-end errors
		DeadEndErrorsTotal,
		StreamingErrorsTotal,
		ToolExecutionErrorsTotal,
		// Reconciler
		ReconcilerAnomaliesTotal,
		// Workflow resume
		WorkflowResumeOutcomeTotal,
		// Temporal
		TemporalWorkflowsTotal,
		TemporalActivityDuration,
	)
}

// initOTelMetrics creates an OTel MeterProvider backed by the Prometheus registry,
// enabling OTel-instrumented libraries (otelhttp, etc.) to export via /metrics.
func initOTelMetrics() (*sdkmetric.MeterProvider, error) {
	exporter, err := otelprom.New(
		otelprom.WithRegisterer(Registry),
	)
	if err != nil {
		return nil, err
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
	)
	otel.SetMeterProvider(mp)
	return mp, nil
}
