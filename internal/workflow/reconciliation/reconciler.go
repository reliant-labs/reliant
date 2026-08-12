// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// Reconciler detects and repairs mismatches between Temporal workflow state
// and database state. Temporal is the source of truth for execution state.
// The DB workflows.status is a cache that can become stale when:
// - Temporal workflow crashes before completion activity runs
// - Server restarts during workflow execution
// - Temporal workflow expires (retention period)
// - Activity failures prevent status updates
//
// When drift is detected, the Reconciler updates the DB to match Temporal
// using UpdateWorkflowStatus, which triggers activity event emission to
// update the frontend in real-time.
//
// The Reconciler also handles truly stuck tasks (activities/workflow tasks
// lost from Temporal's queue). A stuck task is only acted on after it has
// been observed stuck across multiple consecutive reconcile passes while the
// task queue had active pollers (a worker that is down or rebuilding is NOT
// a stuck task — Temporal re-dispatches everything when the worker returns).
// Once confirmed, recovery is attempted via workflow reset (replay from just
// before the lost task); termination + mark-failed is the fallback only if
// the reset fails.
//
// Finally, a cause-agnostic PROGRESS WATCHDOG covers the failure classes the
// enumerated detectors (stuck-in-Scheduled, wedged-task) cannot: a RUNNING
// workflow that the user is waiting on, with ZERO pending work in Temporal
// (no pending workflow task, activities, children, or nexus ops) and a
// HistoryLength that stops growing, is a workflow that claims to be healthy
// while doing nothing. Legitimate user-input waits are excluded (see
// observeProgress for the discriminator). Detection only alerts; a further
// confirmation window must elapse before the workflow is terminated, marked
// failed (checkpoint preserved), and the user is told how to resume.
//
// Every anomaly class increments reliant_reconciler_anomalies_total and logs
// at ERROR (which the logging package forwards to Sentry).
type Reconciler struct {
	repo       db.Repository
	tempClient client.Client

	// Background polling state
	mu          sync.Mutex
	stopPolling chan struct{}
	pollDone    chan struct{}
	isRunning   bool

	// Configuration
	pollInterval            time.Duration
	stuckActivityThreshold  time.Duration
	taskQueue               string
	namespace               string
	stuckConfirmationPasses int
	stuckConfirmationWindow time.Duration
	wedgeAttemptThreshold   int
	progressStallPasses     int
	progressStallWindow     time.Duration

	// stuckMu guards stuckObservations, the in-memory debounce state for
	// stuck-task handling. In-memory tracking is acceptable here: a single
	// reconciler process runs per deployment, and even if multiple replicas
	// ran, each replica debouncing independently only makes destructive
	// actions rarer (each must confirm on its own), while the
	// CompareAndSwapWorkflowStatus CAS already guards against duplicate
	// terminal transitions. A process restart merely restarts the debounce.
	stuckMu           sync.Mutex
	stuckObservations map[string]*stuckObservation

	// progressMu guards progressObservations, the in-memory streak tracking
	// for the cause-agnostic progress watchdog. Same in-memory rationale as
	// stuckObservations: a restart merely restarts the streak, and the CAS
	// transition guards duplicate terminal actions.
	progressMu           sync.Mutex
	progressObservations map[string]*progressObservation

	// resetGuard bounds reset-and-replay attempts per workflow so a
	// deterministically-failing run is not reset forever. Optional (nil = always
	// allow); shared with the PauseService resume path via SetResetGuard so both
	// resetters count against a single per-workflow bound.
	resetGuard *v2workflow.ResetAttemptGuard
}

// SetResetGuard installs the shared reset-attempt guard (see
// v2workflow.ResetAttemptGuard). Nil-safe: an unset guard always allows resets.
func (r *Reconciler) SetResetGuard(g *v2workflow.ResetAttemptGuard) {
	r.resetGuard = g
}

// stuckObservation tracks one workflow's stuck task across reconcile passes.
type stuckObservation struct {
	taskType      string    // "workflow" or "activity"
	activityID    string    // only set for taskType == "activity"
	firstObserved time.Time // when this stuck task was first seen (pollers active)
	passes        int       // consecutive poller-active passes observing this same stuck task
}

// progressObservation tracks one RUNNING workflow's static-history streak for
// the cause-agnostic progress watchdog. A streak only accumulates while the
// workflow is in the suspicious "quiescent" shape (running, zero pending
// work); any pass with pending work, or any HistoryLength change, resets it.
type progressObservation struct {
	historyLength int64     // HistoryLength the streak is anchored to
	firstObserved time.Time // when this static-history streak started
	passes        int       // consecutive quiescent passes at this historyLength
	detected      bool      // detection-stage anomaly already reported once
}

// Anomaly classes for metrics/alerting. These are the label values of
// reliant_reconciler_anomalies_total; keep them in sync with the metric's
// help text in internal/observability/metrics.go.
const (
	anomalyStuckReset                      = "stuck_reset"
	anomalyWedgeTerminated                 = "wedge_terminated"
	anomalyLostWorkflowRepaired            = "lost_workflow_repaired"
	anomalyProgressStallDetected           = "progress_stall_detected"
	anomalyProgressStallConfirmed          = "progress_stall_confirmed"
	anomalyResetFailedTerminated           = "reset_failed_terminated"
	anomalyResetAttemptsExhausted          = "reset_attempts_exhausted"
	anomalyOrphanDescendantReaped          = "orphan_descendant_reaped"
	anomalyStrandedSpawnRepaired           = "stranded_spawn_repaired"
	anomalyStrandedBackgroundSpawnRepaired = "stranded_background_spawn_repaired"
	anomalyOrphanedAgentMessagesResolved   = "orphaned_agent_messages_resolved"
)

// Debounce defaults: a stuck task must be observed on at least
// DefaultStuckConfirmationPasses consecutive poller-active reconcile passes,
// spanning at least DefaultStuckConfirmationWindow, before any recovery
// action is taken. Both must hold. This absorbs transient scheduling delays
// and worker restarts that DescribeTaskQueue's poller history hasn't aged
// out yet.
const (
	DefaultStuckConfirmationPasses = 3
	DefaultStuckConfirmationWindow = 3 * time.Minute
)

// DefaultWedgeAttemptThreshold is the pending WORKFLOW task attempt count at
// which a running workflow is considered wedged: every workflow task fails and
// is retried forever (the classic cause is a non-deterministic replay error —
// TMPRL1100 — after worker code changed mid-run). Transient workflow task
// failures (worker OOM, deploy blips) resolve within an attempt or two;
// crossing this threshold while pollers are active, sustained across the
// debounce window, means the task can never succeed.
const DefaultWedgeAttemptThreshold = 5

// wedgeObservationTaskType is the debounce identity used for wedged workflow
// tasks, distinct from the "workflow"/"activity" stuck-in-Scheduled classes.
const wedgeObservationTaskType = "wedged-workflow-task"

// Progress-watchdog defaults. This is a tripwire for UNKNOWN failure causes,
// not a hair trigger: a workflow must sit in the quiescent shape (running,
// zero pending work, HistoryLength frozen) for at least
// DefaultProgressStallPasses consecutive passes spanning at least
// DefaultProgressStallWindow before it is even REPORTED (detection stage:
// ERROR log + metric, no action). The destructive response (terminate + mark
// failed + resumable chat message) requires double both thresholds
// (progressStallConfirmMultiplier) — with the default 30s poll interval,
// detection at ~10 minutes and action at ~20 minutes of provable inactivity.
//
// Legitimate long waits do not reach either stage — see observeProgress for
// the pause / ask-question / approval discriminator.
const (
	DefaultProgressStallPasses = 4
	DefaultProgressStallWindow = 10 * time.Minute

	// progressStallConfirmMultiplier scales the detection thresholds (both
	// passes and wall-clock window) up to the confirmation thresholds.
	progressStallConfirmMultiplier = 2
)

// progressStallChatMessage is posted to the chat when a confirmed progress
// stall is terminated. It is accurate because the run is marked failed with
// its position checkpoint preserved, and SendMessage starts the next run in
// resume-at-position mode.
const progressStallChatMessage = "This conversation's workflow stopped making progress and was stopped as a precaution. It will resume where it left off when you send a message."

// wedgeInterruptedChatMessage is posted to the chat when a wedged run is
// terminated. It is accurate because the terminated run is marked failed in
// the DB and SendMessage starts the next run in resume-at-position mode.
const wedgeInterruptedChatMessage = "This conversation's workflow was interrupted by an update and will resume where it left off when you send a message."

// pollerRecencyWindow bounds how old a poller's LastAccessTime may be to
// still count as "active". Temporal's DescribeTaskQueue keeps poller history
// for ~5 minutes after the last poll, so a dead worker is still listed for a
// while; a live worker long-polls at least once a minute, so 2 minutes
// comfortably covers a live worker while aging out a dead one sooner.
const pollerRecencyWindow = 2 * time.Minute

