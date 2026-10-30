package runtime

import (
	"fmt"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/workflow"
)

// No policy or filesystem reads occur in workflow code. The preview activity
// validates and snapshots the effective activation before any card is created.
func executeWithActivationApprovals(ctx, activityCtx workflow.Context, rtx types.RuntimeContext, node *pb.Node, unattended bool) workflow.Future {
	var calls []*pb.ToolCallMsg
	for _, tc := range node.GetExecuteTools().GetResolvedToolCalls() {
		if !isActivationCall(tc.GetName()) {
			continue
		}
		// Never ask for a call the model was not actually offered.
		caps := node.GetExecuteTools().GetCapabilities()
		if caps == nil {
			continue
		}
		offered := false
		for _, n := range caps.GetOffered() {
			if n == tc.GetName() {
				offered = true
				break
			}
		}
		if offered {
			calls = append(calls, tc)
		}
	}
	rtx.Unattended = unattended
	if len(calls) == 0 {
		return workflow.ExecuteActivity(activityCtx, "ExecuteTools", types.ActivityInput{Runtime: rtx, Node: node})
	}
	future, set := workflow.NewFuture(ctx)
	workflow.Go(activityCtx, func(gctx workflow.Context) {
		// Blocking APIs must use this coroutine's context, not its parent's.
		activityCtx = gctx
		rtx.Unattended = unattended
		rtx.ActivationApprovals = map[string]string{}
		rtx.ActivationRefusals = map[string]string{}
		for _, tc := range calls {
			previewCtx := workflow.WithActivityOptions(activityCtx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
			var preview types.AutomationApprovalPreview
			err := workflow.ExecuteActivity(previewCtx, "AutomationApprovalPreview", types.AutomationApprovalInput{ChatID: rtx.ChatID, Thread: rtx.Thread, ToolName: tc.GetName(), ToolInput: tc.GetInput(), ToolCallID: tc.GetId(), Unattended: unattended, Capabilities: node.GetExecuteTools().GetCapabilities()}).Get(activityCtx, &preview)
			if err != nil {
				rtx.ActivationRefusals[tc.GetId()] = "Could not validate the activation for approval; nothing changed."
				continue
			}
			if preview.Refusal != "" {
				rtx.ActivationRefusals[tc.GetId()] = preview.Refusal
				continue
			}
			if preview.NoApproval {
				// The tool judged this call card-free (manage_machine's
				// stop, or an unattended start of the activation's own
				// machine); its execution re-checks the policy.
				continue
			}
			if unattended {
				rtx.ActivationRefusals[tc.GetId()] = "Unattended runs cannot manage activations."
				continue
			}
			output, err := executeApprovalSignalFlow(activityCtx, approvalExecution{ChatID: rtx.ChatID, WorkflowID: rtx.WorkflowID, ThreadID: rtx.Thread, StepID: rtx.StepID, LoopNodeID: rtx.LoopNodeID, LoopIteration: rtx.LoopIteration, NodePath: rtx.NodePath, Title: preview.Title, Logger: workflow.GetLogger(gctx)})
			if err != nil || activityCtx.Err() != nil {
				if id, _ := output["approval_id"].(string); id != "" && activityCtx.Err() != nil {
					disconnected, _ := workflow.NewDisconnectedContext(gctx)
					closeCtx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
					_ = workflow.ExecuteActivity(closeCtx, "ApprovalResolve", map[string]interface{}{"approval_id": id, "status": "interrupted"}).Get(closeCtx, nil)
				}
				rtx.ActivationRefusals[tc.GetId()] = "Approval could not be completed; nothing changed."
				continue
			}
			if output["status"] != "approved" {
				rtx.ActivationRefusals[tc.GetId()] = fmt.Sprintf("The user did not approve %s; nothing changed. Do not attempt it another way.", tc.GetName())
				continue
			}
			rtx.ActivationApprovals[tc.GetId()] = preview.Hash
		}
		var result map[string]interface{}
		err := workflow.ExecuteActivity(activityCtx, "ExecuteTools", types.ActivityInput{Runtime: rtx, Node: node}).Get(gctx, &result)
		set.Set(result, err)
	})
	return future
}
func isActivationCall(name string) bool {
	switch name {
	case names.ToolCreateTriggerActivation, names.ToolUpdateTriggerActivation, names.ToolEnableTrigger, names.ToolPauseTrigger,
		names.ToolManageMachine:
		return true
	}
	return false
}
