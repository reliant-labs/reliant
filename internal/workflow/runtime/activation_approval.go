package runtime

import (
	"fmt"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// activationApprovalBatchChangeID gates asking for every card a turn's tool
// calls need at once, as one batch. Histories recorded before it previewed a
// call, asked for its card and waited on it before previewing the next, and
// replay through activationApprovalAsk.oneByOne.
const activationApprovalBatchChangeID = "activation-approvals-batched"

// connectionProposalChangeID gates settling propose_connection cards as
// connection proposals (settleConnection) and deduping identical ones in a
// turn. Histories recorded before it settled every card the generic way, and
// replay so.
const connectionProposalChangeID = "connection-proposal-approvals"

// activationPreviewBoundedChangeID gates bounding the approval preview: a
// longer per-attempt timeout and at most activationPreviewAttempts attempts,
// after which the call is refused ("could not validate"). Histories recorded
// before it previewed with a 30s timeout and the server's default retry
// policy, which retries without end: a preview that always outran its
// timeout held the agent's turn for hours (2026-11-20).
const activationPreviewBoundedChangeID = "activation-preview-bounded"

// activationPreviewAttempts bounds the preview's attempts. A preview that
// cannot finish fails into a refusal the model sees, instead of blocking it.
const activationPreviewAttempts = 3

// previewActivityOptions are the preview's options under
// activationPreviewBoundedChangeID.
func previewActivityOptions(gctx workflow.Context) workflow.ActivityOptions {
	if workflow.GetVersion(gctx, activationPreviewBoundedChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second}
	}
	return workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    activationPreviewAttempts,
		},
	}
}

// connectionProposalsEnabled reports whether this run settles connection
// proposals as such: the production gate.
func connectionProposalsEnabled(ctx workflow.Context) bool {
	return workflow.GetVersion(ctx, connectionProposalChangeID, workflow.DefaultVersion, 1) >= 1
}

// No policy or filesystem reads occur in workflow code. The preview activity
// validates and snapshots the effective activation before any card is created.
func executeWithActivationApprovals(ctx, activityCtx workflow.Context, rtx types.RuntimeContext, node *pb.Node, unattended bool) workflow.Future {
	return executeWithActivationApprovalsGated(ctx, activityCtx, rtx, node, unattended, connectionProposalsEnabled)
}

// executeWithActivationApprovalsGated is executeWithActivationApprovals with
// the connection-proposal gate supplied: tests replay a history recorded with
// the gate off (the code before connectionProposalChangeID) through the
// production gate.
func executeWithActivationApprovalsGated(ctx, activityCtx workflow.Context, rtx types.RuntimeContext, node *pb.Node, unattended bool, proposalsGate func(workflow.Context) bool) workflow.Future {
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
		ask.proposals = proposalsGate(gctx)
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
	// proposals: this run settles propose_connection cards as connection
	// proposals (connectionProposalChangeID).
	proposals bool
}

// isProposal reports whether card is a connection proposal to settle as one:
// its preview says so by kind, never by whether a payload happens to be set.
func (a activationApprovalAsk) isProposal(preview types.AutomationApprovalPreview) bool {
	return a.proposals && preview.Kind == types.PreviewKindConnectionProposal
}

// activationCard is one call that needs the owner's approval, with its preview.
type activationCard struct {
	call    *pb.ToolCallMsg
	preview types.AutomationApprovalPreview
}

