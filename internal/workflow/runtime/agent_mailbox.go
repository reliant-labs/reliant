// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// drainAgentMessagesVersionGate is the workflow.GetVersion changeID for the
// mailbox drain call at the agent-loop step boundary. Histories recorded
// before this change must keep replaying WITHOUT the extra DrainAgentMessages
// activity command, or they wedge with TMPRL1100 on the next replay (see
// replaytest/fixtures/README.md).
//
// Versions:
//
//	DefaultVersion — no drain at all (pre-mailbox histories).
//	1              — drain as a REGULAR activity (6 history events per boundary).
//	2              — drain as a LOCAL activity (1 marker event per boundary).
//
// Version 2 exists purely to cut history growth. A regular activity costs
// exactly six events (ActivityTaskScheduled/Started/Completed, each preceded
// by a WorkflowTask triple); a local activity is batched into the workflow
// task and recorded as a single MARKER_RECORDED event. Measured against a real
// 51,199-event history, this drain ran 1,350 times for 8,100 events — every
// one of them, on the overwhelmingly common empty-queue path, spent to learn
// there was nothing to deliver.
//
// GetVersion returns the RECORDED version for any history that already has the
// marker, so a run that recorded 1 keeps issuing the regular-activity command
// for the rest of its life and only new executions get 2. Bumping maxSupported
// is therefore safe; what would NOT be safe is removing 1 from the switch.
const drainAgentMessagesVersionGate = "agent-mailbox-drain"

// Drain gate versions. Named rather than inlined so the switch below reads as
// the version ladder it is.
const (
	drainVersionRegularActivity = 1
	drainVersionLocalActivity   = 2
)

// drainAgentMessagesAtBoundary delivers any queued agent_messages for thread
// into its history, immediately before the next call_llm.
//
// This runs at every step boundary, so the empty-queue path is the hot path,
// and it necessarily costs one activity dispatch per boundary — workflow code
// cannot query the DB directly (determinism), and there is no in-workflow
// signal that tracks a thread's queue depth without a round trip. Within that
// one activity, the empty case is kept cheap deliberately:
// DrainAgentMessagesActivity issues a single ListQueuedAgentMessagesForThread
// query (served by the partial index idx_agent_messages_inbox WHERE status=1)
// and returns immediately when it is empty, with no SaveMessage or
// mark-delivered round trip.
//
// What version 2 changes is the COST of that dispatch, not its frequency: a
// LOCAL activity runs in the workflow worker's own process as part of the
// current workflow task and is recorded as one MARKER_RECORDED event, where a
// regular activity costs six. Nothing about the delivery semantics moves.
//
// WHY A LOCAL ACTIVITY IS SAFE HERE — the property that matters is that a
// local activity is not separately scheduled or retried by the SERVER, so a
// worker crash mid-execution loses the in-flight attempt entirely and the
// workflow task is retried from its last recorded point. That is disqualifying
// for anything whose loss corrupts state. It is not disqualifying here, for
// three independent reasons:
//
//  1. The drain is ALREADY best-effort by design. The error path below logs
//     and returns; a drain that fails outright is not a workflow failure. Loss
//     of an attempt is therefore indistinguishable from the failure mode this
//     function already treats as acceptable.
//  2. Nothing is lost when an attempt is lost. Delivery is one transaction in
//     DrainAgentMessagesActivity covering the envelope, every body, and the
//     MarkAgentMessagesDelivered bookkeeping. It either commits or it does
//     not. A lost attempt leaves the rows at status=1 (queued) — exactly the
//     state that makes the NEXT boundary pick them up. There is no partial
//     delivery to reconcile.
//  3. Re-execution is harmless. If the transaction commits but the marker is
//     never recorded, the retry re-runs the query, finds the rows now marked
//     delivered, and returns the empty result. The duplicate-delivery window
//     that would worry us is closed by the same transaction.
//
// The cost that IS real: a local activity's work happens inside the workflow
// task, so a slow drain eats into the workflow task timeout rather than
// running on its own schedule. That is priced for — the empty path is a single
// indexed SELECT, and the non-empty path is bounded by the number of queued
// rows.
//
// Best-effort: a failed drain is logged and the turn proceeds without it.
// Failing the whole workflow over a mailbox delivery hiccup would be a worse
// outcome than the agent simply seeing the message one iteration later, and
// the rows stay queued (status=1) for the next boundary to pick up.
func drainAgentMessagesAtBoundary(ctx workflow.Context, chatID, thread string) {
	version := workflow.GetVersion(ctx, drainAgentMessagesVersionGate,
		workflow.DefaultVersion, drainVersionLocalActivity)
	if version < drainVersionRegularActivity {
		return
	}
	if thread == "" {
		return
	}

	logger := workflow.GetLogger(ctx)
	input := types.DrainAgentMessagesInput{ChatID: chatID, Thread: thread}
	var output types.DrainAgentMessagesOutput

	var err error
	if version >= drainVersionLocalActivity {
		// Retries stay inside the workflow task, so the budget is tighter
		// than the regular-activity policy: three quick attempts of a single
		// indexed query, not a 10s-backoff ladder.
		localCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
			ScheduleToCloseTimeout: 30 * time.Second,
			StartToCloseTimeout:    10 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    100 * time.Millisecond,
				BackoffCoefficient: 2.0,
				MaximumInterval:    time.Second,
				MaximumAttempts:    3,
			},
		})
		err = workflow.ExecuteLocalActivity(localCtx, "DrainAgentMessages", input).Get(ctx, &output)
	} else {
		activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    10 * time.Second,
				MaximumAttempts:    3,
			},
		})
		err = workflow.ExecuteActivity(activityCtx, "DrainAgentMessages", input).Get(ctx, &output)
	}
	if err != nil {
		logger.Warn("[AgentMailbox] Failed to drain queued agent messages",
			"chatID", chatID,
			"thread", thread,
			"error", err,
		)
		return
	}
	if output.HasMessages {
		logger.Info("[AgentMailbox] Delivered queued agent messages",
			"chatID", chatID,
			"thread", thread,
			"count", output.Count,
		)
	}
}
