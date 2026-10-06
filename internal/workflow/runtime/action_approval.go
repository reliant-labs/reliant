// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// The action approval gate. In a run a person is attending, an integration
// action that changes something outside Reliant (posting to Slack, sending an
// email or a text, opening or commenting on an issue, an HTTP request) does
// not run on the model's say-so. Each such call first goes through
// executeApprovalSignalFlow — the approval node's own row-card-signal flow —
// titled from the action and its parameters. Approved, it runs with the rest
// of its batch. Denied or unanswered, the ExecuteTools activity refuses it, so
// it is recorded FAILED with the reason like every other refusal.
//
// Which calls ask is DATA: the call_llm turn's capability set lists them
// (ToolCapabilities.approval_required — the offered mutating integration
// actions, and none when the run is unattended). "Always allow" is the user's
// standing decision for one action; ApprovalCreate reads it and resolves the
// approval without a card.
//
// The commands this adds — ApprovalCreate, a timer, maybe ApprovalResolve —
// are issued only for a batch holding such a call, so a history without one
// replays exactly as before.

// actionApprovalCalls are the calls in a batch that ask before they run: those
// the turn's capability set says require approval. A call it does not offer
// at all is not among them — the ExecuteTools activity refuses it, and a card
// for a call that cannot run would be noise.
func actionApprovalCalls(calls []*reliantv1.ToolCallMsg, caps *reliantv1.ToolCapabilities) []*reliantv1.ToolCallMsg {
	required := caps.GetApprovalRequired()
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

// afterActionApprovals asks about every gated call at once — one card each,
// answered in any order — then starts the batch with run, handing it the
// calls that were not approved as refusals. The returned future resolves to
// that batch's own result, so the batch's output and who saves it are what
// they would have been without the gate.
//
// The asks wait on activityCtx, the context the batch itself runs on, so an
// interrupt or pause that would cancel the batch also ends the wait: the batch
// then starts on a cancelled context and fails the way any interrupted batch
// does, instead of the turn sitting on a card nobody will answer.
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
// it may not run, or "" when they approved it (or always allow it).
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
		ToolName:      name,
		ToolCallID:    tc.GetId(),
		ToolInput:     unwrapToolMetaInput(tc.GetInput()),
		Logger:        logger,
	})
	if err != nil {
		// Nobody could be asked, so nobody approved: refuse rather than fail
		// the turn, and the conversation carries on.
		logger.Warn("[ActionApproval] Could not ask for approval", "tool", name, "toolCallID", tc.GetId(), "error", err)
		return fmt.Sprintf("Approval for %s could not be requested, so it was not run.", name)
	}
	if ctx.Err() != nil {
		// Interrupted or paused while the card was up. The flow could not
		// resolve its row on the cancelled context, so close it here, or the
		// card would still be asking about a call that will never run.
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

// unwrapToolMetaInput is the arguments the model wrote, out of the metadata
// envelope ({"input": "<raw>", "__reliant_tool_meta__": {...}}) when a call
// carries one.
func unwrapToolMetaInput(input string) string {
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(input), &envelope); err != nil {
		return input
	}
	if _, hasMeta := envelope["__reliant_tool_meta__"]; hasMeta {
		if inner, ok := envelope["input"].(string); ok {
			return inner
		}
	}
	return input
}