// preview validates one call. It returns the call's card when it needs one;
// otherwise the call is card-free or already refused.
func (a activationApprovalAsk) preview(gctx workflow.Context, tc *pb.ToolCallMsg) (activationCard, bool) {
	previewCtx := workflow.WithActivityOptions(gctx, previewActivityOptions(gctx))
	var preview types.AutomationApprovalPreview
	err := workflow.ExecuteActivity(previewCtx, "AutomationApprovalPreview", types.AutomationApprovalInput{ChatID: a.rtx.ChatID, Thread: a.rtx.Thread, ToolName: tc.GetName(), ToolInput: tc.GetInput(), ToolCallID: tc.GetId(), Unattended: a.unattended, Capabilities: a.capabilities}).Get(gctx, &preview)
	switch {
	case err != nil:
		a.rtx.ActivationRefusals[tc.GetId()] = "Could not validate the activation for approval; nothing changed."
	case preview.Refusal != "":
		a.rtx.ActivationRefusals[tc.GetId()] = preview.Refusal
	case a.isProposal(preview) && preview.Connection == nil:
		workflow.GetLogger(gctx).Error("[Approval] connection proposal preview has no proposal; refusing the call", "toolCallID", tc.GetId())
		a.rtx.ActivationRefusals[tc.GetId()] = missingProposalRefusal
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
	return executeApprovalSignalFlow(gctx, approvalExecution{ChatID: rtx.ChatID, WorkflowID: rtx.WorkflowID, ThreadID: rtx.Thread, StepID: rtx.StepID, LoopNodeID: rtx.LoopNodeID, LoopIteration: rtx.LoopIteration, NodePath: rtx.NodePath, Title: card.preview.Title, ToolCallID: card.call.GetId(), ToolName: card.call.GetName(), BatchID: batchID, ConnectionProposal: a.cardProposal(card), Logger: workflow.GetLogger(gctx)})
}

// cardProposal is the proposal recorded on card's approval, when this run
// settles it as one.
func (a activationApprovalAsk) cardProposal(card activationCard) *core.ConnectionProposal {
	if !a.isProposal(card.preview) {
		return nil
	}
	return card.preview.Connection
}

// settle records the owner's decision on one card.
func (a activationApprovalAsk) settle(gctx workflow.Context, card activationCard, output map[string]interface{}, err error) {
	id := card.call.GetId()
	if a.isProposal(card.preview) {
		a.settleConnection(gctx, card, output, err)
		return
	}
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

// settleConnection records the person's answer to a propose_connection
// card. Approving it means they made the connection: its id is recorded for
// the call, which reads it back and answers the model with the status and
// identifiers only. Anything else answers the call with a status the model
// can act on: declined, or not completed.
func (a activationApprovalAsk) settleConnection(gctx workflow.Context, card activationCard, output map[string]interface{}, err error) {
	id := card.call.GetId()
	if card.preview.Connection == nil {
		// preview() refuses such a card before raising it; never settle one
		// as a generic approval, which would drop its connection.
		workflow.GetLogger(gctx).Error("[Approval] connection proposal card has no proposal; not settling it", "toolCallID", id)
		a.rtx.ActivationRefusals[id] = missingProposalRefusal
		return
	}
	name := card.preview.Connection.IntegrationName
	if name == "" {
		name = card.preview.Connection.IntegrationID
	}
	if err != nil || gctx.Err() != nil {
		if approvalID, _ := output["approval_id"].(string); approvalID != "" && gctx.Err() != nil {
			disconnected, _ := workflow.NewDisconnectedContext(gctx)
			closeCtx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Second})
			_ = workflow.ExecuteActivity(closeCtx, "ApprovalResolve", map[string]interface{}{"approval_id": approvalID, "status": "interrupted"}).Get(closeCtx, nil)
		}
		a.rtx.ActivationRefusals[id] = fmt.Sprintf("status: not completed. The card asking to connect %s could not be completed; no connection was made.", name)
		return
	}
	connectionID, _ := output["connection_id"].(string)
	switch status, _ := output["status"].(string); {
	case status == "approved" && connectionID != "":
		a.rtx.ActivationApprovals[id] = card.preview.Hash
		if a.rtx.ApprovedConnections == nil {
			a.rtx.ApprovedConnections = map[string]string{}
		}
		a.rtx.ApprovedConnections[id] = connectionID
	case status == "denied" && output["action_taken"] == connectionNotCompleted:
		a.rtx.ActivationRefusals[id] = fmt.Sprintf("status: not completed. The person closed the connection dialog without connecting %s; no connection was made. Do not propose it again unless they ask you to.", name)
	case status == "denied":
		a.rtx.ActivationRefusals[id] = fmt.Sprintf("status: declined. The person declined to connect %s; no connection was made. Do not propose it again unless they ask you to.", name)
	default:
		a.rtx.ActivationRefusals[id] = fmt.Sprintf("status: not completed. Nobody connected %s before the card closed; no connection was made.", name)
	}
}

// missingProposalRefusal answers a propose_connection call whose preview said
// it is a connection proposal but carried no proposal: a server fault.
const missingProposalRefusal = "status: not completed. The connection proposal could not be read on the server (its preview carried no proposal), so no card was raised and no connection was made. Report this as a Reliant bug; do not retry."

// connectionNotCompleted is the action_taken of a propose_connection card
// denied because the person closed the connection dialog without connecting
// (frontends/web ConnectionProposalCard).
const connectionNotCompleted = "not_completed"

// dedupeConnectionCards answers every propose_connection card of a turn that
// asks for the same thing as an earlier one (integration, scope and org) as
// a duplicate, and returns the rest: one card per proposal.
func (a activationApprovalAsk) dedupeConnectionCards(cards []activationCard) []activationCard {
	seen := map[string]bool{}
	kept := cards[:0:0]
	if !a.proposals {
		return cards
	}
	for _, card := range cards {
		if p := card.preview.Connection; a.isProposal(card.preview) && p != nil {
			if seen[p.Key()] {
				a.rtx.ActivationRefusals[card.call.GetId()] = fmt.Sprintf("status: already pending. An identical proposal to connect %s is on a card raised in this same turn; wait for the person's answer.", p.IntegrationName)
				continue
			}
			seen[p.Key()] = true
		}
		kept = append(kept, card)
	}
	return kept
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
	cards = a.dedupeConnectionCards(cards)
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
	case names.ToolCreateTriggerActivation, names.ToolUpdateTriggerActivation, names.ToolEnableTrigger, names.ToolPauseTrigger, names.ToolDeleteTrigger,
		names.ToolManageMachine, names.ToolManageMachinePool, names.ToolProposeConnection, names.ToolManageChange,
		// Sends an approved GTM email item (internal/gtm/email); every call
		// needs the owner's card.
		names.ToolSendGtmEmail:
		return true
	}
	return false
}
