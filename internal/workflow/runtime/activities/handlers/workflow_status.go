// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// ============================================================================
// TYPES (strongly typed inputs/outputs)
// ============================================================================

// WorkflowStatusInput is the input for WorkflowStatus activity
// RouterDecisionInfo carries routing decision metadata for UI display.
type RouterDecisionInfo struct {
	Workflow string `json:"workflow"` // Selected workflow ref
	Preset   string `json:"preset"`   // Selected preset
}

type WorkflowStatusInput struct {
	ChatID              string              `json:"chat_id" reliant:"-"`
	WorkflowID          string              `json:"workflow_id"`
	WorkflowName        string              `json:"workflow_name"`
	Status              string              `json:"status"`                            // "started", "completed", "failed", or "cancelled"
	ParentWorkflowID    string              `json:"parent_workflow_id,omitempty"`      // Parent workflow UUID (empty for root)
	Thread              string              `json:"thread,omitempty"`                  // Thread path for message isolation
	ThreadTitle         string              `json:"thread_title,omitempty"`            // Human-readable title for the thread (e.g., preset name or node ID)
	Title               string              `json:"title,omitempty"`                   // Title/prompt (for child workflows, shown in UI swim lane header)
	SpawnedByToolCallID string              `json:"spawned_by_tool_call_id,omitempty"` // Tool call ID that spawned this workflow
	SpawnedByNodeID     string              `json:"spawned_by_node_id,omitempty"`      // Node ID that spawned this child workflow
	LoopIteration       *int64              `json:"loop_iteration,omitempty"`          // Iteration index when spawned by a loop node
	RouterDecision      *RouterDecisionInfo `json:"router_decision,omitempty"`         // Routing decision metadata (set when spawned by a router node)
	Resumed             bool                `json:"resumed,omitempty"`                 // "started" follows a self-pause resume: un-pause the chat's workflow rows chat-wide
	// Outcome is the run's VERDICT — "success" or "failure" — as declared by the
	// terminal node the graph reached (Node.outcome). It is orthogonal to Status:
	// a run that routes to a `failed` node has Status "completed" (the Temporal
	// execution really did finish) and Outcome "failure". Empty means the
	// workflow declared no outcome, and the stored value is left untouched —
	// absence is not failure.
	Outcome string `json:"outcome,omitempty"`
}

// WorkflowStatusOutput is the output from WorkflowStatus activity
type WorkflowStatusOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// WorkflowStatusActivity implements TypedActivity[WorkflowStatusInput, WorkflowStatusOutput]
// This activity writes workflow status updates to chat_updates for UI notifications
type WorkflowStatusActivity struct {
	repo db.Repository
}

// NewWorkflowStatusActivity creates a new WorkflowStatusActivity
func NewWorkflowStatusActivity(repo db.Repository) *WorkflowStatusActivity {
	return &WorkflowStatusActivity{
		repo: repo,
	}
}

// Name returns the activity name for registration
func (a *WorkflowStatusActivity) Name() string {
	return "WorkflowStatus"
}

