// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// ChildWorkflowInitOpts contains options for initializing a child workflow with its thread.
type ChildWorkflowInitOpts struct {
	Ctx              workflow.Context
	ChatID           string
	ParentWorkflowID string
	ChildWorkflowID  string
	ChildThreadID    string
	WorkflowName     string
	ThreadTitle      *string
	ThreadMode       string // "inherit", "new", or "fork" — uses model.ThreadMode* constants
	ForkFromThread   string // Only used when ThreadMode == model.ThreadModeFork
	ParentThread     string // Parent thread ID for tracking lineage (set for both fork and new)
	SpawnedByNodeID  string
	LoopIteration    *int64
	InjectMessage    *InjectMessageConfig // nil if no inject message
	Logger           log.Logger
}

// InjectMessageConfig contains configuration for an inject message to save after creating
// the child workflow's thread.
type InjectMessageConfig struct {
	Role        string
	Content     string
	Attachments []string
}

// initChildWorkflow creates the child workflow+thread and optionally saves an inject message.
//
// Thread mode behavior:
// - model.ThreadModeInherit: Creates workflow, links to existing thread (no new thread)
// - model.ThreadModeNew: Creates workflow + new isolated thread
// - model.ThreadModeFork: Creates workflow + forked thread (with fork metadata)
//
// CreateWorkflowWithThread handles all three cases - it checks if thread exists
// and either creates new or updates existing thread's workflow_id.
func initChildWorkflow(opts ChildWorkflowInitOpts) error {
	activityOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	}
	activityCtx := workflow.WithActivityOptions(opts.Ctx, activityOpts)

	// Step 1: Create workflow + thread
	// For inherit: thread exists, CreateWorkflowWithThread just links workflow to it
	// For new: creates isolated thread
	// For fork: creates forked thread with context inheritance
	// Use map[string]interface{} to avoid import cycle with handlers package
	createInput := map[string]interface{}{
		"workflow_id":   opts.ChildWorkflowID,
		"workflow_name": opts.WorkflowName,
		"chat_id":       opts.ChatID,
		"thread_id":     opts.ChildThreadID,
	}

	if opts.ParentWorkflowID != "" {
		createInput["parent_workflow_id"] = opts.ParentWorkflowID
	}
	if opts.ThreadTitle != nil && *opts.ThreadTitle != "" {
		createInput["thread_title"] = *opts.ThreadTitle
	}
	if opts.ThreadMode == model.ThreadModeFork && opts.ForkFromThread != "" {
		createInput["fork_from_thread"] = opts.ForkFromThread
	}
	if opts.ParentThread != "" {
		createInput["parent_thread"] = opts.ParentThread
	}
	if opts.SpawnedByNodeID != "" {
		createInput["spawned_by_node_id"] = opts.SpawnedByNodeID
	}
	if opts.LoopIteration != nil {
		createInput["loop_iteration"] = *opts.LoopIteration
	}

	if err := workflow.ExecuteActivity(activityCtx, "CreateWorkflowWithThread", createInput).Get(opts.Ctx, nil); err != nil {
		return fmt.Errorf("failed to create child workflow+thread: %w", err)
	}

	opts.Logger.Info("[initChildWorkflow] Created child workflow",
		"childWorkflowID", opts.ChildWorkflowID,
		"childThreadID", opts.ChildThreadID,
		"threadMode", opts.ThreadMode,
	)

	// Step 2: Save inject message if provided (thread now guaranteed to exist)
	if opts.InjectMessage != nil && opts.InjectMessage.Content != "" {
		// Build input for the SaveMessage activity.
		// V2_SaveMessage expects {"runtime": {...}, "node": {"args": {"resolved_role": ..., ...}}}.
		flatInput := &types.SaveMessageInput{
			ChatID:      opts.ChatID,
			Thread:      opts.ChildThreadID,
			Role:        opts.InjectMessage.Role,
			Content:     opts.InjectMessage.Content,
			Attachments: opts.InjectMessage.Attachments,
			WorkflowID:  opts.ChildWorkflowID,
		}
		rtx := types.RuntimeContext{
			ChatID:     opts.ChatID,
			Thread:     opts.ChildThreadID,
			WorkflowID: opts.ChildWorkflowID,
		}
		saveInput := types.ActivityInput{Runtime: rtx, Node: buildSaveMessageNode(flatInput)}

		if err := workflow.ExecuteActivity(activityCtx, "SaveMessage", saveInput).Get(opts.Ctx, nil); err != nil {
			opts.Logger.Error("[initChildWorkflow] Failed to save inject message",
				"childWorkflowID", opts.ChildWorkflowID,
				"childThreadID", opts.ChildThreadID,
				"error", err,
			)
			return fmt.Errorf("failed to save inject message: %w", err)
		}

		opts.Logger.Info("[initChildWorkflow] Saved inject message to child thread",
			"childWorkflowID", opts.ChildWorkflowID,
			"childThreadID", opts.ChildThreadID,
			"role", opts.InjectMessage.Role,
		)
	}

	return nil
}
