package runtime

import (
	"fmt"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/workflow"
)

// activationApprovalBatchChangeID gates asking for every card a turn's tool
// calls need at once, as one batch. Histories recorded before it previewed a
// call, asked for its card and waited on it before previewing the next, and
// replay through activationApprovalAsk.oneByOne.
const activationApprovalBatchChangeID = "activation-approvals-batched"

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
		rtx.Unattended = unattended
		rtx.ActivationApprovals = map[string]string{}
		rtx.ActivationRefusals = map[string]string{}
		ask := activationApprovalAsk{rtx: &rtx, capabilities: node.GetExecuteTools().GetCapabilities(), unattended: unattended}
		if workflow.GetVersion(gctx, activationApprovalBatchChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
			ask.oneByOne(gctx, calls)
		} else {
			ask.together(gctx, calls)
		}
		var result map[string]interface{}
		err := workflow.ExecuteActivity(gctx, "ExecuteTools", types.ActivityInput{Runtime: rtx, Node: node}).Get(gctx, &result)
		set.Set(result, err)
	})
	return future
}

// activationApprovalAsk previews a turn's activation calls and asks the owner
// for the cards they need, recording each call's verdict on rtx: the approved
// hash ExecuteTools binds the call to, or the refusal it answers it with.
type activationApprovalAsk struct {
	rtx          *types.RuntimeContext
	capabilities *pb.ToolCapabilities
	unattended   bool
}

// activationCard is one call that needs the owner's approval, with its preview.
type activationCard struct {
	call    *pb.ToolCallMsg
	preview types.AutomationApprovalPreview
}

// preview validates one call. It returns the call's card when it needs one;
// otherwise the call is card-free or already refused.
func (a activationApprovalAsk) preview(gctx workflow.Context, tc *pb.ToolCallMsg) (activationCard, bool) {
	previewCtx := workflow.WithActivityOptions(gctx, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
	var preview types.AutomationApprovalPreview
	err := workflow.ExecuteActivity(previewCtx, "AutomationApprovalPreview", types.AutomationApprovalInput{ChatID: a.rtx.ChatID, Thread: a.rtx.Thread, ToolName: tc.GetName(), ToolInput: tc.GetInput(), ToolCallID: tc.GetId(), Unattended: a.unattended, Capabilities: a.capabilities}).Get(gctx, &preview)
	switch {
	case err != nil:
		a.rtx.ActivationRefusals[tc.GetId()] = "Could not validate the activation for approval; nothing changed."
	case preview.Refusal != "":
		a.rtx.ActivationRefusals[tc.GetId()] = preview.Refusal
	case preview.NoApproval:
		// The tool judged this call card-free (manage_machine's stop, an
		// unattended start of the activation's own machine, a pause, or an
		// activation change the owner's automation-approvals setting allows
		// without a card); its execution re-checks the policy.
	case a.unattended:
		a.rtx.ActivationRefusals[tc.GetId()] = "This change needs the owner's approval card, and an unattended run cannot show one; nothing changed."
	default:
		return activationCard{call: tc, preview: preview}, true
	}
	return activationCard{}, false
}

// wait asks for one card and waits for the owner's decision. batchID groups
// it with the other cards of its turn; empty when it is the only one.
func (a activationApprovalAsk) wait(gctx workflow.Context, card activationCard, batchID string) (map[string]interface{}, error) {
	rtx := a.rtx
	return executeApprovalSignalFlow(gctx, approvalExecution{ChatID: rtx.ChatID, WorkflowID: rtx.WorkflowID, ThreadID: rtx.Thread, StepID: rtx.StepID, LoopNodeID: rtx.LoopNodeID, LoopIteration: rtx.LoopIteration, NodePath: rtx.NodePath, Title: card.preview.Title, ToolCallID: card.call.GetId(), BatchID: batchID, Logger: workflow.GetLogger(gctx)})
}

// settle records the owner's decision on one card.
func (a activationApprovalAsk) settle(gctx workflow.Context, card activationCard, output map[string]interface{}, err error) {
	id := card.call.GetId()
	if err != nil || gctx.Err() != nil {
		if approvalID, _ := output["approval_id"].(string); approvalID != "" && gctx.Err() != nil {
			disconnected, _ := workflow.NewDisconnectedContext(gctx)
			closeCtx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
			_ = workflow.ExecuteActivity(closeCtx, "ApprovalResolve", map[string]interface{}{"approval_id": approvalID, "status": "interrupted"}).Get(closeCtx, nil)
		}
		a.rtx.ActivationRefusals[id] = "Approval could not be completed; nothing changed."
		return
	}
	if output["status"] != "approved" {
		a.rtx.ActivationRefusals[id] = fmt.Sprintf("The user did not approve %s; nothing changed. Do not attempt it another way.", card.call.GetName())
		return
	}
	a.rtx.ActivationApprovals[id] = card.preview.Hash
}

// together previews every call first, then asks for all the cards the turn
// needs at once and waits for every decision. Cards raised together carry
// one batch id, so the chat and the Inbox offer them as a single decision
// (each card can still be decided on its own).
func (a activationApprovalAsk) together(gctx workflow.Context, calls []*pb.ToolCallMsg) {
	var cards []activationCard
	for _, tc := range calls {
		if card, ok := a.preview(gctx, tc); ok {
			cards = append(cards, card)
		}
	}
	if len(cards) == 0 {
		return
	}
	batchID := ""
	if len(cards) > 1 {
		// Deterministic and unique: tool call ids are unique within a run,
		// and the execution id scopes them across runs.
		batchID = workflow.GetInfo(gctx).WorkflowExecution.ID + "/" + cards[0].call.GetId()
	}
	outputs := make([]map[string]interface{}, len(cards))
	errs := make([]error, len(cards))
	wg := workflow.NewWaitGroup(gctx)
	for i, card := range cards {
		wg.Add(1)
		workflow.Go(gctx, func(cctx workflow.Context) {
			defer wg.Done()
			outputs[i], errs[i] = a.wait(cctx, card, batchID)
		})
	}
	wg.Wait(gctx)
	for i, card := range cards {
		a.settle(gctx, card, outputs[i], errs[i])
	}
}

// oneByOne is how runs recorded before activationApprovalBatchChangeID ask:
// preview a call, ask for its card and wait, then the next. Delete it once
// no history recorded before that gate can still be in flight.
func (a activationApprovalAsk) oneByOne(gctx workflow.Context, calls []*pb.ToolCallMsg) {
	for _, tc := range calls {
		card, ok := a.preview(gctx, tc)
		if !ok {
			continue
		}
		output, err := a.wait(gctx, card, "")
		a.settle(gctx, card, output, err)
	}
}

func isActivationCall(name string) bool {
	switch name {
	case names.ToolCreateTriggerActivation, names.ToolUpdateTriggerActivation, names.ToolEnableTrigger, names.ToolPauseTrigger,
		names.ToolManageMachine:
		return true
	}
	return false
}
