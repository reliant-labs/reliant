// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// drainAgentMessagesVersionGate is the workflow.GetVersion changeID for the
// mailbox drain call at the agent-loop step boundary. Histories recorded
// before this change must keep replaying WITHOUT the extra DrainAgentMessages
// activity command, or they wedge with TMPRL1100 on the next replay (see
// replaytest/fixtures/README.md). New executions get version 1 and drain
// their mailbox on every iteration.
const drainAgentMessagesVersionGate = "agent-mailbox-drain"

// drainAgentMessagesAtBoundary delivers any queued agent_messages for thread
// into its history, immediately before the next call_llm.
//
// This runs at every step boundary, so the empty-queue path is the hot path,
// and it necessarily costs exactly one Temporal activity dispatch per
// boundary — workflow code cannot query the DB directly (determinism), and
// there is no in-workflow signal that tracks a thread's queue depth without
// an activity round trip. Within that one activity, the empty case is kept
// cheap deliberately: DrainAgentMessagesActivity issues a single
// ListQueuedAgentMessagesForThread query (served by the partial index
// idx_agent_messages_inbox WHERE status=1) and returns immediately when it is
// empty, with no SaveMessage or mark-delivered round trip. A separate
// Count-then-List would only add a second query to the common case; List
// alone already answers "is there anything to do" in one round trip.
//
// Best-effort: a failed drain is logged and the turn proceeds without it.
// Failing the whole workflow over a mailbox delivery hiccup would be a worse
// outcome than the agent simply seeing the message one iteration later, and
// the rows stay queued (status=1) for the next boundary to pick up.
func drainAgentMessagesAtBoundary(ctx workflow.Context, chatID, thread string) {
	if workflow.GetVersion(ctx, drainAgentMessagesVersionGate, workflow.DefaultVersion, 1) < 1 {
		return
	}
	if thread == "" {
		return
	}

	logger := workflow.GetLogger(ctx)
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    10 * time.Second,
			MaximumAttempts:    3,
		},
	})

	input := map[string]interface{}{
		"chat_id": chatID,
		"thread":  thread,
	}
	var output struct {
		Count       int  `json:"count"`
		HasMessages bool `json:"has_messages"`
	}
	if err := workflow.ExecuteActivity(activityCtx, "DrainAgentMessages", input).Get(ctx, &output); err != nil {
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