// Execute writes workflow status to chat_updates table, updates chat state, and tracks workflow hierarchy
func (a *WorkflowStatusActivity) Execute(ctx context.Context, input WorkflowStatusInput) (WorkflowStatusOutput, error) {
	// Track workflow analytics (non-blocking)
	go a.trackAnalytics(ctx, input)

	// Track workflow in database for parent-child hierarchy
	if err := a.trackWorkflow(ctx, input); err != nil {
		// Log but don't fail - chat_update is more important for UI
		logging.Warn("[WorkflowStatus] Failed to track workflow", "error", err)
	}

	// Determine if this is a root workflow (no parent)
	isRootWorkflow := input.ParentWorkflowID == ""

	// Update chat notification state based on workflow status
	// Note: Activity (running) is derived from workflow.status, not stored in chat.state
	// - "started" -> no change
	// - "completed" -> mark unread (only for ROOT workflow - user should see result)
	// - "cancelled" -> no change (user cancelled, no action needed)
	// - "failed" -> no change (nothing pending)
	if input.Status == "completed" && isRootWorkflow {
		if err := a.repo.UpdateChatUnread(ctx, input.ChatID, true, "workflow_completed"); err != nil {
			logging.Warn("[WorkflowStatus] Failed to mark chat as unread", "error", err)
		}
	}

	// Build update data (shared for both chat_updates and user_updates)
	updateData := map[string]interface{}{
		"update_type":   "workflow_status",
		"workflow_id":   input.WorkflowID,
		"workflow_name": input.WorkflowName,
		"status":        input.Status,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
		// Parentage travels with the event so a consumer can tell a ROOT
		// terminal from a child's without a second lookup. `workflow follow`
		// needs exactly this to know when the run it is following has ended.
		"parent_workflow_id": input.ParentWorkflowID,
	}
	// The verdict travels on the same event as the terminal status so a live
	// follower learns "ended, did not pass" at the boundary itself rather than
	// having to go re-read the run afterwards.
	if input.Outcome != "" {
		updateData["outcome"] = input.Outcome
	}

	// Marshal update data
	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return WorkflowStatusOutput{Success: false}, fmt.Errorf("failed to marshal workflow status: %w", err)
	}

	// Write to chat_updates for per-chat websocket (all workflows)
	if err := a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeWorkflowStatus, input.WorkflowID, string(updateDataJSON)); err != nil {
		return WorkflowStatusOutput{Success: false}, fmt.Errorf("failed to create chat_update: %w", err)
	}

	// For child workflows starting, also emit a "thread" update for UI swim lanes
	if input.Status == "started" && input.ParentWorkflowID != "" {
		if err := a.emitThreadUpdate(ctx, input); err != nil {
			// Log but don't fail - the workflow_status update is more important
			logging.Warn("[WorkflowStatus] Failed to emit thread update", "error", err)
		}
	}

	// For child workflows completing/failing/cancelling, update thread status
	if input.Status != "started" && input.ParentWorkflowID != "" {
		if err := a.emitThreadStatusUpdate(ctx, input); err != nil {
			logging.Warn("[WorkflowStatus] Failed to emit thread status update", "error", err)
		}
	}

	// Emit refetch signal so frontend updates workflow executions without polling
	if err := a.repo.EmitChatRefetch(ctx, input.ChatID, db.RefetchWorkflowExecutions); err != nil {
		logging.Warn("[WorkflowStatus] Failed to emit workflow executions refetch", "error", err)
	}

	return WorkflowStatusOutput{Success: true}, nil
}

