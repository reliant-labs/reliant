// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
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
// lost from Temporal's queue) by terminating the workflow and marking it
// as failed.
type Reconciler struct {
	repo       db.Repository
	tempClient client.Client

	// Background polling state
	mu          sync.Mutex
	stopPolling chan struct{}
	pollDone    chan struct{}
	isRunning   bool

	// Configuration
	pollInterval           time.Duration
	stuckActivityThreshold time.Duration
}

// ReconcilerConfig contains configuration for the Reconciler
type ReconcilerConfig struct {
	// PollInterval is how often the background reconciliation runs
	// Default: 30 seconds
	PollInterval time.Duration

	// StuckActivityThreshold is how long a task can be in "Scheduled" state
	// without being picked up before it's considered stuck and marked as failed.
	// Default: 30 seconds
	StuckActivityThreshold time.Duration
}

// DefaultConfig returns the default reconciler configuration
func DefaultConfig() *ReconcilerConfig {
	return &ReconcilerConfig{
		PollInterval:           30 * time.Second,
		StuckActivityThreshold: 30 * time.Second,
	}
}

// NewReconciler creates a new workflow reconciler
func NewReconciler(repo db.Repository, tempClient client.Client, config *ReconcilerConfig) *Reconciler {
	if config == nil {
		config = DefaultConfig()
	}
	return &Reconciler{
		repo:                   repo,
		tempClient:             tempClient,
		pollInterval:           config.PollInterval,
		stuckActivityThreshold: config.StuckActivityThreshold,
		stopPolling:            make(chan struct{}),
		pollDone:               make(chan struct{}),
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
}

// ReconciliationResult contains the result of reconciling a single workflow
type ReconciliationResult struct {
	WorkflowID     string
	ChatID         string
	DBStatus       db.WorkflowStatus
	TemporalStatus db.WorkflowStatus
	WasStale       bool // True if DB status was updated
	NeedsRecovery  bool // True if workflow is lost and needs user action
	Error          error
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
	case enums.WORKFLOW_EXECUTION_STATUS_FAILED, enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		mappedStatus = db.WorkflowStatusFailed
	case enums.WORKFLOW_EXECUTION_STATUS_CANCELED, enums.WORKFLOW_EXECUTION_STATUS_TERMINATED:
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

					logging.Warn("[Reconciler] Detected stuck workflow task",
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

						logging.Warn("[Reconciler] Detected stuck activity",
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
		logging.Warn("[Reconciler] Workflow not found in Temporal, marking as completed",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"dbStatus", wf.Status,
		)

		swapped, err := r.repo.CompareAndSwapWorkflowStatus(ctx, wf.ID, db.WorkflowStatusCompleted, wf.Status)
		if err != nil {
			result.Error = fmt.Errorf("failed to mark lost workflow as completed: %w", err)
			return result
		}
		if !swapped {
			return result // another reconciler already handled this
		}

		result.TemporalStatus = db.WorkflowStatusCompleted
		result.WasStale = true
		result.NeedsRecovery = true

		return result
	}

	result.TemporalStatus = temporalState.Status

	// Check for stuck task (workflow task or activity) - this indicates the task was lost
	// and the workflow is unrecoverable. Mark it as failed and notify the user.
	// With the shared worker pool, there's no per-workflow worker lifecycle to worry about.
	if temporalState.HasStuckTask && wf.Status == db.WorkflowStatusRunning {
		logging.Error("[Reconciler] Workflow is stuck and unrecoverable - terminating and marking as failed",
			"workflowID", wf.ID,
			"chatID", wf.ChatID,
			"stuckTaskType", temporalState.StuckTaskType,
			"stuckActivityID", temporalState.StuckActivityID,
			"stuckActivityType", temporalState.StuckActivityType,
			"stuckDuration", temporalState.StuckDuration,
		)

		// Terminate the stuck workflow in Temporal so it's no longer "running"
		// This prevents any confusion where DB says failed but Temporal says running
		terminateReason := fmt.Sprintf("Workflow stuck: %s task in Scheduled state for %v", temporalState.StuckTaskType, temporalState.StuckDuration)
		if err := r.tempClient.TerminateWorkflow(ctx, wf.ID, "", terminateReason); err != nil {
			logging.Warn("[Reconciler] Failed to terminate stuck workflow in Temporal",
				"error", err,
				"workflowID", wf.ID,
			)
			// Continue anyway - we still want to mark it failed in DB
		}

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
		}
	}

	return result
}

// ReconcileRunningWorkflows reconciles all workflows with status='running'.
// This is the main entry point for background reconciliation.
// Returns the number of workflows reconciled and any errors encountered.
func (r *Reconciler) ReconcileRunningWorkflows(ctx context.Context) (reconciled int, errors []error) {
	// Get all running workflows from DB
	allWorkflows, err := r.repo.ListWorkflowsByStatus(ctx, db.WorkflowStatusRunning)
	if err != nil {
		return 0, []error{fmt.Errorf("failed to list running workflows: %w", err)}
	}

	if len(allWorkflows) == 0 {
		return 0, nil
	}

	logging.Info("[Reconciler] Reconciling workflows",
		"running", len(allWorkflows),
	)

	// Reconcile each workflow in parallel (with limited concurrency)
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

			result := r.ReconcileWorkflow(ctx, wf)
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
			"%s became stuck and could not recover after a system restart. "+
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
