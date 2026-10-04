// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: Temporal signal delivery for spawn cancellation; SDK wiring
package temporal

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/threadcancel"
)

// SpawnWorkflowReconciler settles the workflows row for a spawn that has been
// told to stop, so a page reload agrees with what was asked for instead of
// showing the spawn as still running.
//
// A one-method consumer-side interface rather than db.Repository: the stopper
// needs exactly one compare-and-swap, and taking the whole repository would
// pull the database layer into this package for it. It is spelled in terms of
// core.WorkflowStatus, which db.WorkflowStatus aliases, so db.Repository
// satisfies it without either side knowing about the other. Nil is valid and
// means "signal only" — the signal is the part that actually stops the work.
type SpawnWorkflowReconciler interface {
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus core.WorkflowStatus) (bool, error)
}

// SpawnRef names the identities of the spawn to stop. Aliased from the
// threadcancel leaf package, which both ends of this path can import — see its
// doc comment for why all three ids are carried.
type SpawnRef = threadcancel.SpawnRef

// SpawnStopper delivers a spawn cancellation to the workflow that is running
// the spawn, and reconciles the spawn's workflows row behind it.
//
// This is the ONE place that stops a spawn. Both callers go through it:
// ToolCallService.cancelChildWorkflowForToolCall (a user clicking cancel on a
// spawn card) and the spawn_stop tool (an agent stopping a sub-agent it
// spawned). They had the same two steps in the same order, and a second copy
// of "which signal, to which workflow id, with which ids in the payload" is
// exactly the drift that previously made cancel report success while the spawn
// ran on — so the steps live here once.
//
// It lives in this package rather than internal/workflow to avoid an import
// cycle: internal/llm/tools is imported BY internal/workflow/runtime, so an
// implementation a tool can be handed cannot live anywhere that imports tools.
// Same reason AgentMessageNotifier is here.
type SpawnStopper struct {
	client     client.Client
	lookup     ChatWorkflowLookup
	reconciler SpawnWorkflowReconciler
}

// NewSpawnStopper wires a stopper. A nil client disables it — StopSpawn then
// reports that it cannot deliver, which is what the daemon runtime (no
// Temporal connection) gets and what spawn_stop surfaces to the agent.
func NewSpawnStopper(c client.Client, lookup ChatWorkflowLookup, reconciler SpawnWorkflowReconciler) *SpawnStopper {
	return &SpawnStopper{client: c, lookup: lookup, reconciler: reconciler}
}

// StopSpawn asks the chat's root workflow to stop one spawn.
//
// An error means the spawn is STILL RUNNING and nothing was recorded: a cancel
// that claims it worked and didn't is worse than one that admits it couldn't,
// which is why the signal failure propagates instead of being swallowed. The
// reconcile afterwards is best-effort — the spawn is already stopping by then,
// and failing the call over bookkeeping would invite a retry that re-signals.
//
// ref.ThreadID and ref.ToolCallID are both carried into the payload. Either is
// enough for the receiver, but they are not derivable from each other, and a
// caller that knows both should name both so the spawn matches whichever it
// knows itself by. ref.WorkflowID is used only for the reconcile.
func (s *SpawnStopper) StopSpawn(ctx context.Context, chatID string, ref SpawnRef) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("no temporal client; cannot deliver spawn cancellation")
	}
	if chatID == "" {
		return fmt.Errorf("chat id is required to address the workflow running the spawn")
	}
	if ref.ThreadID == "" && ref.ToolCallID == "" {
		return fmt.Errorf("a spawn must be named by its thread id or its tool call id")
	}

	// Signal the CHAT's root workflow, not the spawn. A spawn has no Temporal
	// execution of its own — dispatchSpawnBackground runs it as a goroutine
	// inside the root execution — so there is nothing here to terminate.
	// Terminating the child id was the previous implementation and always
	// failed with "workflow not found for ID": that id names a thread and a DB
	// row.
	workflowID := chatID
	if s.lookup != nil {
		if resolved, ok := s.lookup(ctx, chatID); ok && resolved != "" {
			workflowID = resolved
		}
	}

	if err := s.client.SignalWorkflow(ctx, workflowID, "", threadcancel.SignalName, threadcancel.Signal{
		Thread:     ref.ThreadID,
		ToolCallID: ref.ToolCallID,
	}); err != nil {
		return fmt.Errorf("failed to signal spawn cancellation to %s: %w", workflowID, err)
	}
	logging.Info("Signalled spawn cancellation",
		"chatID", chatID, "childThread", ref.ThreadID, "toolCallID", ref.ToolCallID,
		"childWorkflowID", ref.WorkflowID, "workflowID", workflowID)

	s.reconcile(ctx, ref.WorkflowID)
	return nil
}

// reconcile moves the spawn's workflows row to cancelled.
//
// childWorkflowID, NOT the child thread id. They diverge for a resumed spawn,
// where the thread id names the ORIGINAL run's row — long since completed — so
// CASing it swaps nothing from active/paused and the row that is actually
// executing stays active after a successful stop.
//
// CAS rather than a blind write so a spawn that settled terminally on its own
// is never clobbered; PAUSED is covered too, because an explicit stop
// overrides a pause.
func (s *SpawnStopper) reconcile(ctx context.Context, childWorkflowID string) {
	if s.reconciler == nil || childWorkflowID == "" {
		return
	}
	for _, from := range []core.WorkflowStatus{core.Active(), core.Paused()} {
		swapped, err := s.reconciler.CompareAndSwapWorkflowStatus(ctx, childWorkflowID, core.Cancelled(), from)
		if err != nil {
			logging.Warn("Failed to reconcile stopped spawn's workflow status",
				"childWorkflowID", childWorkflowID, "from", from, "error", err)
			return
		}
		if swapped {
			return
		}
	}
}