// trackWorkflow creates or updates a workflow record in the database
func (a *WorkflowStatusActivity) trackWorkflow(ctx context.Context, input WorkflowStatusInput) error {
	switch input.Status {
	case "started":
		// Post-resume notification: a resume un-parks the ENTIRE Temporal
		// execution, so un-pause the chat's workflow rows chat-wide (the
		// mirror of the "paused" chat-wide propagation below). The row-level
		// handling below still creates/updates the notifying workflow's row.
		if input.Resumed && input.ChatID != "" {
			if err := a.repo.ResumeWorkflowsByChat(ctx, input.ChatID); err != nil {
				logging.Warn("[WorkflowStatus] Failed to resume chat workflows after self-pause resume",
					"chatID", input.ChatID,
					"workflowID", input.WorkflowID,
					"error", err)
			}
		}

		// Check if workflow already exists (e.g., branched chat with pending status)
		existingWorkflow, err := a.repo.GetWorkflow(ctx, input.WorkflowID)
		if err == nil && existingWorkflow != nil {
			if existingWorkflow.Status == db.WorkflowStatusRunning {
				return nil // Already running
			}
			// Transition to running from any non-running state (pending, completed, failed, cancelled).
			// This handles follow-up messages where SendMessage starts a new Temporal run
			// reusing the same workflow ID after the previous run completed.
			return a.repo.UpdateWorkflowStatus(ctx, input.WorkflowID, db.WorkflowStatusRunning)
		}

		// Create new workflow record
		// Thread path should be workflow ID - no more "0" default
		thread := input.Thread
		if thread == "" {
			// Use workflow ID as thread path (both root and child workflows)
			thread = input.WorkflowID
		}

		if input.ParentWorkflowID == "" {
			// Root workflow - build workflow struct and create it
			// Set spawned_by_node_id if provided
			var spawnedByNodeID *string
			if input.SpawnedByNodeID != "" {
				spawnedByNodeID = &input.SpawnedByNodeID
			}

			workflow := &db.Workflow{
				ID:              input.WorkflowID,
				ParentID:        nil, // Root workflow has no parent
				ChatID:          input.ChatID,
				WorkflowName:    input.WorkflowName,
				Thread:          thread,
				Status:          db.WorkflowStatusRunning,
				SpawnedByNodeID: spawnedByNodeID,
				LoopIteration:   input.LoopIteration,
				CreatedAt:       time.Now().UTC(),
			}

			// Root workflow - thread already exists from ChatService
			// Create workflow first, then update the thread's workflow_id
			if err := a.repo.CreateWorkflow(ctx, workflow); err != nil {
				return err
			}
			if _, err := a.repo.UpdateThreadWorkflow(ctx, thread, input.WorkflowID); err != nil {
				logging.Warn("[WorkflowStatus] Failed to update thread workflow_id",
					db.UpdateTypeThread, thread,
					"workflow_id", input.WorkflowID,
					"error", err)
			}
		} else {
			// Child workflow - workflow and thread are normally created by the
			// parent via V2_CreateWorkflowWithThread before the child starts,
			// but this status write can race ahead of the parent's commit.
			// Retry the lookup briefly, then create the row ourselves,
			// mirroring how the parent creates it. CreateWorkflow is
			// idempotent (INSERT ... ON CONFLICT DO NOTHING), so the parent's
			// create remains a safe no-op if it lands afterwards.
			existingWorkflow, err := a.getWorkflowWithRetry(ctx, input.WorkflowID)
			if err != nil {
				var spawnedByNodeID *string
				if input.SpawnedByNodeID != "" {
					spawnedByNodeID = &input.SpawnedByNodeID
				}
				parentID := input.ParentWorkflowID
				childWorkflow := &db.Workflow{
					ID:              input.WorkflowID,
					ParentID:        &parentID,
					ChatID:          input.ChatID,
					WorkflowName:    input.WorkflowName,
					Thread:          thread,
					Status:          db.WorkflowStatusRunning,
					SpawnedByNodeID: spawnedByNodeID,
					LoopIteration:   input.LoopIteration,
					CreatedAt:       time.Now().UTC(),
				}
				if createErr := a.repo.CreateWorkflow(ctx, childWorkflow); createErr != nil {
					return fmt.Errorf("child workflow %s does not exist (lookup: %v) and create-on-missing failed: %w", input.WorkflowID, err, createErr)
				}
				logging.Info("[WorkflowStatus] Created missing child workflow row (status update raced ahead of parent creation)",
					"workflowID", input.WorkflowID,
					"parentWorkflowID", input.ParentWorkflowID)
				return nil
			}

			// Update status to running if needed
			if existingWorkflow.Status != db.WorkflowStatusRunning {
				if err := a.repo.UpdateWorkflowStatus(ctx, input.WorkflowID, db.WorkflowStatusRunning); err != nil {
					return fmt.Errorf("failed to update workflow status: %w", err)
				}
			}

			logging.Info("[WorkflowStatus] Child workflow status updated",
				"workflowID", input.WorkflowID,
				"previousStatus", existingWorkflow.Status,
				"newStatus", db.WorkflowStatusRunning)
		}

		return nil

	case "completed":
		// Complete the workflow itself, and — for a ROOT workflow that declares a
		// `transition_to` target — switch the chat to that workflow in the SAME
		// commit, so the status write and the workflow_name switch land atomically.
		// If the switch's DB write fails, the whole tx rolls back and Temporal
		// retries the completion; TransitionChatOnCompletion is idempotent so the
		// retry is safe.
		var transitionedTo string
		if err := a.repo.RunTx(ctx, func(txCtx context.Context) error {
			if err := a.repo.UpdateWorkflowStatus(txCtx, input.WorkflowID, db.WorkflowStatusCompleted); err != nil {
				return err
			}
			// The verdict lands in the SAME commit as the terminal status. Two
			// writes would leave a window where the run reads as a plain
			// COMPLETED — precisely the false green this field exists to close.
			if input.Outcome != "" {
				if err := a.repo.SetWorkflowOutcome(txCtx, input.WorkflowID, input.Outcome); err != nil {
					return err
				}
			}
			// Only the chat's active root workflow transitions the chat; a
			// completing child (spawn/fork) must never switch the chat out from
			// under its parent.
			if input.ParentWorkflowID == "" {
				to, tErr := TransitionChatOnCompletion(txCtx, a.repo, input.ChatID, input.WorkflowName)
				if tErr != nil {
					return tErr
				}
				transitionedTo = to
			}
			return nil
		}); err != nil {
			return err
		}
		// UI signal for the switch — best-effort, outside the tx so a cosmetic
		// message failure never rolls back the committed transition.
		if transitionedTo != "" {
			thread := input.Thread
			if thread == "" {
				thread = input.WorkflowID
			}
			EmitTransitionMessage(ctx, a.repo, input.ChatID, thread, input.WorkflowID, transitionedTo)
		}
		// A completed run never resumes at position — drop the checkpoint so a
		// later fresh run can't pick up a stale position.
		a.clearCheckpoint(ctx, input.WorkflowID)
		// Cascade completion to any thread records owned by this workflow
		// Thread records ("thread:*") are created by fork()/new() in action configs
		return a.repo.CascadeTerminalStatusToDescendants(ctx, input.WorkflowID, db.WorkflowStatusCompleted)

	case "failed":
		// NOTE: the position checkpoint is intentionally KEPT on failure — it
		// is what lets the next user message resume the run at position.
		if err := a.repo.UpdateWorkflowStatus(ctx, input.WorkflowID, db.WorkflowStatusFailed); err != nil {
			return err
		}
		return a.repo.CascadeTerminalStatusToDescendants(ctx, input.WorkflowID, db.WorkflowStatusFailed)

	case "cancelled":
		if err := a.repo.UpdateWorkflowStatus(ctx, input.WorkflowID, db.WorkflowStatusCancelled); err != nil {
			return err
		}
		// User-cancelled runs start fresh on the next message — drop the
		// checkpoint (resume-at-position applies only to failed/terminated).
		a.clearCheckpoint(ctx, input.WorkflowID)
		return a.repo.CascadeTerminalStatusToDescendants(ctx, input.WorkflowID, db.WorkflowStatusCancelled)

	case "paused":
		// Self-pause: workflow is pausing itself (e.g., due to rate limit
		// exhaustion or the daemon-offline breaker). A self-pause parks the
		// ENTIRE Temporal execution — every nested inline workflow shares the
		// root execution's pause flag — but the notifying executor may be a
		// nested spawn/loop whose workflow_id is not the root row. The paused
		// status must land chat-wide (root row included): the root row is what
		// SendMessage's resume routing and the reconciler's progress-watchdog
		// pause exclusion consult. Leaving it "running" hides the pause from
		// both — the watchdog then terminates a legitimately parked workflow.
		if err := a.repo.UpdateWorkflowStatus(ctx, input.WorkflowID, db.WorkflowStatusPaused); err != nil {
			return err
		}
		if input.ChatID != "" {
			return a.repo.PauseRunningWorkflowsByChat(ctx, input.ChatID)
		}
		return nil

	default:
		// Unknown status - skip tracking
		return nil
	}
}

