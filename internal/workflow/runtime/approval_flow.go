// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// defaultApprovalTimeout is how long an approval node waits for a human before
// resolving itself as "timeout".
const defaultApprovalTimeout = 1 * time.Hour

// approvalExecution is the fully-resolved description of one approval wait. The
// caller has already evaluated the node's CEL config, so this carries values
// rather than a node.
type approvalExecution struct {
	ChatID     string
	WorkflowID string
	ThreadID   string
	StepID     string
	// LoopNodeID and LoopIteration attribute the approval to the loop
	// iteration that raised it, matching what askQuestionExecution carries.
	LoopNodeID    string
	LoopIteration int
	// NodePath is the fully-qualified dotted graph position of the approval
	// node ("impl_loop.attempt.approve"). Observability only — it is passed to
	// ApprovalCreate for logging and test observation and is never written to
	// the loop_node_id column, which stays loop-scoped.
	NodePath   string
	Title      string
	TimeoutStr string
	Logger     log.Logger
}

// approvalExecutionFromNode evaluates an approval node's config and returns the
// resolved wait description. Every caller — top level, sub-workflow body,
// sequential loop body, parallel loop body — resolves the same way, so the
// title/timeout CEL and the "is this actually an approval node" check live here
// rather than being restated at each call site.
func approvalExecutionFromNode(
	node *reliantv1.Node,
	nodeOutputs map[string]interface{},
	workflowInputs map[string]interface{},
	workflowID string,
	workflowName string,
	iterCtx map[string]interface{},
	loopOutputs map[string]interface{},
	execContext *ExecutionContext,
	chatID string,
	loopNodeID string,
	loopIteration int,
	nodePath string,
	logger log.Logger,
) (approvalExecution, error) {
	evalResult, err := EvaluateNodeConfig(
		node, nodeOutputs, workflowID, workflowName,
		workflowInputs, iterCtx, loopOutputs, execContext,
	)
	if err != nil {
		return approvalExecution{}, fmt.Errorf("failed to evaluate approval node config: %w", err)
	}

	args := model.GetApprovalArgs(evalResult)
	if args == nil {
		return approvalExecution{}, fmt.Errorf("expected approval node, got %s", model.NodeType(node))
	}

	threadID := ""
	if execContext != nil {
		threadID = execContext.Thread
	}

	return approvalExecution{
		ChatID:        chatID,
		WorkflowID:    workflowID,
		ThreadID:      threadID,
		StepID:        node.GetId(),
		LoopNodeID:    loopNodeID,
		LoopIteration: loopIteration,
		NodePath:      nodePath,
		Title:         model.CelStringValue(args.GetTitle()),
		TimeoutStr:    model.CelStringValue(args.GetTimeout()),
		Logger:        logger,
	}, nil
}

// executeApprovalSignalFlow creates an approval record via the ApprovalCreate
// activity and then blocks on a Temporal signal from the gRPC approval service,
// or on a timeout, whichever comes first.
//
// IDENTITY. Each wait must be addressable on its own, or one loop iteration's
// approval could be satisfied by another's signal. Two things make that true,
// and neither depends on a name this package builds:
//
//   - The approval record's idempotency key is
//     "<temporal workflow id>:<temporal activity id>" (ApprovalCreateActivity),
//     and Temporal assigns a fresh, deterministic activity id to every
//     ExecuteActivity in a workflow execution — including ones issued from
//     separate workflow.Go goroutines, as parallel loop iterations are. So a
//     second iteration reaching the same node scheduling a second
//     ApprovalCreate gets a distinct entity id and therefore a distinct row.
//   - The signal channel is named for the resulting approval id, a per-row
//     UUID, so a signal for iteration 0 cannot wake iteration 1.
//
// Replay safety comes from the same key: on replay the activity id is
// reproduced, the existing row is found, and a resolved one short-circuits the
// wait instead of creating a duplicate.
func executeApprovalSignalFlow(ctx workflow.Context, input approvalExecution) (map[string]interface{}, error) {
	timeout := defaultApprovalTimeout
	if input.TimeoutStr != "" {
		if parsed, parseErr := time.ParseDuration(input.TimeoutStr); parseErr == nil {
			timeout = parsed
		}
	}

	temporalWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	createCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	createInput := map[string]interface{}{
		"chat_id":              input.ChatID,
		"workflow_id":          input.WorkflowID,
		"temporal_workflow_id": temporalWorkflowID,
		"thread_id":            input.ThreadID,
		"step_id":              input.StepID,
		"loop_node_id":         input.LoopNodeID,
		"loop_iteration":       input.LoopIteration,
		"node_path":            input.NodePath,
		"title":                input.Title,
		"timeout":              input.TimeoutStr,
	}

	var createOutput struct {
		ApprovalID      string `json:"approval_id"`
		AlreadyResolved bool   `json:"already_resolved"`
		Status          string `json:"status"`
		ActionTaken     string `json:"action_taken"`
	}
	if err := workflow.ExecuteActivity(createCtx, "ApprovalCreate", createInput).Get(ctx, &createOutput); err != nil {
		return nil, fmt.Errorf("ApprovalCreate activity failed: %w", err)
	}

	// Already resolved (idempotency on replay): return without waiting.
	if createOutput.AlreadyResolved {
		return map[string]interface{}{
			"approval_id":  createOutput.ApprovalID,
			"status":       createOutput.Status,
			"action_taken": createOutput.ActionTaken,
		}, nil
	}

	signalName := "signal.approval." + createOutput.ApprovalID
	signalCh := workflow.GetSignalChannel(ctx, signalName)

	timeoutCtx, cancelTimer := workflow.WithCancel(ctx)
	timeoutFuture := workflow.NewTimer(timeoutCtx, timeout)

	selector := workflow.NewSelector(ctx)

	var status, actionTaken, denialReason string

	selector.AddReceive(signalCh, func(ch workflow.ReceiveChannel, more bool) {
		var signalData map[string]interface{}
		ch.Receive(ctx, &signalData)
		if s, ok := signalData["status"].(string); ok {
			status = s
		} else {
			status = "approved" // default if signal data is missing status
		}
		if at, ok := signalData["action_taken"].(string); ok {
			actionTaken = at
		}
		if dr, ok := signalData["denial_reason"].(string); ok {
			denialReason = dr
		}
		cancelTimer()
	})

	selector.AddFuture(timeoutFuture, func(f workflow.Future) {
		status = "timeout"
	})

	selector.Select(ctx)

	if status == "timeout" {
		resolveCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				MaximumAttempts: 3,
			},
		})
		resolveInput := map[string]interface{}{
			"approval_id": createOutput.ApprovalID,
			"status":      "timeout",
		}
		var resolveOutput map[string]interface{}
		if err := workflow.ExecuteActivity(resolveCtx, "ApprovalResolve", resolveInput).Get(ctx, &resolveOutput); err != nil {
			input.Logger.Warn("[Approval] Failed to resolve approval as timeout in DB",
				"stepID", input.StepID,
				"approvalID", createOutput.ApprovalID,
				"error", err,
			)
		}
	}

	output := map[string]interface{}{
		"approval_id":  createOutput.ApprovalID,
		"status":       status,
		"action_taken": actionTaken,
	}
	if denialReason != "" {
		output["denial_reason"] = denialReason
	}

	return output, nil
}