// ReconcilerConfig contains configuration for the Reconciler
type ReconcilerConfig struct {
	// PollInterval is how often the background reconciliation runs
	// Default: 30 seconds
	PollInterval time.Duration

	// StuckActivityThreshold is how long a task can be in "Scheduled" state
	// without being picked up before it's considered a stuck-task
	// observation (recovery only happens after the debounce confirms it).
	// Default: 30 seconds
	StuckActivityThreshold time.Duration

	// TaskQueue is the Temporal task queue whose pollers gate stuck-task
	// handling. Default: the shared workflow task queue.
	TaskQueue string

	// Namespace is the Temporal namespace used for reset requests.
	// Default: the reliant namespace.
	Namespace string

	// StuckConfirmationPasses is how many consecutive poller-active
	// reconcile passes must observe the same stuck task before recovery.
	// Default: DefaultStuckConfirmationPasses.
	StuckConfirmationPasses int

	// StuckConfirmationWindow is the minimum wall-clock time a stuck task
	// must persist (with pollers active) before recovery.
	// Default: DefaultStuckConfirmationWindow.
	StuckConfirmationWindow time.Duration

	// WedgeAttemptThreshold is the pending workflow task attempt count at
	// which a workflow is treated as wedged (workflow task failing forever,
	// e.g. non-deterministic replay after a code update).
	// Default: DefaultWedgeAttemptThreshold.
	WedgeAttemptThreshold int

	// ProgressStallPasses is how many consecutive quiescent passes (running,
	// zero pending work, HistoryLength frozen) must observe a workflow before
	// a progress stall is REPORTED. Action requires double this.
	// Default: DefaultProgressStallPasses.
	ProgressStallPasses int

	// ProgressStallWindow is the minimum wall-clock time a static-history
	// streak must span before a progress stall is REPORTED. Action requires
	// double this. Default: DefaultProgressStallWindow.
	ProgressStallWindow time.Duration
}

// DefaultConfig returns the default reconciler configuration
func DefaultConfig() *ReconcilerConfig {
	return &ReconcilerConfig{
		PollInterval:            30 * time.Second,
		StuckActivityThreshold:  30 * time.Second,
		TaskQueue:               v2workflow.SharedTaskQueue,
		Namespace:               v2workflow.TemporalNamespace,
		StuckConfirmationPasses: DefaultStuckConfirmationPasses,
		StuckConfirmationWindow: DefaultStuckConfirmationWindow,
		WedgeAttemptThreshold:   DefaultWedgeAttemptThreshold,
		ProgressStallPasses:     DefaultProgressStallPasses,
		ProgressStallWindow:     DefaultProgressStallWindow,
	}
}

// NewReconciler creates a new workflow reconciler. Zero-valued config fields
// fall back to DefaultConfig values.
func NewReconciler(repo db.Repository, tempClient client.Client, config *ReconcilerConfig) *Reconciler {
	def := DefaultConfig()
	if config == nil {
		config = def
	}
	cfg := *config
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = def.PollInterval
	}
	if cfg.StuckActivityThreshold <= 0 {
		cfg.StuckActivityThreshold = def.StuckActivityThreshold
	}
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = def.TaskQueue
	}
	if cfg.Namespace == "" {
		cfg.Namespace = def.Namespace
	}
	if cfg.StuckConfirmationPasses <= 0 {
		cfg.StuckConfirmationPasses = def.StuckConfirmationPasses
	}
	if cfg.StuckConfirmationWindow <= 0 {
		cfg.StuckConfirmationWindow = def.StuckConfirmationWindow
	}
	if cfg.WedgeAttemptThreshold <= 0 {
		cfg.WedgeAttemptThreshold = def.WedgeAttemptThreshold
	}
	if cfg.ProgressStallPasses <= 0 {
		cfg.ProgressStallPasses = def.ProgressStallPasses
	}
	if cfg.ProgressStallWindow <= 0 {
		cfg.ProgressStallWindow = def.ProgressStallWindow
	}
	return &Reconciler{
		repo:                    repo,
		tempClient:              tempClient,
		pollInterval:            cfg.PollInterval,
		stuckActivityThreshold:  cfg.StuckActivityThreshold,
		taskQueue:               cfg.TaskQueue,
		namespace:               cfg.Namespace,
		stuckConfirmationPasses: cfg.StuckConfirmationPasses,
		stuckConfirmationWindow: cfg.StuckConfirmationWindow,
		wedgeAttemptThreshold:   cfg.WedgeAttemptThreshold,
		progressStallPasses:     cfg.ProgressStallPasses,
		progressStallWindow:     cfg.ProgressStallWindow,
		stuckObservations:       make(map[string]*stuckObservation),
		progressObservations:    make(map[string]*progressObservation),
		stopPolling:             make(chan struct{}),
		pollDone:                make(chan struct{}),
	}
}

// TemporalWorkflowState represents the actual state from Temporal
type TemporalWorkflowState struct {
	Exists    bool              // Whether the workflow exists in Temporal
	Status    db.WorkflowStatus // Mapped status (only valid if Exists is true)
	RunID     string            // Current run ID (only valid if Exists is true)
	IsRunning bool              // True if Temporal says workflow is running

	// Stuck task detection (can be activity OR workflow task)
	HasStuckTask       bool          // True if a task is stuck in Scheduled state
	StuckTaskType      string        // "activity" or "workflow"
	StuckActivityID    string        // Activity ID (only if StuckTaskType == "activity")
	StuckActivityType  string        // Activity type name (only if StuckTaskType == "activity")
	StuckTaskScheduled time.Time     // When the stuck task was scheduled
	StuckDuration      time.Duration // How long it's been stuck

	// Wedged workflow task detection: the pending WORKFLOW task is being
	// picked up and failing over and over (attempt count keeps climbing),
	// so it never sits in Scheduled long enough to look "stuck". The classic
	// cause is a non-deterministic replay error after worker code changed
	// mid-run. Reset is USELESS for this class — replay re-diverges — so
	// recovery is terminate + mark failed + resume-at-position on the next
	// user message.
	HasWedgedWorkflowTask bool  // True if pending workflow task attempt >= threshold
	WedgedTaskAttempt     int32 // Attempt count of the wedged workflow task

	// Progress-watchdog inputs (only populated while IsRunning). Together
	// they define the suspicious "quiescent" shape: a running workflow with
	// zero pending work whose HistoryLength has stopped growing.
	HistoryLength          int64 // WorkflowExecutionInfo.HistoryLength
	HasPendingWorkflowTask bool  // any pending workflow task (any state)
	PendingActivityCount   int   // pending activities (any state)
	PendingChildrenCount   int   // pending Temporal child workflows
	PendingNexusCount      int   // pending nexus operations
}

// quiescent reports whether the workflow is in the progress watchdog's
// suspicious shape: Temporal says RUNNING but there is NOTHING in flight —
// no pending workflow task, activities, children, or nexus operations. A
// workflow doing legitimate slow work always has a pending something (a slow
// LLM call is a pending activity; a fired timer becomes a pending workflow
// task). The only legitimate quiescent workflows are signal-parked waits,
// which observeProgress excludes via their DB wait markers.
func (s *TemporalWorkflowState) quiescent() bool {
	return s.IsRunning &&
		!s.HasStuckTask && !s.HasWedgedWorkflowTask &&
		!s.HasPendingWorkflowTask &&
		s.PendingActivityCount == 0 &&
		s.PendingChildrenCount == 0 &&
		s.PendingNexusCount == 0
}

// ReconciliationResult contains the result of reconciling a single workflow
type ReconciliationResult struct {
	WorkflowID       string
	ChatID           string
	DBStatus         db.WorkflowStatus
	TemporalStatus   db.WorkflowStatus
	WasStale         bool // True if DB status was updated
	NeedsRecovery    bool // True if workflow is lost and needs user action
	RecoveredByReset bool // True if a stuck task was recovered via workflow reset
	ProgressStalled  bool // True if a confirmed progress stall was terminated + marked failed
	Error            error
}

// passStats aggregates anomaly counts for one reconcile pass so
// ReconcileRunningWorkflows can emit a single summary line (and stay silent
// on clean passes).
type passStats struct {
	mu     sync.Mutex
	counts map[string]int
}

func (s *passStats) record(class string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[string]int)
	}
	s.counts[class]++
}

// snapshot returns the total anomaly count and a stable "class=n" summary.
func (s *passStats) snapshot() (total int, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.counts) == 0 {
		return 0, ""
	}
	parts := make([]string, 0, len(s.counts))
	for class, n := range s.counts {
		parts = append(parts, fmt.Sprintf("%s=%d", class, n))
		total += n
	}
	sort.Strings(parts)
	return total, strings.Join(parts, " ")
}

// recordAnomaly increments the Prometheus anomaly counter for the class and
// adds it to the per-pass summary. Callers pair every recordAnomaly with a
// context-rich ERROR log (which the logging package forwards to Sentry —
// these classes are actionable and must never be suppressed).
func (r *Reconciler) recordAnomaly(stats *passStats, class string) {
	observability.ReconcilerAnomaliesTotal.WithLabelValues(class).Inc()
	if stats != nil {
		stats.record(class)
	}
}

// pollerState lazily caches DescribeTaskQueue results for a single reconcile
// pass, so a pass makes at most one poller check (one call per task queue
// type) no matter how many workflows are stuck.
type pollerState struct {
	mu              sync.Mutex
	fetched         bool
	workflowPollers bool
	activityPollers bool
	err             error
}

// pollersActive reports whether the reconciler's task queue has active
// pollers for the given stuck task type ("workflow" or "activity"). Results
// are fetched once per reconcile pass and cached. A poller only counts as
// active if it polled within pollerRecencyWindow (Temporal lists pollers for
// ~5 minutes after their last poll, so a freshly-dead worker is still
// listed).
func (r *Reconciler) pollersActive(ctx context.Context, ps *pollerState, taskType string) (bool, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if !ps.fetched {
		ps.fetched = true
		ps.workflowPollers, ps.err = r.queryPollers(ctx, enums.TASK_QUEUE_TYPE_WORKFLOW)
		if ps.err == nil {
			ps.activityPollers, ps.err = r.queryPollers(ctx, enums.TASK_QUEUE_TYPE_ACTIVITY)
		}

		// One summary log line per pass (not per workflow).
		switch {
		case ps.err != nil:
			logging.Warn("[Reconciler] Failed to check task queue pollers - skipping stuck-task handling this pass",
				"taskQueue", r.taskQueue,
				"error", ps.err,
			)
		case !ps.workflowPollers || !ps.activityPollers:
			logging.Info("[Reconciler] Task queue has no active pollers - worker down or rebuilding, skipping stuck-task handling this pass",
				"taskQueue", r.taskQueue,
				"workflowPollers", ps.workflowPollers,
				"activityPollers", ps.activityPollers,
			)
		}
	}

	if ps.err != nil {
		return false, ps.err
	}
	// "activity" gates on activity pollers; "workflow" and the wedged-
	// workflow-task class both gate on workflow pollers.
	if taskType == "activity" {
		return ps.activityPollers, nil
	}
	return ps.workflowPollers, nil
}

