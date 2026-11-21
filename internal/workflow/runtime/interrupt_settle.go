package runtime

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/workflow"
)

// interruptSettlesToolStepChangeID gates settling an execute_tools step a
// same-thread interrupt cancelled, instead of dispatching it again.
//
// Before it, every executor's cancel handler treated an interrupt like a
// pause: wait for any resume, then re-dispatch the same step. For calls with a
// terminal tool_calls row that was harmless (InterruptThread records them
// Cancelled, and the re-dispatch returns that), but a batch cancelled before
// any call had a row — an activation call still in its approval preview, or
// waiting on the owner's card — ran its preview again, raised its card again,
// and never reached the call_llm that delivers the mailbox. Prod chat 17037094
// (2026-11-20) re-dispatched one update_trigger_activation batch on every one
// of four interrupts.
//
// Histories recorded before it re-dispatched the batch (previews, cards and
// all) and replay so: frozen fixture set
// 2026-11-20-interrupt-redispatches-tool-step.
const interruptSettlesToolStepChangeID = "interrupt-settles-tool-step"

// cancelCause names why a step's dispatch was cancelled, for the log:
// "interrupt" when its thread was interrupted since the step was dispatched,
// "pause" otherwise. The two arrive as the same CanceledError.
func (e *StepExecutor) cancelCause(running *RunningStep) string {
	if running != nil && e.threadInterruptedSince(running.ThreadInterruptEpoch) {
		return "interrupt"
	}
	return "pause"
}

// noteCancelled records, for a step a cancellation ended, what its
// re-dispatch must do. Every executor's cancel handler (workflow.go,
// loop_executor.go, inline_workflow_executor.go) calls it before
// re-Start()ing the step, so the three share one decision:
//
//   - a pause re-runs the step as it was (a call_llm continues its cut-off
//     partial: noteCancelledForPause);
//   - an interrupt settles an execute_tools step (noteInterruptedTurn): its
//     re-dispatch runs none of the batch's calls, answers each one, and the
//     loop moves on to the call_llm that delivers the message the user
//     interrupted to send. Any other step re-runs, which for call_llm (the
//     only other step an interrupt reaches in an agent loop) is what
//     delivers that message.
func (e *StepExecutor) noteCancelled(running *RunningStep) {
	if e.cancelCause(running) == "pause" {
		e.noteCancelledForPause(running)
		return
	}
	e.noteInterruptedTurn(running)
}

// noteInterruptedTurn marks an execute_tools step an interrupt cancelled, so
// its re-dispatch settles the batch (takeInterruptedTurn). The version gate is
// asked here, on the one path where the two shapes differ.
func (e *StepExecutor) noteInterruptedTurn(running *RunningStep) {
	if model.NodeType(running.Node) != model.NodeTypeExecuteTools {
		return
	}
	if workflow.GetVersion(e.ctx, interruptSettlesToolStepChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return
	}
	if e.interruptedTurns == nil {
		e.interruptedTurns = make(map[string]bool)
	}
	e.interruptedTurns[running.Node.GetId()] = true
}

// takeInterruptedTurn reports whether noteInterruptedTurn marked nodeID, and
// clears the mark: it describes exactly one re-dispatch.
func (e *StepExecutor) takeInterruptedTurn(nodeID string) bool {
	if !e.interruptedTurns[nodeID] {
		return false
	}
	delete(e.interruptedTurns, nodeID)
	return true
}

// startSettleInterrupted dispatches the one ExecuteTools activity that
// settles an interrupted batch: every call of the turn, spawn and ask_user
// included, goes to the activity with InterruptedTurn set, and the activity
// answers each from the durable tool_calls rows without running anything
// (ExecuteToolsActivity.settleInterruptedCall).
//
// Nothing else the batch normally does runs: no activation preview, no
// approval card, no spawn admission, no machine wake. Those are the start of
// new work, which the interrupt asked to stop. The activity's output is the
// whole result, so it writes the node's save_message itself — for a batch
// whose result the workflow would otherwise assemble as well.
//
// Why the activity answers the calls rather than this workflow code: whether
// a call already finished is in the database (the API's InterruptThread, and
// the cancelled dispatch itself, record outcomes there), which workflow code
// may not read; the activity reads it, keeps a finished call's real result,
// and records every other call Cancelled with the result the model is shown,
// so the transcript, the tool_calls rows and the UI agree.
func (e *StepExecutor) startSettleInterrupted(node, evalResult *reliantv1.Node, rtx types.RuntimeContext) (workflow.Future, string, bool) {
	rtx.InterruptedTurn = true
	rtx.SaveMessage = e.saveMessageRequest(node)
	e.logger.Info("[StepExecutor] Settling the tool batch an interrupt cancelled, without running it",
		"stepID", node.GetId(),
		"toolCalls", len(evalResult.GetExecuteTools().GetResolvedToolCalls()),
	)
	activityName := nodeTypeToActivityName(node.GetType())
	future := workflow.ExecuteActivity(e.activityOptions(node), activityName, types.ActivityInput{Runtime: rtx, Node: evalResult})
	return future, activityName, rtx.SaveMessage != nil
}
