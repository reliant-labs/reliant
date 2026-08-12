// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"

	"go.temporal.io/sdk/workflow"
)

// continueAsNewVersionGate is the workflow.GetVersion changeID for
// ContinueAsNew at the agent-loop iteration boundary. The check adds a new
// command sequence (and, when it fires, a CONTINUE_AS_NEW close command) at a
// point where pre-change histories recorded nothing, so replaying one with the
// check live would wedge it with a non-determinism error (TMPRL1100) — the
// exact incident class this feature exists to prevent. GetVersion returns
// DefaultVersion for those histories (check off) and 1 for new executions.
const continueAsNewVersionGate = "continue-as-new"

// Temporal terminates an execution that exceeds EITHER of two per-execution
// limits (server defaults, verified against the running 1.26.2 server, whose
// dynamic config is empty so stock values apply):
//
//	limit.historyCount.error  51,200 events
//	limit.historySize.error   50 MB
//
// Which one binds depends entirely on how big this run's payloads are, so the
// handoff thresholds below cover both. Measured density across real runs
// ranges from ~517 to ~1,157 bytes/event: at the low end the count binds first
// (50 MB would not arrive until ~101k events), at the high end the SIZE binds
// first (the cap arrives at ~45k events, before the count cap). A count-only
// threshold is therefore not safe on payload-heavy runs, which are exactly the
// long agent conversations this exists for.
//
// The thresholds are deliberately high. Every handoff costs the user something
// real — in-memory node outputs are dropped and conversation state is rebuilt
// from thread history — so continuing rarely is a feature, not a compromise.
// What bounds how high they can go is that the check is SKIPPED whenever the
// boundary is not quiescent (see readyToContinueAsNew): a run with a
// background spawn in flight keeps going until the spawn lands, and the
// headroom left after the threshold is what makes that wait safe. A turn costs
// a median of 37 events and a maximum of 64, measured over 73 turns of real
// history, so headroom is priced in worst-case turns.
//
// 40,000 events leaves 11,200 events — about 175 worst-case turns — before the
// count cap. 40 MB leaves 10 MB, which is 8,600 worst-case turns at the
// heaviest density observed. Both are far more than any quiescence wait should
// ever need, while cutting handoffs to roughly a third as often as a 13k-event
// threshold would.
const (
	continueAsNewEventThreshold = 40000
	continueAsNewSizeThreshold  = 40 * 1024 * 1024
)

// readyToContinueAsNew reports whether this run both NEEDS to continue as new
// and can do so safely at the current point.
//
// "Needs to" is the history-growth test against our OWN thresholds, on both
// the count and size axes.
//
// It deliberately ignores workflow.GetInfo(ctx).GetContinueAsNewSuggested(),
// Temporal's server-side hint, even though reading it looks like the obvious
// thing to do. The server raises that flag at limit.historyCount.
// suggestContinueAsNew / limit.historySize.suggestContinueAsNew, which default
// to 4,096 events / 4 MB — roughly a tenth of the real cap, and about 110
// agent turns. Honoring it would hand off constantly and make every long
// conversation pay the rebuild cost for no safety benefit, and because it is
// OR-ed against our own threshold it would also make that threshold dead code:
// the hint always trips first. Our numbers come from measured turn cost
// against the actual limits, so they are the ones to use.
//
// Both inputs are replay-deterministic despite being server-supplied: the SDK
// sets them from each WORKFLOW_TASK_STARTED event as it is replayed
// (internal_event_handlers.go), so a replay re-derives the same value at the
// same point and takes the same branch.
//
// "Can safely" is the quiescence test, and it is the reason this is a function
// rather than an inline comparison:
//
//   - A background spawn (spec §4.3 `background: true`) is a goroutine inside
//     THIS execution, not a child workflow. Continuing as new ends the
//     execution and kills it mid-flight, leaving its tool_calls row stuck at
//     "backgrounded" forever. So a run with any live detached spawn — on any
//     thread, since the continuation ends all of them — waits for a later
//     boundary. The threshold's headroom is what makes waiting affordable.
//
//   - An armed pause means someone (or the daemon-offline breaker) has stopped
//     this run and a resume signal is the only thing that will restart it. A
//     continuation started here would drop that signal on the floor and come
//     back running, silently undoing the pause.
//
// Signals in flight are the accepted residual risk. A signal delivered after
// the CONTINUE_AS_NEW command is emitted but before the new run starts is
// lost, and there is no way to close that window from inside the workflow.
// Every channel this run listens on is either reconstructible from Postgres
// (questions are rows, resolved-on-replay via QuestionCreate's AlreadyResolved;
// conversation state is thread history) or carried forward explicitly in the
// continuation's input (update_workflow_state mutates input.Inputs in place,
// and that map is what we pass on). cancel_thread is per-spawn and transient,
// and quiescence guarantees there is no spawn to cancel. Losing a rare signal
// is strictly better than the alternative this replaces: a chat wedged at the
// history cap, where every message the user sends does nothing.
func readyToContinueAsNew(ctx workflow.Context, childTracker *ChildWorkflowTracker, pauseArmed bool) bool {
	if !quiescentForContinueAsNew(childTracker, pauseArmed) {
		return false
	}
	info := workflow.GetInfo(ctx)
	return historyNeedsContinueAsNew(info.GetCurrentHistoryLength(), info.GetCurrentHistorySize())
}