// queryPollers returns whether the task queue has at least one recently
// active poller of the given type.
func (r *Reconciler) queryPollers(ctx context.Context, taskQueueType enums.TaskQueueType) (bool, error) {
	resp, err := r.tempClient.DescribeTaskQueue(ctx, r.taskQueue, taskQueueType)
	if err != nil {
		return false, fmt.Errorf("DescribeTaskQueue(%s, %s): %w", r.taskQueue, taskQueueType, err)
	}
	cutoff := time.Now().Add(-pollerRecencyWindow)
	for _, p := range resp.GetPollers() {
		if p.GetLastAccessTime().AsTime().After(cutoff) {
			return true, nil
		}
	}
	return false, nil
}

// getTemporalWorkflowState queries Temporal for the actual workflow state.
// Returns state with Exists=false if workflow not found.
// Also detects stuck activities that have been in "Scheduled" state too long.
func (r *Reconciler) getTemporalWorkflowState(ctx context.Context, workflowID string) (*TemporalWorkflowState, error) {
	descResp, err := r.tempClient.DescribeWorkflowExecution(ctx, workflowID, "")
	if err != nil {
		// Check if it's a "not found" error
		errStr := err.Error()
		if strings.Contains(errStr, "not found") || strings.Contains(errStr, "NotFound") {
			return &TemporalWorkflowState{Exists: false}, nil
		}
		// Unexpected error - return it
		return nil, fmt.Errorf("failed to query Temporal: %w", err)
	}

	if descResp == nil || descResp.WorkflowExecutionInfo == nil {
		return &TemporalWorkflowState{Exists: false}, nil
	}

	execStatus := descResp.WorkflowExecutionInfo.Status
	runID := ""
	if descResp.WorkflowExecutionInfo.Execution != nil {
		runID = descResp.WorkflowExecutionInfo.Execution.RunId
	}

	// Map Temporal status to our status
	var mappedStatus db.WorkflowStatus
	isRunning := false
	switch execStatus {
	case enums.WORKFLOW_EXECUTION_STATUS_RUNNING:
		mappedStatus = db.WorkflowStatusRunning
		isRunning = true
	case enums.WORKFLOW_EXECUTION_STATUS_COMPLETED:
		mappedStatus = db.WorkflowStatusCompleted
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
		// TERMINATED = system/operator kill → Failed (resumable at position).
		// Only CANCELED (user cancel) maps to Cancelled.
		mappedStatus = db.WorkflowStatusFailed
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED:
		mappedStatus = db.WorkflowStatusCancelled
	case enums.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW:
		// Workflow continued - treat as running since there's a new run
		mappedStatus = db.WorkflowStatusRunning
		isRunning = true
	default:
		// Unknown status - treat as completed to allow starting fresh
		logging.Warn("[Reconciler] Unknown Temporal workflow status",
			"workflowID", workflowID,
			"status", execStatus.String(),
		)
		mappedStatus = db.WorkflowStatusCompleted
	}

	state := &TemporalWorkflowState{
		Exists:    true,
		Status:    mappedStatus,
		RunID:     runID,
		IsRunning: isRunning,
	}

	// Check for stuck tasks (only if workflow is running)
	// A task is "stuck" if it's in Scheduled state for longer than the threshold.
	// This indicates the task was lost (not in the task queue) and needs recovery.
	if isRunning {
		now := time.Now()

		// Progress-watchdog inputs: history growth + pending-work census.
		state.HistoryLength = descResp.WorkflowExecutionInfo.HistoryLength
		state.HasPendingWorkflowTask = descResp.PendingWorkflowTask != nil
		state.PendingActivityCount = len(descResp.PendingActivities)
		state.PendingChildrenCount = len(descResp.PendingChildren)
		state.PendingNexusCount = len(descResp.PendingNexusOperations)

		// Wedge check FIRST: a workflow task with a high attempt count is
		// being dispatched and failing repeatedly (e.g. non-deterministic
		// replay error after a code update). It must not be classified as
		// merely "stuck in Scheduled" — the stuck path recovers via reset,
		// which is useless here (replay re-diverges). The attempt count on
		// DescribeWorkflowExecution's PendingWorkflowTask is the cheapest
		// reliable signal for this class: transient failures resolve within
		// an attempt or two, while a wedge climbs forever.
		if pwt := descResp.PendingWorkflowTask; pwt != nil && r.wedgeAttemptThreshold > 0 && pwt.Attempt >= int32(r.wedgeAttemptThreshold) {
			state.HasWedgedWorkflowTask = true
			state.WedgedTaskAttempt = pwt.Attempt

			logging.Debug("[Reconciler] Observed wedged workflow task (repeated failures)",
				"workflowID", workflowID,
				"attempt", pwt.Attempt,
				"taskState", pwt.State.String(),
			)
			return state, nil
		}

		// First check for stuck workflow task (higher priority - workflow can't proceed without it)
		if descResp.PendingWorkflowTask != nil {
			pwt := descResp.PendingWorkflowTask
			if pwt.State == enums.PENDING_WORKFLOW_TASK_STATE_SCHEDULED {
				scheduledTime := pwt.ScheduledTime.AsTime()
				stuckDuration := now.Sub(scheduledTime)

				if stuckDuration > r.stuckActivityThreshold {
					state.HasStuckTask = true
					state.StuckTaskType = "workflow"
					state.StuckTaskScheduled = scheduledTime
					state.StuckDuration = stuckDuration

					// Debug: this fires every pass while a task waits (e.g.
					// during a worker rebuild); action-time logs are louder.
					logging.Debug("[Reconciler] Observed workflow task in Scheduled state past threshold",
						"workflowID", workflowID,
						"scheduledTime", scheduledTime,
						"stuckDuration", stuckDuration,
					)
				}
			}
		}

		// Then check for stuck activities (if no stuck workflow task)
		if !state.HasStuckTask && len(descResp.PendingActivities) > 0 {
			for _, pa := range descResp.PendingActivities {
				// Only check activities in "Scheduled" state (not yet started by a worker)
				if pa.State == enums.PENDING_ACTIVITY_STATE_SCHEDULED {
					scheduledTime := pa.ScheduledTime.AsTime()
					stuckDuration := now.Sub(scheduledTime)

					if stuckDuration > r.stuckActivityThreshold {
						state.HasStuckTask = true
						state.StuckTaskType = "activity"
						state.StuckActivityID = pa.ActivityId
						state.StuckActivityType = pa.ActivityType.GetName()
						state.StuckTaskScheduled = scheduledTime
						state.StuckDuration = stuckDuration

						// Debug: fires every pass while a task waits; see above.
						logging.Debug("[Reconciler] Observed activity in Scheduled state past threshold",
							"workflowID", workflowID,
							"activityID", pa.ActivityId,
							"activityType", pa.ActivityType.GetName(),
							"scheduledTime", scheduledTime,
							"stuckDuration", stuckDuration,
						)
						break // Only report the first stuck activity
					}
				}
			}
		}
	}

	return state, nil
}

// ReconcileWorkflow reconciles a single workflow's status with Temporal.
// Returns a ReconciliationResult with details about what was found/fixed.
func (r *Reconciler) ReconcileWorkflow(ctx context.Context, wf *db.Workflow) *ReconciliationResult {
	return r.reconcileWorkflow(ctx, wf, &pollerState{}, &passStats{})
}