// clearCheckpoint drops the position checkpoint for a workflow. Best-effort:
// a stale checkpoint is harmless for completed/cancelled workflows because
// SendMessage only consults it for failed/terminated predecessors.
func (a *WorkflowStatusActivity) clearCheckpoint(ctx context.Context, workflowID string) {
	if err := a.repo.DeleteWorkflowCheckpoint(ctx, workflowID); err != nil {
		logging.Warn("[WorkflowStatus] Failed to clear workflow checkpoint",
			"workflowID", workflowID,
			"error", err)
	}
}

// getWorkflowWithRetry re-reads a workflow row a few times with short backoff
// to absorb the window where a child's status write races ahead of the
// parent's CreateWorkflowWithThread commit.
func (a *WorkflowStatusActivity) getWorkflowWithRetry(ctx context.Context, workflowID string) (*db.Workflow, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		workflow, err := a.repo.GetWorkflow(ctx, workflowID)
		if err == nil && workflow != nil {
			return workflow, nil
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("workflow %s not found", workflowID)
		}
	}
	return nil, lastErr
}

// emitThreadUpdate emits a "thread" update for UI swim lanes when a child workflow starts
func (a *WorkflowStatusActivity) emitThreadUpdate(ctx context.Context, input WorkflowStatusInput) error {
	now := time.Now().UTC()

	// Thread path should be workflow ID
	thread := input.Thread
	if thread == "" {
		thread = input.WorkflowID
	}

	// NOTE: planning_mode is now a workflow input param, not a chat field.
	// The frontend gets planning_mode from the workflow state.
	updateData := map[string]interface{}{
		"update_type":             "thread",
		"id":                      input.WorkflowID,
		"chat_id":                 input.ChatID,
		"thread":                  thread,
		"workflow_id":             input.WorkflowID,
		"workflow_name":           input.WorkflowName,
		"status":                  "running",
		"created_at":              now.Format(time.RFC3339),
		"spawned_by_tool_call_id": input.SpawnedByToolCallID,
		"title":                   input.Title,
	}
	if input.ThreadTitle != "" {
		updateData["thread_title"] = input.ThreadTitle
	}
	if input.SpawnedByNodeID != "" {
		updateData["spawned_by_node_id"] = input.SpawnedByNodeID
	}
	if input.RouterDecision != nil {
		updateData["router_decision"] = map[string]string{
			"workflow": input.RouterDecision.Workflow,
			"preset":   input.RouterDecision.Preset,
		}
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal thread update: %w", err)
	}

	return a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeThread, input.WorkflowID, string(updateDataJSON))
}