// quiescentForContinueAsNew reports whether the current boundary is a safe
// place to end this execution. See readyToContinueAsNew for why each condition
// disqualifies the boundary.
func quiescentForContinueAsNew(childTracker *ChildWorkflowTracker, pauseArmed bool) bool {
	if pauseArmed {
		return false
	}
	if childTracker != nil && len(childTracker.listLiveDetachedSpawns()) > 0 {
		return false
	}
	return true
}

// historyNeedsContinueAsNew reports whether the history has grown far enough
// on EITHER axis that this run should hand off. Split out from
// readyToContinueAsNew so the thresholds are testable without a workflow
// environment, whose history length and size are properties of the harness
// rather than something a test can set.
func historyNeedsContinueAsNew(historyLength, historySizeBytes int) bool {
	return historyLength >= continueAsNewEventThreshold ||
		historySizeBytes >= continueAsNewSizeThreshold
}

// isContinueAsNew reports whether err is (or wraps) the SDK's ContinueAsNew
// signal — a deliberate handoff to a successor run, not a failure.
//
// Two call sites depend on getting this right, and they fail in different
// ways. The loop's error path would otherwise wrap it as "loop step failed"
// and post that to the user's chat. handleWorkflowCompletion would otherwise
// mark the chat failed and run cleanup activities that tear down state the
// successor still needs.
func isContinueAsNew(err error) bool {
	var contErr *workflow.ContinueAsNewError
	return errors.As(err, &contErr)
}

// newContinueAsNewError builds the error that ends this execution and starts a
// fresh one under the same workflow ID with empty history.
//
// The continuation is expressed as a RESUME, reusing the machinery the coarse
// fresh-restart path already relies on: it re-enters at the given node and, for
// a loop node, at the given iteration, and reads conversation state from thread
// history rather than from replayed workflow state. That is the whole reason
// this is a small change — position checkpoints already record exactly
// {node_id, loop_iteration}, and WorkflowInput.Resume is already the shape that
// carries them.
//
// What crosses the boundary is therefore deliberately narrow: the chat and
// workflow identity, the CURRENT inputs map (so values applied by
// update_workflow_state signals survive), the execution context, and the
// position. Node outputs from the predecessor are NOT carried — same as any
// resume — so templated cross-node references must tolerate absence, which the
// has()-guard execution-order validation already enforces.
func newContinueAsNewError(ctx workflow.Context, input WorkflowInput, nodeID string, iteration int) error {
	return workflow.NewContinueAsNewError(ctx, DynamicWorkflow, WorkflowInput{
		ChatID:       input.ChatID,
		WorkflowName: input.WorkflowName,
		Inputs:       input.Inputs,
		ExecContext:  input.ExecContext,
		Resume: &ResumeInput{
			NodeID:        nodeID,
			LoopIteration: iteration,
		},
	})
}