// reconcileWorkflow is ReconcileWorkflow with an explicit per-pass poller
// cache (so ReconcileRunningWorkflows performs at most one poller check per
// pass regardless of how many workflows it reconciles) and a per-pass anomaly
// aggregator for the end-of-pass summary line.
func (r *Reconciler) reconcileWorkflow(ctx context.Context, wf *db.Workflow, pollers *pollerState, stats *passStats) *ReconciliationResult {
	result := &ReconciliationResult{
		WorkflowID: wf.ID,
		ChatID:     wf.ChatID,
		DBStatus:   wf.Status,
	}

	// Skip child/inline workflows - they don't have their own Temporal workflow.
	// Inline workflows (spawns, thread forks) run within their parent's Temporal
	// execution context and don't exist as separate Temporal workflows. Querying
	// Temporal for their IDs would return "not found" and incorrectly mark them
	// as "lost". Their lifecycle is managed by their parent workflow.
	if wf.ParentID != nil {
		return result
	}

	// Reconcile workflows that can drift against Temporal state:
	// - running: should usually map directly to Temporal running/terminal states
	// - paused: may become stale if Temporal execution is gone/terminal
	if wf.Status != db.WorkflowStatusRunning && wf.Status != db.WorkflowStatusPaused {
		return result
	}

	// Query Temporal for actual state
	temporalState, err := r.getTemporalWorkflowState(ctx, wf.ID)
	if err != nil {
		result.Error = err
		return result
	}

	if !temporalState.Exists {
		// Workflow not in Temporal (expired/lost) — repair DB status
		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.WorkflowStatusCompleted, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to mark lost workflow as completed: %w", err)
			return result
		}
		if !swapped {
			return result // another reconciler already handled this
		}

		// ERROR (Sentry-visible): the DB believed this workflow was live but
		// Temporal has no record of it — state was silently lost somewhere.
		logging.Error("[Reconciler] Workflow not found in Temporal, marked as completed",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"dbStatus", wf.Status,
		)
		r.recordAnomaly(stats, anomalyLostWorkflowRepaired)

		// This root workflow just moved to Completed here rather than via the
		// authoritative WorkflowStatus activity, so transition the chat to any
		// declared transition_to target (idempotent no-op if the activity did).
		r.transitionChatOnCompletion(ctx, wf)

		result.TemporalStatus = db.WorkflowStatusCompleted
		result.WasStale = true
		result.NeedsRecovery = true

		return result
	}

	result.TemporalStatus = temporalState.Status

	// Any pass that finds the workflow NOT stuck/wedged clears its debounce
	// state: the tracked task was picked up (or completed), so a later stuck
	// observation starts a fresh confirmation window. A wedged task keeps its
	// streak for PAUSED workflows too — the wedge detector below handles
	// paused executions (a wedged replay can never process its resume) — but
	// the stuck-task path stays running-only, so a paused stuck observation
	// still clears.
	notWedgedOrStuck := !temporalState.HasStuckTask && !temporalState.HasWedgedWorkflowTask
	pausedForWedge := wf.Status == db.WorkflowStatusPaused && temporalState.HasWedgedWorkflowTask
	if notWedgedOrStuck || (wf.Status != db.WorkflowStatusRunning && !pausedForWedge) {
		r.clearStuckObservation(wf.ID)
	}

	// Progress-watchdog bookkeeping: only a RUNNING workflow in the quiescent
	// shape accumulates a stall streak; any other pass resets it. Note that
	// PAUSED workflows never accumulate — pause (user pause, retry-exhaustion
	// self-pause, daemon-offline circuit breaker) always marks the DB status
	// paused, so the user is not awaiting progress.
	suspicious := wf.Status == db.WorkflowStatusRunning && temporalState.quiescent()
	if !suspicious {
		r.clearProgressObservation(wf.ID)
	}

	// Wedged workflow task: the workflow task is dispatched and fails over
	// and over (attempt count climbing) — every signal lands in a black hole
	// and the workflow can never make progress. Debounced like the stuck
	// path (pollers active + consecutive passes + wall-clock window).
	// Recovery for this class deliberately does NOT reset: for the dominant
	// cause (non-deterministic replay after a code update) replay re-diverges
	// no matter where we reset to. Instead: terminate with a clear reason,
	// mark failed in the DB (which routes the next user message into
	// resume-at-position), and tell the user how to continue.
	//
	// PAUSED workflows are included: a paused execution whose replay wedges
	// (deploy changes determinism while it is parked) can never process its
	// resume signal — without this it retries the workflow task forever and
	// the chat is permanently unrecoverable. A healthy paused workflow has NO
	// pending workflow task, so it can never trip this detector.
	if temporalState.HasWedgedWorkflowTask &&
		(wf.Status == db.WorkflowStatusRunning || wf.Status == db.WorkflowStatusPaused) {
		if !r.observeTask(ctx, wf, wedgeObservationTaskType, "", pollers) {
			// Not yet confirmed (pollers absent, or debounce still counting).
			return result
		}

		logging.Error("[Reconciler] Workflow task is failing repeatedly (wedged) - terminating and marking as failed",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"attempt", temporalState.WedgedTaskAttempt,
		)
		r.recordAnomaly(stats, anomalyWedgeTerminated)

		terminateReason := fmt.Sprintf(
			"Workflow wedged: workflow task failing repeatedly (attempt %d) - likely non-deterministic replay after a code update; reset would re-diverge",
			temporalState.WedgedTaskAttempt,
		)
		if err := r.tempClient.TerminateWorkflow(ctx, wf.ID, "", terminateReason); err != nil {
			logging.Warn("[Reconciler] Failed to terminate wedged workflow in Temporal",
				"error", err,
				"workflowID", wf.ID,
			)
			// Continue anyway - we still want to mark it failed in DB
		}
		r.clearStuckObservation(wf.ID)

		// Mark failed (CAS prevents duplicate transitions). Failed + kept
		// position checkpoint = the next user message resumes at position.
		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.WorkflowStatusFailed, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to mark wedged workflow as failed: %w", err)
			return result
		}
		if !swapped {
			return result // another reconciler already handled this
		}

		// Tell the user what happened and how to continue. Accurate because
		// SendMessage starts the next run in resume-at-position mode for
		// failed/terminated predecessors.
		if _, err := r.repo.SaveMessageToThread(ctx, wf.ChatID, wf.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), wedgeInterruptedChatMessage, &wf.ID, nil, nil); err != nil {
			logging.Warn("[Reconciler] Failed to add wedge interruption message to chat",
				"error", err,
				"workflowID", wf.ID,
			)
		}

		result.WasStale = true
		result.TemporalStatus = db.WorkflowStatusFailed

		return result
	}

	// Check for stuck task (workflow task or activity). A task sitting in
	// Scheduled state usually means the worker is down or rebuilding — in
	// that case Temporal re-dispatches it as soon as a worker returns, and
	// the reconciler must do nothing. Only a task that stays Scheduled
	// while the task queue has active pollers, across the debounce window,
	// is treated as lost and recovered.
	if temporalState.HasStuckTask && wf.Status == db.WorkflowStatusRunning {
		if !r.observeTask(ctx, wf, temporalState.StuckTaskType, temporalState.StuckActivityID, pollers) {
			// Not yet confirmed (pollers absent, or debounce still counting).
			return result
		}

		// Confirmed: the task stayed Scheduled across the debounce window
		// with active pollers — it was lost (e.g. worker died mid-dispatch
		// and the queue entry is gone). Recover by resetting the workflow
		// to just before the lost task was scheduled: Temporal replays and
		// re-issues it, and the workflow simply continues.
		//
		// Bounded guard: a workflow that keeps re-sticking at the same point
		// without forward progress (a deterministic problem reset cannot fix)
		// must not be reset forever. Once the guard gives up, skip the reset and
		// go straight to terminate + mark failed (routing the next user message
		// to the coarse restart, which runs current code with no old history).
		resetSkippedByGuard := !r.resetGuard.Allow(wf.ID, temporalState.HistoryLength)
		if !resetSkippedByGuard {
			if err := r.recoverStuckWorkflowByReset(ctx, wf, temporalState); err == nil {
				r.resetGuard.Record(wf.ID, temporalState.HistoryLength)
				r.clearStuckObservation(wf.ID)
				r.recordAnomaly(stats, anomalyStuckReset)
				result.RecoveredByReset = true
				return result
			} else { //nolint:revive // keep err scoped to the recovery attempt
				logging.Warn("[Reconciler] Workflow reset failed - falling back to terminate",
					"error", err,
					"workflowID", wf.ID,
					"chatID", wf.ChatID,
					"stuckTaskType", temporalState.StuckTaskType,
					"stuckActivityID", temporalState.StuckActivityID,
				)
			}
		}

		terminateDetail := "reset recovery failed"
		if resetSkippedByGuard {
			terminateDetail = "reset-attempt guard exhausted (repeated resets made no progress)"
			logging.Error("[Reconciler] Reset-attempt guard exhausted for stuck workflow - terminating and marking as failed",
				"workflowID", wf.ID,
				"chatID", wf.ChatID,
				"stuckTaskType", temporalState.StuckTaskType,
				"resetAttempts", r.resetGuard.Attempts(wf.ID),
			)
			r.recordAnomaly(stats, anomalyResetAttemptsExhausted)
		} else {
			logging.Error("[Reconciler] Workflow is stuck and reset failed - terminating and marking as failed",
				"workflowID", wf.ID,
				"chatID", wf.ChatID,
				"stuckTaskType", temporalState.StuckTaskType,
				"stuckActivityID", temporalState.StuckActivityID,
				"stuckActivityType", temporalState.StuckActivityType,
				"stuckDuration", temporalState.StuckDuration,
			)
			r.recordAnomaly(stats, anomalyResetFailedTerminated)
		}
		r.resetGuard.Clear(wf.ID)

		// Terminate the stuck workflow in Temporal so it's no longer "running"
		// This prevents any confusion where DB says failed but Temporal says running
		terminateReason := fmt.Sprintf("Workflow stuck: %s task in Scheduled state for %v (%s)", temporalState.StuckTaskType, temporalState.StuckDuration, terminateDetail)
		if err := r.tempClient.TerminateWorkflow(ctx, wf.ID, "", terminateReason); err != nil {
			logging.Warn("[Reconciler] Failed to terminate stuck workflow in Temporal",
				"error", err,
				"workflowID", wf.ID,
			)
			// Continue anyway - we still want to mark it failed in DB
		}
		r.clearStuckObservation(wf.ID)

		// Mark workflow as failed in DB (CAS prevents duplicate transitions)
		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.WorkflowStatusFailed, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to mark workflow as failed: %w", err)
			return result
		}
		if !swapped {
			return result // another reconciler already handled this
		}

		// Add error message to chat so user knows what happened
		// Only runs if CAS succeeded, preventing duplicate messages
		if err := r.addWorkflowErrorMessage(ctx, wf, temporalState); err != nil {
			logging.Warn("[Reconciler] Failed to add error message to chat",
				"error", err,
				"workflowID", wf.ID,
			)
		}

		result.WasStale = true
		result.TemporalStatus = db.WorkflowStatusFailed

		return result
	}

	// Progress watchdog: the cause-agnostic tripwire. The workflow is RUNNING
	// with ZERO pending work in Temporal — the shape of a silent wedge from an
	// UNKNOWN cause (every enumerated detector above requires a pending task
	// to look at). Track HistoryLength across passes; a frozen history across
	// the detection thresholds is reported (ERROR + metric, no action), and
	// only after the confirmation thresholds (double detection) is the
	// workflow terminated, marked failed (checkpoint preserved — SendMessage
	// resumes at position), and the user told how to continue.
	if suspicious {
		stage, passes, elapsed := r.observeProgress(ctx, wf, temporalState)
		switch stage {
		case progressStageDetected:
			// Report only. The cause is unknown — it could still be a wait
			// class this reconciler doesn't know about — so give it a full
			// confirmation window before doing anything destructive.
			logging.Error("[Reconciler] Progress stall detected: running workflow has no pending work and no history growth",
				"workflowID", wf.ID,
				"chatID", wf.ChatID,
				"historyLength", temporalState.HistoryLength,
				"passes", passes,
				"elapsed", elapsed,
				"detectPasses", r.progressStallPasses,
				"detectWindow", r.progressStallWindow,
			)
			r.recordAnomaly(stats, anomalyProgressStallDetected)

		case progressStageConfirmed:
			// Destructive action is additionally gated on live workflow
			// pollers: with the worker fleet down/rebuilding the whole system
			// is stalled for a KNOWN reason, and terminating would be wrong.
			// Unlike the stuck-task debounce, poller absence does NOT reset
			// the streak — a quiescent workflow needs no worker to make
			// progress, so the evidence stays valid; we just hold the action.
			active, pollErr := r.pollersActive(ctx, pollers, "workflow")
			if pollErr != nil || !active {
				return result
			}

			logging.Error("[Reconciler] Progress stall confirmed - terminating and marking as failed for resume",
				"workflowID", wf.ID,
				"chatID", wf.ChatID,
				"historyLength", temporalState.HistoryLength,
				"passes", passes,
				"elapsed", elapsed,
			)
			r.recordAnomaly(stats, anomalyProgressStallConfirmed)

			terminateReason := fmt.Sprintf(
				"Workflow stalled: no pending tasks and history length %d unchanged for %v across %d reconcile passes (cause unknown)",
				temporalState.HistoryLength, elapsed.Round(time.Second), passes,
			)
			if err := r.tempClient.TerminateWorkflow(ctx, wf.ID, "", terminateReason); err != nil {
				logging.Warn("[Reconciler] Failed to terminate stalled workflow in Temporal",
					"error", err,
					"workflowID", wf.ID,
				)
				// Continue anyway - we still want to mark it failed in DB
			}
			r.clearProgressObservation(wf.ID)

			// Mark failed (CAS prevents duplicate transitions). Failed + kept
			// position checkpoint = the next user message resumes at position.
			swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.WorkflowStatusFailed, wf.Status)
			if err != nil {
				result.Error = fmt.Errorf("failed to mark stalled workflow as failed: %w", err)
				return result
			}
			if !swapped {
				return result // another reconciler already handled this
			}

			// Tell the user what happened and how to continue. Accurate
			// because SendMessage starts the next run in resume-at-position
			// mode for failed/terminated predecessors.
			if _, err := r.repo.SaveMessageToThread(ctx, wf.ChatID, wf.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), progressStallChatMessage, &wf.ID, nil, nil); err != nil {
				logging.Warn("[Reconciler] Failed to add progress-stall message to chat",
					"error", err,
					"workflowID", wf.ID,
				)
			}

			result.WasStale = true
			result.ProgressStalled = true
			result.TemporalStatus = db.WorkflowStatusFailed

			return result
		}
	}

	// Temporal has the workflow - check for status mismatch
	// Special case: DB says paused, Temporal says running = intentional pause (keep paused)
	if wf.Status == db.WorkflowStatusPaused && temporalState.Status == db.WorkflowStatusRunning {
		// Intentional pause - don't override
		return result
	}

	// Special case: DB says paused, but Temporal says completed/failed/cancelled
	// The Temporal execution ended while paused — repair DB status.
	if wf.Status == db.WorkflowStatusPaused && !temporalState.IsRunning {
		logging.Warn("[Reconciler] Paused workflow's Temporal execution ended, repairing",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"dbStatus", wf.Status,
			"temporalStatus", temporalState.Status,
		)

		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, temporalState.Status, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to repair paused workflow status: %w", err)
			return result
		}
		if !swapped {
			return result // another reconciler already handled this
		}

		result.TemporalStatus = temporalState.Status
		result.WasStale = true

		return result
	}

	// For other mismatches, repair DB to match Temporal (source of truth).
	if wf.Status != temporalState.Status {
		logging.Warn("[Reconciler] Status mismatch detected, repairing",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"dbStatus", wf.Status,
			"temporalStatus", temporalState.Status,
		)

		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, temporalState.Status, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to repair workflow status: %w", err)
			return result
		}
		if swapped {
			result.WasStale = true
			// Drift-repaired to a terminal Completed here (not via the
			// WorkflowStatus activity) — transition the chat to any target.
			if temporalState.Status == db.WorkflowStatusCompleted {
				r.transitionChatOnCompletion(ctx, wf)
			}
		}
	}

	return result
}

