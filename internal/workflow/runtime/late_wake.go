// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"maps"

	"go.temporal.io/sdk/workflow"
)

// A user message that reaches a thread while its run is finishing must never
// be stranded: either this run gives it a turn, or a run is started for it.
//
// Both ways a user reaches a running thread — SendMessage and the composer's
// SendAgentMessage — queue the message as a mailbox row and then ring the
// thread-wake doorbell (ReasonMailbox). SendMessage queues rather than writing
// into history because the thread may be mid-turn, and a history row written
// then is ordered BEFORE the in-flight reply: history ends with the assistant
// and the next turn yields instead of answering. The doorbell is therefore the
// run's only record that there is input no turn has read, and every place the
// run could stop must consult it:
//
//   - the loop-exit gate, when the wake landed during the last turn
//     (awaitLiveDetachedSpawnsOrHandoff, both of its branches);
//   - the end of the run, when it landed after the gate let the loop exit —
//     while the graph wound down or the completion bookkeeping ran
//     (continueAsNewForLateWake);
//   - a continue-as-new handoff taken while parked on background spawns,
//     whose successor would otherwise wait on those spawns before its first
//     turn (newContinueAsNewError).
//
// A signal that arrives while the run's FINAL workflow task is in flight is
// not lost either: Temporal refuses to complete or continue-as-new a workflow
// with events it has not yet seen (WORKFLOW_TASK_FAILED_CAUSE_UNHANDLED_COMMAND,
// "the Workflow attempted to close itself without handling the new Events",
// docs.temporal.io/references/errors), fails that task and hands the worker a
// new one that includes the signal. The run's code then reaches the end-of-run
// check again with the wake counted.
//
// What reaches the run after it has closed is the send path's problem, and it
// handles it: a wake that reaches no run makes the sender start one for the
// input instead (ChatService.wakeLiveRun in chat_send.go). And the
// reconciler's orphaned-mailbox sweep leaves a thread alone while its
// workflow is open, so a row in flight to a successor is not marked
// undelivered first.

// lateUserWakeChangeID versions every place a late wake now buys a turn
// (workflow.GetVersion). Histories recorded before it exited, completed, or
// re-parked there; they must replay doing the same.
const lateUserWakeChangeID = "late-user-wake-gets-a-turn"

// continueAsNewForLateWake is the end-of-run check: called after a ROOT run's
// normal-completion bookkeeping, as the last thing before the workflow
// returns. If thread was woken after its last turn began, it returns the
// error that continues this execution as a fresh run, which takes the turn the
// wake asked for. Otherwise nil, and the run completes.
//
// The successor is exactly the run SendMessage would start for a message that
// arrived a moment later, after this one had completed: the same workflow at
// graph entry, on the same thread, reading the conversation from history. Its
// first call_llm drains the mailbox and reads history, so it answers the user
// message and delivers the queued row alike — and which side of the finish
// line the message landed on does not change what happens to it.
func continueAsNewForLateWake(ctx workflow.Context, input WorkflowInput, thread string, tracker *ChildWorkflowTracker) error {
	if tracker == nil || thread == "" {
		return nil
	}
	// The doorbell is counted by setupThreadWakeHandler's coroutine, and a
	// signal delivered in the same workflow task as the activity this run
	// just finished awaiting is handed to that coroutine without it having
	// RUN yet: Temporal's channel gives the value straight to a blocked
	// receiver, and the dispatcher resumes this (root) coroutine first. That
	// is precisely the retried final task after an UNHANDLED_COMMAND. Let it
	// run, then take anything still buffered, before reading the count.
	yieldToRunnableCoroutines(ctx)
	absorbPendingWakes(ctx, tracker)
	if !tracker.wakeSinceLastTurn(thread) {
		return nil
	}
	if workflow.GetVersion(ctx, lateUserWakeChangeID, workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		return nil
	}
	workflow.GetLogger(ctx).Info("[Workflow Runtime] Woken after its last turn; continuing as a fresh run so the input gets a turn",
		"chatID", input.ChatID,
		"thread", thread,
	)
	return workflow.NewContinueAsNewError(ctx, DynamicWorkflow, lateWakeSuccessorInput(input))
}

// lateWakeSuccessorInput is the fresh run started for a late wake: graph
// entry (no Resume), the current inputs, the same execution context and
// trigger. It is a run a person's reply started, not the chat's launch run,
// so the launch marker does not cross — the run that did the launch work has
// already reported its own finish.
func lateWakeSuccessorInput(input WorkflowInput) WorkflowInput {
	inputs := maps.Clone(input.Inputs)
	delete(inputs, InputKeyLaunchRun)
	return WorkflowInput{
		ChatID:       input.ChatID,
		WorkflowName: input.WorkflowName,
		Inputs:       inputs,
		ExecContext:  input.ExecContext,
		Trigger:      input.Trigger,
	}
}

// yieldToRunnableCoroutines lets every other coroutine that is ready to run
// in this workflow task do so before the caller continues. It issues no
// command.
//
// workflow.Await yields only while its condition is false, and the
// dispatcher re-checks it only after a pass in which some coroutine made
// progress — so the condition is set by a coroutine of its own. That
// coroutine is appended after every existing one, so by the time it runs,
// they have all had their turn in the same pass.
func yieldToRunnableCoroutines(ctx workflow.Context) {
	ran := false
	workflow.Go(ctx, func(workflow.Context) { ran = true })
	_ = workflow.Await(ctx, func() bool { return ran })
}

// absorbPendingWakes counts any thread-wake signal still buffered in its
// channel, the same way setupThreadWakeHandler would.
func absorbPendingWakes(ctx workflow.Context, tracker *ChildWorkflowTracker) {
	ch := workflow.GetSignalChannel(ctx, ThreadWakeSignalName)
	for {
		var sig ThreadWakeSignal
		if !ch.ReceiveAsync(&sig) {
			return
		}
		if sig.Thread != "" {
			tracker.notifyThreadWake(sig.Thread)
		}
	}
}