// trackAnalytics sends workflow analytics events to Statsig
// Only tracks root workflows to reduce event volume - child workflow data
// can be derived from root workflow metrics if needed
func (a *WorkflowStatusActivity) trackAnalytics(ctx context.Context, input WorkflowStatusInput) {
	// Skip child workflows to reduce event volume
	isChild := input.ParentWorkflowID != ""
	if isChild {
		return
	}

	// Determine workflow type
	workflowType := "custom"
	if strings.HasPrefix(input.WorkflowName, "builtin://") {
		workflowType = "builtin"
	}

	var userID string
	var presets []string
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		logging.Warn("[WorkflowStatus] Failed to load chat for analytics", "chatID", input.ChatID, "error", err)
	} else {
		userID = chat.UserID
		presets = selectedPresetNames(chat.SelectedPresets)
	}

	var projectID string
	if chat != nil {
		projectID = chat.ProjectID
	}

	metrics := analytics.WorkflowMetrics{
		WorkflowID:   input.WorkflowID,
		WorkflowName: input.WorkflowName,
		WorkflowType: workflowType,
		ChatID:       input.ChatID,
		ProjectID:    projectID,
		IsWorkspace:  chat != nil && chat.WorktreeID != nil && *chat.WorktreeID != "",
		Presets:      presets,
		PresetTypes:  classifyPresetTypes(presets),
		IsChild:      false, // Always false now since we skip children
	}

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	switch input.Status {
	case "started":
		analyticsClient.TrackWorkflowStarted(metrics)
	case "completed":
		metrics.Success = true
		analyticsClient.TrackWorkflowEnded(metrics)
	case "failed":
		metrics.Success = false
		analyticsClient.TrackWorkflowEnded(metrics)
	case "cancelled":
		metrics.Success = false
		metrics.ErrorMessage = "workflow_cancelled"
		analyticsClient.TrackWorkflowEnded(metrics)
	}
}

func selectedPresetNames(selected map[string]string) []string {
	if len(selected) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(selected))
	presets := make([]string, 0, len(selected))
	for _, presetName := range selected {
		if presetName == "" {
			continue
		}
		if _, exists := seen[presetName]; exists {
			continue
		}
		seen[presetName] = struct{}{}
		presets = append(presets, presetName)
	}
	sort.Strings(presets)
	return presets
}

// classifyPresetTypes returns a slice of "builtin" or "custom" for each preset name.
// A preset is "builtin" if it exists in the embedded builtin presets directory.
func classifyPresetTypes(presetNames []string) []string {
	if len(presetNames) == 0 {
		return nil
	}

	// Build set of builtin preset names
	builtinNames := make(map[string]bool)
	entries, err := builtin.BuiltinPresetsFS.ReadDir("presets")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			name = strings.TrimSuffix(name, ".yml")
			builtinNames[name] = true
		}
	}

	types := make([]string, len(presetNames))
	for i, name := range presetNames {
		if builtinNames[name] {
			types[i] = "builtin"
		} else {
			types[i] = "custom"
		}
	}
	return types
}

// emitThreadStatusUpdate emits a "thread" update when a child workflow reaches a terminal status
func (a *WorkflowStatusActivity) emitThreadStatusUpdate(ctx context.Context, input WorkflowStatusInput) error {
	now := time.Now().UTC()

	thread := input.Thread
	if thread == "" {
		thread = input.WorkflowID
	}

	// NOTE: planning_mode is now a workflow input param, not a chat field.
	// The frontend gets planning_mode from the workflow state.
	updateData := map[string]interface{}{
		"update_type":   "thread",
		"id":            input.WorkflowID,
		"chat_id":       input.ChatID,
		"thread":        thread,
		"workflow_id":   input.WorkflowID,
		"workflow_name": input.WorkflowName,
		"status":        input.Status,
		"completed_at":  now.Format(time.RFC3339),
		"title":         input.Title,
	}
	if input.ThreadTitle != "" {
		updateData["thread_title"] = input.ThreadTitle
	}
	if input.SpawnedByNodeID != "" {
		updateData["spawned_by_node_id"] = input.SpawnedByNodeID
	}
	if input.SpawnedByToolCallID != "" {
		updateData["spawned_by_tool_call_id"] = input.SpawnedByToolCallID
	}

	updateDataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal thread status update: %w", err)
	}

	return a.repo.CreateChatUpdate(ctx, input.ChatID, db.UpdateTypeThread, input.WorkflowID, string(updateDataJSON))
}