// transitionChatOnCompletion switches the chat to the completed ROOT workflow's
// declared `transition_to` target. This covers the rare paths where the
// reconciler — not the authoritative WorkflowStatus completion activity — is what
// marks a run Completed (Temporal lost the workflow, or a terminal-status drift
// repair). Best-effort and idempotent: a normal completion already transitioned
// via the activity, so this is a no-op then. reconcileWorkflow returns early for
// child workflows (wf.ParentID != nil), so every caller here is a root workflow.
func (r *Reconciler) transitionChatOnCompletion(ctx context.Context, wf *db.Workflow) {
	to, err := handlers.TransitionChatOnCompletion(ctx, r.repo, wf.ChatID, wf.WorkflowName)
	if err != nil {
		logging.Warn("[Reconciler] Failed to transition chat on completion",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"error", err,
		)
		return
	}
	if to != "" {
		handlers.EmitTransitionMessage(ctx, r.repo, wf.ChatID, wf.Thread, wf.ID, to)
	}
}

// observeTask records one problem-task observation (stuck-in-Scheduled or
// wedged workflow task) and reports whether it is now CONFIRMED (eligible for
// recovery). Confirmation requires:
//   - active pollers on the task queue for the task's type THIS pass
//     (no pollers = worker down/rebuilding = Temporal will re-dispatch), and
//   - at least stuckConfirmationPasses consecutive poller-active passes
//     observing the SAME task, and
//   - at least stuckConfirmationWindow elapsed since first observation.
//
// The tracking entry resets whenever the task is no longer problematic, the
// task identity changes, or pollers were absent (or unknown) for a pass.
func (r *Reconciler) observeTask(ctx context.Context, wf *db.Workflow, taskType, activityID string, pollers *pollerState) bool {
	active, err := r.pollersActive(ctx, pollers, taskType)
	if err != nil || !active {
		// Worker down/rebuilding, or liveness unknown: not evidence of a
		// lost task. Reset the debounce so only uninterrupted poller-active
		// observations count. (The skip itself is logged once per pass by
		// pollersActive, not per workflow.)
		r.clearStuckObservation(wf.ID)
		return false
	}

	r.stuckMu.Lock()
	defer r.stuckMu.Unlock()

	obs := r.stuckObservations[wf.ID]
	if obs == nil || obs.taskType != taskType || obs.activityID != activityID {
		// New task (or the task changed identity, meaning the previous one
		// made progress) — start a fresh confirmation window.
		obs = &stuckObservation{
			taskType:      taskType,
			activityID:    activityID,
			firstObserved: time.Now(),
		}
		r.stuckObservations[wf.ID] = obs
	}

	obs.passes++
	return obs.passes >= r.stuckConfirmationPasses && time.Since(obs.firstObserved) >= r.stuckConfirmationWindow
}

// clearStuckObservation drops the debounce entry for a workflow.
func (r *Reconciler) clearStuckObservation(workflowID string) {
	r.stuckMu.Lock()
	defer r.stuckMu.Unlock()
	delete(r.stuckObservations, workflowID)
}

// pruneStuckObservations drops debounce entries for workflows that are no
// longer in the running set (completed/cancelled between passes), so the
// in-memory map cannot grow unboundedly.
func (r *Reconciler) pruneStuckObservations(running map[string]bool) {
	r.stuckMu.Lock()
	defer r.stuckMu.Unlock()
	for id := range r.stuckObservations {
		if !running[id] {
			delete(r.stuckObservations, id)
		}
	}
}

// progressStage is the progress watchdog's verdict for one quiescent pass.
type progressStage int

const (
	// progressStageNone: below thresholds, already reported, or excluded as a
	// legitimate user-input wait — nothing to do this pass.
	progressStageNone progressStage = iota
	// progressStageDetected: the detection thresholds were just crossed —
	// report (ERROR + metric) but take no action. Returned exactly once per
	// streak.
	progressStageDetected
	// progressStageConfirmed: the confirmation thresholds (double detection)
	// have passed — the caller may act.
	progressStageConfirmed
)

