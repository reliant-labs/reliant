// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"slices"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// The integration action approval prompt, kept only so the runs that recorded
// it keep replaying.
//
// Until it was removed, a call to an integration action that changes something
// outside Reliant (posting to Slack, sending an email, an HTTP request) did not
// run on the model's say-so in a run a person was attending. The call_llm turn
// listed those calls in ToolCapabilities.approval_required, and the batch first
// asked about each one through executeApprovalSignalFlow — ApprovalCreate, a
// timer, a signal or ApprovalResolve — and only then started ExecuteTools,
// with every call that was not approved refused (ExecuteToolsArgs.
// refused_tool_calls). Now a batch starts at once.
//
// Nothing writes approval_required any more, so only a turn recorded before
// the removal can reach this file, and only one recorded without the version
// marker takes it. Retire it — with both deprecated proto fields and
// frozen/2026-10-11-integration-action-approval — once no run that old can
// still be open.

// actionApprovalRemovedChangeID gates the removal of the integration action
// approval prompt (workflow.GetVersion).
//
// It is consulted only where the two shapes first differ: a batch holding a
// call that its turn's recorded capability set says must ask first. Such a
// turn was recorded before the removal, and if the worker that recorded it
// also scheduled the batch, the history holds the approval's commands and no
// marker, so it replays them — and a run still waiting on one of those
// approvals keeps waiting and answers the call the way it always did (as does
// any later such batch in that execution: GetVersion answers once per run). A
// turn recorded before the removal whose batch this code schedules first
// records the marker and runs the batch at once. A run with no such turn never
// consults the gate, so it records no marker.
const actionApprovalRemovedChangeID = "integration-action-approval-removed"

// legacyActionApprovalGate returns the calls in a batch that must still ask
// before they run: those its turn's recorded capability set listed, when the
// run recorded that turn's batch before the prompt was removed. Nil for every
// other batch.
func legacyActionApprovalGate(ctx workflow.Context, calls []*reliantv1.ToolCallMsg, caps *reliantv1.ToolCapabilities) []*reliantv1.ToolCallMsg {
	gated := legacyActionApprovalCalls(calls, caps)
	if len(gated) == 0 {
		return nil
	}
	if workflow.GetVersion(ctx, actionApprovalRemovedChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		return nil
	}
	return gated
}

// legacyActionApprovalCalls are the calls in a batch that the turn's recorded
// capability set says asked before they ran. A call it did not offer at all is
// not among them: the ExecuteTools activity refuses it.
func legacyActionApprovalCalls(calls []*reliantv1.ToolCallMsg, caps *reliantv1.ToolCapabilities) []*reliantv1.ToolCallMsg {
	required := caps.GetApprovalRequired() //nolint:staticcheck // SA1019: read only to replay runs recorded before the prompt was removed
	if len(required) == 0 {
		return nil
	}
	var gated []*reliantv1.ToolCallMsg
	for _, tc := range calls {
		if slices.Contains(required, tc.GetName()) {
			gated = append(gated, tc)
		}
	}
	return gated
}

// afterActionApprovals asks about every gated call at once — one approval
// each, answered in any order — then starts the batch with run, handing it the
// calls that were not approved as refusals. The returned future resolves to
// that batch's own result, so the batch's output and who saves it are what
// they would have been without the gate.
//
// The asks wait on activityCtx, the context the batch itself runs on, so an
// interrupt or pause that would cancel the batch also ends the wait: the batch
// then starts on a cancelled context and fails the way any interrupted batch
// does, instead of the turn sitting on an approval nobody will answer.
func afterActionApprovals(
	ctx workflow.Context,
	activityCtx workflow.Context,
	rtx types.RuntimeContext,
	gated []*reliantv1.ToolCallMsg,
	run func(refused map[string]string) workflow.Future,
) workflow.Future {
	future, settable := workflow.NewFuture(ctx)
	workflow.Go(ctx, func(gCtx workflow.Context) {
		logger := workflow.GetLogger(gCtx)
		refusals := make([]string, len(gated))
		wg := workflow.NewWaitGroup(gCtx)
		for i, tc := range gated {
			i, tc := i, tc
			wg.Add(1)
			workflow.Go(activityCtx, func(askCtx workflow.Context) {
				defer wg.Done()
				refusals[i] = askActionApproval(askCtx, rtx, tc, logger)
			})
		}
		wg.Wait(gCtx)

		refused := make(map[string]string)
		for i, tc := range gated {
			if refusals[i] != "" {
				refused[tc.GetId()] = refusals[i]
			}
		}
		var result map[string]interface{}
		if err := run(refused).Get(gCtx, &result); err != nil {
			settable.SetError(err)
			return
		}
		settable.SetValue(result)
	})
	return future
}

// askActionApproval asks the person attending about one call, and returns why
// it may not run, or "" when they approved it.
func askActionApproval(ctx workflow.Context, rtx types.RuntimeContext, tc *reliantv1.ToolCallMsg, logger log.Logger) string {
	name := tc.GetName()
	output, err := executeApprovalSignalFlow(ctx, approvalExecution{
		ChatID:        rtx.ChatID,
		WorkflowID:    rtx.WorkflowID,
		ThreadID:      rtx.Thread,
		StepID:        rtx.StepID,
		LoopNodeID:    rtx.LoopNodeID,
		LoopIteration: rtx.LoopIteration,
		NodePath:      rtx.NodePath,
		// Only an ApprovalCreate that first runs after the removal reads it;
		// a replayed one is matched by type, never by input.
		Title:  fmt.Sprintf("Run %s?", name),
		Logger: logger,
	})
	if err != nil {
		// Nobody could be asked, so nobody approved: refuse rather than fail
		// the turn, and the conversation carries on.
		logger.Warn("[ActionApproval] Could not ask for approval", "tool", name, "toolCallID", tc.GetId(), "error", err)
		return fmt.Sprintf("Approval for %s could not be requested, so it was not run.", name)
	}
	if ctx.Err() != nil {
		// Interrupted or paused while the approval was up. The flow could not
		// resolve its row on the cancelled context, so close it here, or it
		// would still be asking about a call that will never run.
		if approvalID, _ := output["approval_id"].(string); approvalID != "" {
			disconnected, _ := workflow.NewDisconnectedContext(ctx)
			resolveCtx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{
				StartToCloseTimeout: 30 * time.Second,
				RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
			})
			if err := workflow.ExecuteActivity(resolveCtx, "ApprovalResolve", map[string]interface{}{
				"approval_id": approvalID,
				"status":      "interrupted",
			}).Get(resolveCtx, nil); err != nil {
				logger.Warn("[ActionApproval] Could not close the interrupted approval", "approvalID", approvalID, "error", err)
			}
		}
		return fmt.Sprintf("The run was interrupted before the user approved %s, so it was not run.", name)
	}
	switch status, _ := output["status"].(string); status {
	case "approved":
		logger.Info("[ActionApproval] Approved", "tool", name, "toolCallID", tc.GetId(), "actionTaken", output["action_taken"])
		return ""
	case "timeout":
		return fmt.Sprintf("The user did not approve %s in time, so it was not run. Do not attempt it another way.", name)
	default:
		return fmt.Sprintf("The user did not approve %s, so it was not run. Do not attempt it another way.", name)
	}
}
