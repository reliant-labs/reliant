// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package temporal

import (
	"context"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/mailboxsignal"
)

// ChatWorkflowLookup resolves a chat id to the Temporal workflow id driving
// it. Narrow by design: taking db.Repository here would pull the whole
// database layer into this package for one field read.
//
// Nil is valid — the notifier then addresses the workflow by chat id, which is
// the default identity a chat's workflow is created with.
type ChatWorkflowLookup func(ctx context.Context, chatID string) (workflowID string, ok bool)

// AgentMessageNotifier rings the mailbox doorbell on the workflow that owns a
// chat, so a thread parked waiting on its background spawns wakes up and
// drains a queued message instead of sleeping until a sub-agent finishes.
//
// This is the tools-side counterpart to ChatService.notifyAgentMessageQueued.
// spawn_send runs inside the ExecuteTools activity — outside the workflow — so
// it cannot signal directly and reaches this through the narrow
// tools.AgentMessageNotifier interface.
//
// It lives in this package rather than internal/workflow to avoid an import
// cycle: internal/llm/tools is imported BY internal/workflow/runtime, so the
// implementation cannot live anywhere that imports tools.
type AgentMessageNotifier struct {
	client client.Client
	lookup ChatWorkflowLookup
}

// NewAgentMessageNotifier wires a notifier. A nil client disables it, which is
// what the daemon runtime (no Temporal connection) gets.
func NewAgentMessageNotifier(c client.Client, lookup ChatWorkflowLookup) *AgentMessageNotifier {
	return &AgentMessageNotifier{client: c, lookup: lookup}
}

// NotifyAgentMessageQueued signals the chat's workflow that toThreadID has a
// message waiting in its mailbox.
//
// Best-effort by contract. The row is durably queued before this is called and
// the drain reads it from the database, so a failure costs a late delivery —
// the behavior before the doorbell existed — never a lost message. It
// therefore logs rather than returning an error the tool would have to report,
// which would claim failure for a message that IS queued.
func (n *AgentMessageNotifier) NotifyAgentMessageQueued(ctx context.Context, chatID, toThreadID string) {
	if n == nil || n.client == nil || chatID == "" || toThreadID == "" {
		return
	}

	// Signal the CHAT's workflow, not the recipient thread's. A spawn has no
	// Temporal execution of its own — dispatchSpawnBackground runs it as a
	// goroutine inside the parent — so one execution drives every thread in
	// the chat, and the thread named in the payload selects which gate wakes.
	workflowID := chatID
	if n.lookup != nil {
		if resolved, ok := n.lookup(ctx, chatID); ok && resolved != "" {
			workflowID = resolved
		}
	}

	if err := n.client.SignalWorkflow(ctx, workflowID, "", mailboxsignal.SignalName,
		mailboxsignal.Signal{Thread: toThreadID}); err != nil {
		logging.Warn("Could not notify workflow of queued agent message; it will be delivered at the next loop boundary",
			"error", err, "chatID", chatID, "threadID", toThreadID, "workflowID", workflowID)
		return
	}
	logging.Info("Notified workflow of queued agent message",
		"chatID", chatID, "threadID", toThreadID, "workflowID", workflowID)
}