// observeProgress records one quiescent-pass observation for the progress
// watchdog and returns the stage reached, plus the streak's pass count and
// wall-clock span for logging.
//
// A streak is anchored to a HistoryLength value: any growth (or shrink, e.g.
// continue-as-new/reset) starts a fresh streak, because history movement IS
// progress. Detection requires progressStallPasses consecutive quiescent
// passes spanning progressStallWindow; confirmation requires double both.
//
// Discriminator for legitimate signal-parked waits: a workflow blocked on a
// signal (ask_question / ask_user on "signal.question.<id>", tool approval on
// "signal.approval.<id>", pause on "signal.resume") has the EXACT same
// Temporal footprint as a silent wedge — RUNNING, zero pending work, frozen
// history. DescribeWorkflowExecution cannot tell them apart (blocked signal
// receives and unfired timers are invisible in the pending census). What
// distinguishes them is the durable DB wait marker each park point writes
// BEFORE parking:
//   - pause (user pause, retry-exhaustion, daemon-offline breaker) marks the
//     workflow status paused CHAT-WIDE (root row included), so it never
//     enters the running reconcile set; a paused row under a still-running
//     root (a pause writer violating that invariant) is caught by
//     awaitingUserInput as a last line of defense;
//   - ask_question/ask_user writes a pending questions row (status=1) via the
//     QuestionCreate activity, keyed by chat;
//   - tool approvals write a pending approvals row via ApprovalCreate.
//
// The DB checks run only when a threshold is being crossed (at most once per
// detection window per workflow), not on every pass. If the check errors the
// streak is frozen as-is — never report or act on unknown exclusion state.
func (r *Reconciler) observeProgress(ctx context.Context, wf *db.Workflow, state *TemporalWorkflowState) (stage progressStage, passes int, elapsed time.Duration) {
	r.progressMu.Lock()
	obs := r.progressObservations[wf.ID]
	if obs == nil || obs.historyLength != state.HistoryLength {
		// First observation, or history moved (= progress): fresh streak.
		obs = &progressObservation{
			historyLength: state.HistoryLength,
			firstObserved: time.Now(),
		}
		r.progressObservations[wf.ID] = obs
	}
	obs.passes++
	passes = obs.passes
	elapsed = time.Since(obs.firstObserved)
	crossingDetect := !obs.detected &&
		passes >= r.progressStallPasses &&
		elapsed >= r.progressStallWindow
	confirmed := passes >= progressStallConfirmMultiplier*r.progressStallPasses &&
		elapsed >= time.Duration(progressStallConfirmMultiplier)*r.progressStallWindow
	r.progressMu.Unlock()

	if !crossingDetect && !confirmed {
		return progressStageNone, passes, elapsed
	}

	// Threshold crossing: check the legitimate-wait exclusions before
	// reporting or acting.
	waiting, err := r.awaitingUserInput(ctx, wf)
	if err != nil {
		logging.Warn("[Reconciler] Progress watchdog could not check user-input waits - holding",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"error", err,
		)
		return progressStageNone, passes, elapsed
	}
	if waiting {
		// Signal-parked on user input (question/approval): not a stall. Clear
		// the streak so a resolved wait starts fresh accounting.
		r.clearProgressObservation(wf.ID)
		return progressStageNone, passes, elapsed
	}

	if confirmed {
		return progressStageConfirmed, passes, elapsed
	}

	// Mark the detection as reported so the ERROR/metric fire once per streak.
	r.progressMu.Lock()
	if cur := r.progressObservations[wf.ID]; cur != nil {
		cur.detected = true
	}
	r.progressMu.Unlock()
	return progressStageDetected, passes, elapsed
}

// awaitingUserInput reports whether the workflow's chat has a durable
// user-input wait marker: a pending question (ask_question / ask_user), a
// pending tool approval, or a paused workflow row. These are the
// signal-parked waits whose Temporal footprint is indistinguishable from a
// silent stall.
//
// The paused-row check is defense in depth: a self-pause propagates paused
// status chat-wide (root row included), which keeps the workflow out of the
// stall watchdog via the wf.Status gate. A paused NESTED row under a running
// root therefore means some pause writer violated that invariant — treat the
// chat as legitimately parked (never terminate a workflow that is parked
// waiting for signal.resume), but log it as an ERROR so the violation is
// visible instead of silently masking the watchdog.
func (r *Reconciler) awaitingUserInput(ctx context.Context, wf *db.Workflow) (bool, error) {
	question, err := r.repo.GetPendingQuestionByChatID(ctx, wf.ChatID)
	if err != nil {
		return false, fmt.Errorf("checking pending question: %w", err)
	}
	if question != nil {
		return true, nil
	}
	approvals, err := r.repo.ListPendingApprovalsByChat(ctx, wf.ChatID)
	if err != nil {
		return false, fmt.Errorf("checking pending approvals: %w", err)
	}
	if len(approvals) > 0 {
		return true, nil
	}
	chatWorkflows, err := r.repo.ListWorkflowsByChat(ctx, wf.ChatID)
	if err != nil {
		return false, fmt.Errorf("checking paused chat workflows: %w", err)
	}
	var pausedIDs []string
	for _, cw := range chatWorkflows {
		if cw.Status == db.WorkflowStatusPaused {
			pausedIDs = append(pausedIDs, cw.ID)
		}
	}
	if len(pausedIDs) > 0 {
		logging.Error("[Reconciler] Progress watchdog: running root workflow has paused descendant rows - treating as pause-parked, not a stall (pause writer failed to mark the root row)",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"pausedWorkflowIDs", pausedIDs,
		)
		return true, nil
	}
	return false, nil
}

// clearProgressObservation drops the watchdog streak for a workflow.
func (r *Reconciler) clearProgressObservation(workflowID string) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	delete(r.progressObservations, workflowID)
}

// pruneProgressObservations drops watchdog streaks for workflows that are no
// longer in the running set, so the in-memory map cannot grow unboundedly.
func (r *Reconciler) pruneProgressObservations(running map[string]bool) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	for id := range r.progressObservations {
		if !running[id] {
			delete(r.progressObservations, id)
		}
	}
}

// recoverStuckWorkflowByReset attempts to recover a workflow whose task was
// lost by resetting it to the last workflow task completed before the stuck
// task was scheduled. Temporal truncates history there, replays, and
// re-issues the lost task; completed activities before the reset point keep
// their recorded results and signals after it are re-applied, so the
// workflow simply continues.
func (r *Reconciler) recoverStuckWorkflowByReset(ctx context.Context, wf *db.Workflow, state *TemporalWorkflowState) error {
	resetEventID, err := r.findResetPoint(ctx, wf.ID, state)
	if err != nil {
		return fmt.Errorf("finding reset point: %w", err)
	}

	resp, err := r.tempClient.ResetWorkflowExecution(ctx, &workflowservice.ResetWorkflowExecutionRequest{
		Namespace: r.namespace,
		WorkflowExecution: &commonpb.WorkflowExecution{
			WorkflowId: wf.ID,
			RunId:      state.RunID,
		},
		Reason:                    "reconciler: recovering lost task",
		WorkflowTaskFinishEventId: resetEventID,
	})
	if err != nil {
		return fmt.Errorf("resetting workflow execution: %w", err)
	}

	// ERROR (Sentry-visible) even though recovery succeeded: a task was LOST
	// from Temporal's queue, which should never happen — the repair working is
	// not a reason to hide the anomaly.
	logging.Error("[Reconciler] Recovered stuck workflow via reset (task was lost from queue)",
		"workflowID", wf.ID,
		"chatID", wf.ChatID,
		"stuckTaskType", state.StuckTaskType,
		"stuckActivityID", state.StuckActivityID,
		"stuckActivityType", state.StuckActivityType,
		"stuckDuration", state.StuckDuration,
		"resetEventID", resetEventID,
		"newRunID", resp.GetRunId(),
	)
	return nil
}

// findResetPoint walks the workflow history and returns the event ID of the
// WorkflowTaskCompleted event to reset to:
//   - stuck activity: the last WorkflowTaskCompleted BEFORE the stuck
//     activity's ActivityTaskScheduled event (replaying that workflow task
//     re-issues the schedule command);
//   - stuck workflow task: the last WorkflowTaskCompleted in history
//     (resetting there issues a fresh workflow task).
func (r *Reconciler) findResetPoint(ctx context.Context, workflowID string, state *TemporalWorkflowState) (int64, error) {
	iter := r.tempClient.GetWorkflowHistory(ctx, workflowID, state.RunID, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)

	var lastWorkflowTaskCompleted int64
	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return 0, fmt.Errorf("reading workflow history: %w", err)
		}
		switch event.GetEventType() {
		case enums.EVENT_TYPE_WORKFLOW_TASK_COMPLETED:
			lastWorkflowTaskCompleted = event.GetEventId()
		case enums.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			if state.StuckTaskType == "activity" &&
				event.GetActivityTaskScheduledEventAttributes().GetActivityId() == state.StuckActivityID {
				if lastWorkflowTaskCompleted == 0 {
					return 0, fmt.Errorf("no WorkflowTaskCompleted event before stuck activity %s", state.StuckActivityID)
				}
				return lastWorkflowTaskCompleted, nil
			}
		}
	}

	// Stuck workflow task, or the activity's scheduled event was not found
	// (history may have advanced): fall back to the last workflow task
	// completed in history.
	if lastWorkflowTaskCompleted == 0 {
		return 0, fmt.Errorf("no WorkflowTaskCompleted event found in history of workflow %s", workflowID)
	}
	return lastWorkflowTaskCompleted, nil
}

// reapOrphanedDescendants ends every running/paused workflow whose parent is
// already terminal — at the terminal ancestor's own status, so a repaired
// cancel does not read as a repaired success — and returns how many rows it
// moved.
//
// It is the one repair the per-workflow loop structurally cannot make.
// reconcileWorkflow returns immediately for any workflow with a parent_id,
// because a child's lifecycle belongs to its parent — a premise that holds only
// while the parent's terminal transition cascades to it. A TerminateWorkflow
// breaks that premise: it is a hard kill, so the workflow's completion handler
// never runs and the cascade its terminal-status activity would have performed
// never happens. Every terminate path has this
// shape — CancelChat's user cancel, and this reconciler's own wedge, stuck-task
// and progress-stall terminations. The subtree is then stranded at running (2)
// or paused (6) with nothing on any code path that will ever revisit it, and
// `workflow ps` (which filters on status alone) keeps reporting the dead rows
// as live runs next to real ones.
//
// Every caller still cascades for itself. This is the backstop that keeps one
// forgotten call site from being permanent, and it needs no Temporal lookup: a
// child of a terminal parent is dead by exactly the rule
// CascadeTerminalStatusToDescendants already enforces on the write path, read
// in the other direction.
//
// Runs BEFORE the pass lists workflows, so a row known dead is never
// adjudicated as a live one.
func (r *Reconciler) reapOrphanedDescendants(ctx context.Context, stats *passStats) (int, error) {
	reaped, err := r.repo.ReapOrphanedWorkflowDescendants(ctx)
	if err != nil {
		logging.Error("[Reconciler] Failed to reap orphaned workflow descendants", "error", err)
		return 0, fmt.Errorf("failed to reap orphaned workflow descendants: %w", err)
	}
	if reaped == 0 {
		return 0, nil
	}
	for i := int64(0); i < reaped; i++ {
		r.recordAnomaly(stats, anomalyOrphanDescendantReaped)
	}
	logging.Error("[Reconciler] Reaped orphaned workflow descendants — a terminal parent did not cascade",
		"rows", reaped,
	)
	return int(reaped), nil
}

// repairStrandedSpawnToolCalls closes spawn tool calls whose child workflow has
// reached a terminal status but which never received a result, and returns how
// many it repaired.
//
// executeSpawnInline writes the child's terminal workflow status and the
// parent's tool-call result as two separate activities. A worker that dies
// between them leaves the child correctly recorded as finished while the
// parent's call stays at pending/executing forever, so the parent blocks on a
// sub-agent that is already over and the sub-agent's work is silently dropped.
//
// Nothing else revisits these rows. Cleanup makes exactly this repair — and
// callIsStillLive already consults exactly this evidence — but it only runs
// when a workflow reaches an abnormal terminal path, and it is scoped to the
// ENDING workflow's own thread. A stranded spawn call belongs to the PARENT's
// thread, and the parent is typically still alive, so the row sits outside
// every scope Cleanup can reach. Observed on real data: a child failed at
// 22:16:54 (a worker restart appears in the logs at 22:08) and the parent's
// spawn call was still "executing" 49 hours later.
//
// Deliberately mirrors reapOrphanedDescendants: same backstop role, same
// durable evidence, same fail-closed direction. The query never returns a call
// whose child is still running or paused, because fabricating a failure for a
// live spawn writes a lie into conversation history that the model then reads
// as fact and no later pass can distinguish from a real result. A missing
// result is recoverable; an invented one is not.
//
// The synthesized result is the same InterruptedToolResultContent every other
// repair path writes, so a spawn repaired here is indistinguishable from one
// repaired by Cleanup — including its instruction to verify effects before
// re-running.
func (r *Reconciler) repairStrandedSpawnToolCalls(ctx context.Context, stats *passStats) (int, error) {
	stranded, err := r.repo.ListStrandedSpawnToolCalls(ctx)
	if err != nil {
		logging.Error("[Reconciler] Failed to list stranded spawn tool calls", "error", err)
		return 0, fmt.Errorf("failed to list stranded spawn tool calls: %w", err)
	}
	if len(stranded) == 0 {
		return 0, nil
	}

	repaired := 0
	for _, call := range stranded {
		now := time.Now().UTC()
		if err := r.repo.UpsertToolCallResult(ctx, &db.ToolCallResult{
			ToolCallID: call.ID,
			Content:    handlers.InterruptedToolResultContent,
			IsError:    true,
			CreatedAt:  now,
			UpdatedAt:  now,
		}); err != nil {
			logging.Error("[Reconciler] Failed to write stranded spawn result",
				"toolCallID", call.ID, "childWorkflowID", call.ChildWorkflowID, "error", err)
			continue
		}

		// Status after the result: a call carrying a result but still marked
		// executing is the same stuck spinner in a different column, whereas a
		// terminal status with no result is the state that stranded it here.
		if err := db.UpsertToolCallStatus(ctx, r.repo, &db.ToolCall{
			ID:          call.ID,
			ChatID:      call.ChatID,
			ToolName:    call.ToolName,
			Status:      core.ToolCallStatusFailed,
			CompletedAt: &now,
		}); err != nil {
			logging.Error("[Reconciler] Failed to close stranded spawn tool call",
				"toolCallID", call.ID, "error", err)
			continue
		}

		repaired++
		r.recordAnomaly(stats, anomalyStrandedSpawnRepaired)
	}

	if repaired > 0 {
		logging.Error("[Reconciler] Repaired stranded spawn tool calls — a terminal child never reported back to its parent",
			"rows", repaired,
		)
	}
	return repaired, nil
}

// mailboxKindForTerminalWorkflowStatus maps a terminal workflows.status onto
// the mailbox's AgentMessageKind, mirroring the live path's
// spawnResultKindForMailbox (workflow.go) for the cases a REAL detached spawn
// goroutine can itself observe. Expired has no live-path equivalent — it is
// a reconciler-only outcome (a workflow the progress watchdog or stuck-task
// recovery gave up on) — so it maps to Failed, the closest honest label the
// mailbox vocabulary has; the message body says "expired" explicitly so the
// distinction is not lost.
func mailboxKindForTerminalWorkflowStatus(status core.WorkflowStatus) core.AgentMessageKind {
	switch status {
	case core.WorkflowStatusCancelled:
		return core.AgentMessageKindCancelled
	case core.WorkflowStatusCompleted:
		return core.AgentMessageKindCompletion
	default:
		// Failed, Expired, or any other terminal value this repair's own
		// query would not otherwise return.
		return core.AgentMessageKindFailed
	}
}

// repairStrandedBackgroundSpawns is repairStrandedSpawnToolCalls's async
// counterpart (spec: async-spawn-and-agent-messaging.md, §7.1): closes the
// gap where a background=true spawn's child workflow reached a terminal
// status but the detached goroutine that runs it
// (dispatchSpawnBackground/workflow.go) never got to enqueue — or failed to
// enqueue — the completion report into the parent's mailbox. Nothing else
// ever revisits these: the goroutine's own enqueue is fire-and-forget by
// design (a failed EnqueueAgentMessage activity call is logged, not
// retried past its own backoff), and the sync repair above cannot see a
// backgrounded call at all (see ListStrandedBackgroundSpawnToolCalls's
// comment for why).
//
// Idempotent and safe under concurrency: the insert goes through
// EnqueueAgentMessageIfAbsent, which is backed by
// idx_agent_messages_one_terminal_report_per_spawn (a real DB constraint,
// not a check-then-insert in this code) — see the migration and query
// comments for the full reasoning. inserted=false here is the everyday
// "someone already reported this" outcome, not a failure, so it is neither
// logged as an error nor counted as an anomaly; only rows this pass itself
// newly closed count.
//
// Fail-closed by construction: ListStrandedBackgroundSpawnToolCalls's own
// WHERE clause excludes a child still running (2) or paused (6), so this
// function is never even offered one to fabricate a result for.
func (r *Reconciler) repairStrandedBackgroundSpawns(ctx context.Context, stats *passStats) (int, error) {
	stranded, err := r.repo.ListStrandedBackgroundSpawnToolCalls(ctx)
	if err != nil {
		logging.Error("[Reconciler] Failed to list stranded background spawn tool calls", "error", err)
		return 0, fmt.Errorf("failed to list stranded background spawn tool calls: %w", err)
	}
	if len(stranded) == 0 {
		return 0, nil
	}

	repaired := 0
	for _, call := range stranded {
		if call.ParentThreadID == nil || *call.ParentThreadID == "" {
			// tool_calls.thread_id is nilable in general (a call can be
			// recorded before its message is finalized), but a spawn old
			// enough to have a terminal child always has it in practice.
			// Nothing to deliver into without a recipient thread — skip
			// rather than guess, and let a later pass pick it up once the
			// thread_id lands.
			logging.Warn("[Reconciler] Stranded background spawn has no parent thread id, skipping",
				"toolCallID", call.ToolCallID, "childThreadID", call.ChildThreadID)
			continue
		}

		kind := mailboxKindForTerminalWorkflowStatus(call.WorkflowStatus)
		body := fmt.Sprintf(
			"Sub-agent finished while its result was lost in transit (worker interruption). "+
				"Use spawn_status(agent_id=%q) to see what it produced.",
			call.ChildThreadID,
		)
		if call.WorkflowStatus == reliantv1.ChatWorkflowStatus_CHAT_WORKFLOW_STATUS_EXPIRED {
			body = fmt.Sprintf(
				"Sub-agent's run expired before it could report back. "+
					"Use spawn_status(agent_id=%q) to see what it produced before it stopped.",
				call.ChildThreadID,
			)
		}

		toolCallID := call.ToolCallID
		inserted, err := r.repo.EnqueueAgentMessageIfAbsent(ctx, &db.AgentMessage{
			ID:           uuid.New().String(),
			ChatID:       call.ChatID,
			FromThreadID: call.ChildThreadID,
			ToThreadID:   *call.ParentThreadID,
			Kind:         kind,
			Body:         body,
			ToolCallID:   &toolCallID,
			Status:       core.AgentMessageStatusQueued,
			CreatedAt:    time.Now().UTC(),
		})
		if err != nil {
			logging.Error("[Reconciler] Failed to enqueue stranded background spawn completion",
				"toolCallID", call.ToolCallID, "childThreadID", call.ChildThreadID, "error", err)
			continue
		}
		if !inserted {
			// A terminal report already exists for this call — either the
			// detached goroutine's own enqueue landed after all, or a
			// concurrent reconciler pass won the race. Correct, not stale.
			continue
		}

		repaired++
		r.recordAnomaly(stats, anomalyStrandedBackgroundSpawnRepaired)
	}

	if repaired > 0 {
		logging.Error("[Reconciler] Repaired stranded background spawn tool calls — a terminal child never reported back to its parent's mailbox",
			"rows", repaired,
		)
	}
	return repaired, nil
}

// resolveOrphanedAgentMessages marks mailbox rows undelivered when the thread
// they were queued for has already exited, and returns how many rows it moved.
//
// A message is delivered only by drainAgentMessagesAtBoundary, at an agent
// loop-step boundary. A human (SendAgentMessage) or peer agent (spawn_send)
// can queue into a thread that is genuinely running and whose loop then exits
// before reaching the next boundary — an inherent race that no enqueue-time
// liveness check can close, because the thread really was live at enqueue
// time. The live path now resolves the mailbox as the thread goes terminal
// (ThreadStatusActivity.resolveMailbox); this is the backstop for the rows
// that predate it and for the case where the process dies between writing the
// thread's terminal status and resolving its mailbox.
//
// Nothing else revisits these rows. The drain only runs for a thread taking a
// step, and a terminal thread takes none — so a stranded row is not merely
// late, it is permanently unreachable. Observed on real data: two human
// messages queued at 00:06:31 and 00:06:51 into a thread that completed at
// 00:06:56, still queued with delivered_at NULL, with the user told both
// would be read at the agent's next turn.
//
// Deliberately mirrors repairStrandedSpawnToolCalls and
// repairStrandedBackgroundSpawns: same backstop role, same durable evidence,
// same fail-closed direction. ListThreadsWithOrphanedAgentMessages never
// returns a thread that is still live, and the UPDATE itself matches only
// status = 1, so a message the drain is about to deliver cannot be
// relabelled undelivered by a pass that raced it. Marking a live thread's
// queue would destroy a message that was about to arrive — strictly worse
// and unrecoverable, where a missed orphan is simply caught next pass.
//
// Rows are marked, never deleted: the row is the only surviving record that
// the user said something, and it is what lets the UI report "never
// delivered" honestly instead of showing a promise it cannot keep.
func (r *Reconciler) resolveOrphanedAgentMessages(ctx context.Context, stats *passStats) (int, error) {
	threadIDs, err := r.repo.ListThreadsWithOrphanedAgentMessages(ctx)
	if err != nil {
		logging.Error("[Reconciler] Failed to list threads with orphaned agent messages", "error", err)
		return 0, fmt.Errorf("failed to list threads with orphaned agent messages: %w", err)
	}
	if len(threadIDs) == 0 {
		return 0, nil
	}

	resolved := 0
	for _, threadID := range threadIDs {
		rows, err := r.repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, threadID)
		if err != nil {
			logging.Error("[Reconciler] Failed to resolve orphaned agent messages",
				"threadID", threadID, "error", err)
			continue
		}
		if rows == 0 {
			// The live path (or a concurrent pass) got there first between
			// the list and this update. Correct, not stale.
			continue
		}
		resolved += int(rows)
		for i := int64(0); i < rows; i++ {
			r.recordAnomaly(stats, anomalyOrphanedAgentMessagesResolved)
		}
	}

	if resolved > 0 {
		logging.Error("[Reconciler] Resolved orphaned agent messages — queued for a thread whose loop had already exited",
			"rows", resolved,
			"threads", len(threadIDs),
		)
	}
	return resolved, nil
}

// ReconcileRunningWorkflows reconciles all workflows with status running OR
// paused. This is the main entry point for background reconciliation.
//
// Paused workflows MUST be in the pass: reconcileWorkflow's paused-specific
// repairs (paused row whose Temporal execution ended → repair to terminal;
// paused execution whose replay is wedged → terminate + mark failed) are
// unreachable otherwise — a paused zombie then burns workflow-task retries
// forever with no path back to a usable chat.
// Returns the number of workflows reconciled and any errors encountered.
func (r *Reconciler) ReconcileRunningWorkflows(ctx context.Context) (reconciled int, errors []error) {
	// One passStats per pass: anomalies are aggregated into a single summary
	// line, emitted on every exit path (the reap below repairs rows even on a
	// pass where nothing is left running).
	stats := &passStats{}
	defer func() {
		// One summary line per pass when anything anomalous was found; silence
		// when clean. The per-anomaly detail is in the ERROR logs.
		if total, summary := stats.snapshot(); total > 0 {
			logging.Info("[Reconciler] Anomalies this pass",
				"total", total,
				"byClass", summary,
			)
		}
	}()

	reaped, reapErr := r.reapOrphanedDescendants(ctx, stats)
	reconciled += reaped
	if reapErr != nil {
		errors = append(errors, reapErr)
	}

	// After the reap: reaping moves a child to a terminal status, which is
	// precisely the condition that strands its parent's spawn call, so running
	// this second lets both repairs land in the same pass instead of leaving
	// the parent blocked until the next one.
	if _, repairErr := r.repairStrandedSpawnToolCalls(ctx, stats); repairErr != nil {
		errors = append(errors, repairErr)
	}
	// Same reasoning, async spawn's counterpart (spec §7.1): a reap above can
	// just as easily strand a backgrounded spawn's mailbox report as it can
	// strand a synchronous spawn's tool result.
	if _, repairErr := r.repairStrandedBackgroundSpawns(ctx, stats); repairErr != nil {
		errors = append(errors, repairErr)
	}
	// Last of the three, for the same reason the two above run after the
	// reap: a cascade that moves a thread to a terminal status is exactly
	// what strands its mailbox, so resolving here catches the rows this very
	// pass orphaned rather than leaving them until the next one.
	if _, repairErr := r.resolveOrphanedAgentMessages(ctx, stats); repairErr != nil {
		errors = append(errors, repairErr)
	}

	allWorkflows, err := r.repo.ListWorkflowsByStatus(ctx, db.WorkflowStatusRunning)
	if err != nil {
		return reconciled, append(errors, fmt.Errorf("failed to list running workflows: %w", err))
	}
	pausedWorkflows, err := r.repo.ListWorkflowsByStatus(ctx, db.WorkflowStatusPaused)
	if err != nil {
		return reconciled, append(errors, fmt.Errorf("failed to list paused workflows: %w", err))
	}
	allWorkflows = append(allWorkflows, pausedWorkflows...)

	if len(allWorkflows) == 0 {
		// Nothing running: any leftover debounce/streak entries are moot.
		r.pruneStuckObservations(nil)
		r.pruneProgressObservations(nil)
		r.resetGuard.Prune(nil)
		return reconciled, errors
	}

	logging.Info("[Reconciler] Reconciling workflows",
		"running", len(allWorkflows),
	)

	// Reconcile each workflow in parallel (with limited concurrency).
	// One pollerState per pass: the poller check is fetched lazily on the
	// first stuck observation and cached for every other workflow this pass.
	pollers := &pollerState{}
	const maxConcurrency = 10
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, wf := range allWorkflows {
		wg.Add(1)
		go func(wf *db.Workflow) {
			defer wg.Done()
			sem <- struct{}{}        // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			result := r.reconcileWorkflow(ctx, wf, pollers, stats)
			if result.WasStale {
				mu.Lock()
				reconciled++
				mu.Unlock()
			}
			if result.Error != nil {
				mu.Lock()
				errors = append(errors, result.Error)
				mu.Unlock()
			}
		}(wf)
	}

	wg.Wait()

	// Drop debounce/streak entries for workflows no longer in the pass
	// (running or paused).
	runningIDs := make(map[string]bool, len(allWorkflows))
	for _, wf := range allWorkflows {
		runningIDs[wf.ID] = true
	}
	r.pruneStuckObservations(runningIDs)
	r.pruneProgressObservations(runningIDs)
	r.resetGuard.Prune(runningIDs)

	if reconciled > 0 {
		logging.Info("[Reconciler] Reconciliation complete",
			"reconciled", reconciled,
			"errors", len(errors),
		)
	}

	return reconciled, errors
}

// addWorkflowErrorMessage adds an error message to the chat explaining the workflow failure
func (r *Reconciler) addWorkflowErrorMessage(ctx context.Context, wf *db.Workflow, state *TemporalWorkflowState) error {
	var stuckInfo string
	if state.StuckTaskType == "workflow" {
		stuckInfo = "A workflow task"
	} else {
		stuckInfo = fmt.Sprintf("Activity '%s'", state.StuckActivityType)
	}

	errorText := fmt.Sprintf(
		"⚠️ **Workflow Error**\n\n"+
			"%s became stuck and automatic recovery was attempted but failed. "+
			"The workflow has been terminated.\n\n"+
			"**To continue:** Send a new message to restart the conversation.",
		stuckInfo,
	)

	// Use SaveMessageToThread which handles creating message + content block atomically
	_, err := r.repo.SaveMessageToThread(ctx, wf.ChatID, wf.Thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), errorText, &wf.ID, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create error message: %w", err)
	}

	return nil
}

// StartBackgroundReconciliation starts the background reconciliation loop.
// It periodically checks all running workflows and reconciles any stale state.
// Call Stop() to stop the background loop.
func (r *Reconciler) StartBackgroundReconciliation(ctx context.Context) {
	r.mu.Lock()
	if r.isRunning {
		r.mu.Unlock()
		return
	}
	r.isRunning = true
	r.stopPolling = make(chan struct{})
	r.pollDone = make(chan struct{})
	r.mu.Unlock()

	logging.Info("[Reconciler] Starting background reconciliation",
		"interval", r.pollInterval,
	)

	go func() {
		defer close(r.pollDone)

		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logging.Info("[Reconciler] Context cancelled, stopping background reconciliation")
				return
			case <-r.stopPolling:
				logging.Info("[Reconciler] Stop signal received, stopping background reconciliation")
				return
			case <-ticker.C:
				// Run reconciliation with a timeout
				reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				reconciled, errors := r.ReconcileRunningWorkflows(reconcileCtx)
				cancel()

				if len(errors) > 0 {
					logging.Warn("[Reconciler] Background reconciliation had errors",
						"reconciled", reconciled,
						"errors", len(errors),
					)
				}
			}
		}
	}()
}

// Stop stops the background reconciliation loop.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	if !r.isRunning {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	logging.Info("[Reconciler] Stopping background reconciliation")

	// Signal stop
	close(r.stopPolling)

	// Wait for completion with timeout
	select {
	case <-r.pollDone:
		logging.Info("[Reconciler] Background reconciliation stopped")
	case <-time.After(5 * time.Second):
		logging.Warn("[Reconciler] Timeout waiting for background reconciliation to stop")
	}

	r.mu.Lock()
	r.isRunning = false
	r.mu.Unlock()
}
