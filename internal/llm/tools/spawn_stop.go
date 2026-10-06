// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/workflow/threadcancel"
)

type SpawnStopParams struct {
	AgentID string `json:"agent_id" jsonschema:"required,description=The agent_id (thread id) of a sub-agent YOU spawned"`
	Reason  string `json:"reason,omitempty" jsonschema:"description=Why you are stopping it. Recorded in the log; the agent does not see it."`
}

const (
	SpawnStopToolName    = "spawn_stop"
	spawnStopDescription = `Stop a sub-agent you spawned, when its work is no longer needed.

THE RECEIPT IS HONEST — READ IT LITERALLY:
This REQUESTS the stop. The agent is not dead when this returns. A spawn runs
as a cooperative loop and observes the cancellation at its NEXT STEP BOUNDARY,
so an agent in the middle of a long tool call — a build, a test run, a wait —
keeps going until that call returns. Do not treat the work it was doing as
already abandoned.

To confirm it actually stopped, call spawn_status(agent_id=..., wait: true).
That blocks until the agent reaches a terminal state, and is the only thing
that tells you the stop LANDED rather than that it was asked for.

Only a DIRECT sub-agent of yours can be stopped. Your parent, your siblings
and unrelated agents are rejected — stopping something you did not start is
not yours to decide.

If the agent has already finished this reports that and does nothing: a
finished agent has no loop left to stop, and its result (if any) still stands.

Reach for this when a fan-out has already answered the question, when you have
changed approach and the delegated work is now wrong, or when an agent is
clearly stuck. Prefer spawn_send to REDIRECT an agent whose work is still
useful — stopping discards everything it has not reported yet.`
)

// SpawnStopper requests that one spawned agent stop.
//
// Declared here as a one-method interface rather than taking a Temporal
// client, because a tool has no business knowing what a workflow engine is:
// the implementation lives next to the wiring (internal/temporal) and tests
// substitute a recorder. Same shape and same reason as AgentMessageNotifier.
//
// Unlike that notifier this returns an error, and the tool reports it. A stop
// is not best-effort: the whole failure mode this exists to avoid is a surface
// that says "cancelled" while the agent keeps working, so an undelivered stop
// must be visible to the caller.
//
// A nil stopper is valid and means "no way to stop anything here" — the daemon
// runtime builds a factory with no Temporal connection at all.
type SpawnStopper interface {
	StopSpawn(ctx context.Context, chatID string, ref threadcancel.SpawnRef) error
}

type spawnStopTool struct {
	repo    db.Repository
	stopper SpawnStopper
}

func NewSpawnStopTool(repo db.Repository, stopper SpawnStopper) Tool {
	return NewToolWrapper[SpawnStopParams, ToolResponse](&spawnStopTool{repo: repo, stopper: stopper})
}

func (s *spawnStopTool) Name() string {
	return SpawnStopToolName
}

func (s *spawnStopTool) Description() string {
	return spawnStopDescription
}

func (s *spawnStopTool) RequiresPermission(params SpawnStopParams) (bool, error) {
	return false, nil
}

func (s *spawnStopTool) Execute(rctx *rctx.ToolContext, params SpawnStopParams) (ToolResponse, error) {
	if s.repo == nil {
		return NewTextErrorResponse("This tool requires a database connection and is not available in daemon-only mode"), nil
	}
	threadID := rctx.Thread
	if threadID == "" {
		return NewTextErrorResponse("Thread context required"), nil
	}
	if params.AgentID == "" {
		return NewTextErrorResponse("agent_id is required"), nil
	}
	if params.AgentID == threadID {
		return NewTextErrorResponse("agent_id cannot be this thread itself — spawn_stop stops a sub-agent, not the caller"), nil
	}

	// Only a DIRECT child may be stopped, which is stricter than spawn_send's
	// parent<->child rule on purpose: messaging your parent is a report, while
	// stopping your parent would end the run that is waiting on you.
	ref, err := spawnStopRefForChild(rctx, s.repo, threadID, params.AgentID)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if ref.ToolCallID == "" {
		if inherited := findInheritedSpawnChild(rctx, s.repo, threadID, params.AgentID); inherited != nil {
			return NewTextErrorResponse(inheritedSpawnRefusal("stop", inherited, params.AgentID)), nil
		}
		return NewTextErrorResponse(fmt.Sprintf(
			"agent_id %q is not a sub-agent you spawned. spawn_stop can only stop your own direct children — "+
				"not your parent, not a sibling, and not an unrelated agent. "+
				"Use spawn_status to see your sub-agents.", params.AgentID)), nil
	}

	target, err := s.repo.GetThread(rctx.Context, params.AgentID)
	if err != nil || target == nil {
		return NewTextErrorResponse(fmt.Sprintf("Target agent %q could not be found.", params.AgentID)), nil
	}

	title := params.AgentID
	if target.Title != nil && *target.Title != "" {
		title = *target.Title
	}

	// An agent that already finished is not an error to report — nothing is
	// wrong, there is simply no loop left to stop, and the caller's intent
	// ("this work should not continue") is already satisfied.
	if core.ThreadStatusIsTerminal(target.Status) {
		return NewTextResponse(fmt.Sprintf(
			"Agent %q (%s) has already finished (status: %s) — there was nothing left to stop. "+
				"Any result it reported still stands.",
			title, params.AgentID, core.ThreadStatusLabel(target.Status))), nil
	}

	if s.stopper == nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"Stopping a sub-agent is not available in this runtime, so agent %q is still running. "+
				"It will finish on its own; spawn_status(agent_id=%q) tracks it.",
			params.AgentID, params.AgentID)), nil
	}

	// A failed delivery means the agent is still running. Report that rather
	// than a stop that did not happen — the whole point of this tool's receipt
	// is that it never claims more than it did.
	if err := s.stopper.StopSpawn(rctx.Context, rctx.ChatID, ref); err != nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"Could not deliver the stop to agent %q, so it is STILL RUNNING: %v", params.AgentID, err)), nil
	}

	return NewTextResponse(fmt.Sprintf(
		"Stop REQUESTED for %q (%s). It is not stopped yet: the agent observes this at its next step boundary, "+
			"so if it is inside a long tool call it keeps going until that call returns. "+
			"Call spawn_status(agent_id=%q, wait: true) to confirm it actually stopped.",
		title, params.AgentID, params.AgentID)), nil
}

// spawnStopRefForChild resolves targetThreadID to the spawn identities needed
// to stop it, or a zero ref if it is not a direct child of callerThreadID.
//
// It returns the MOST RECENT matching call, not the first. ListSpawnChildren
// orders by requested_at ASC and yields one row PER RESUMPTION for the same
// child thread, so a resumed spawn (spawn with agent_id — observed up to six
// resumptions on one thread) produces several matches of which only the last
// is live. Taking the first returned the oldest tool call and the oldest,
// already-completed workflow row: the signal still landed because the runtime
// also matches on thread id, but the reconcile CASed a finished row and the
// executing one stayed active after a successful stop.
func spawnStopRefForChild(rctx *rctx.ToolContext, repo db.Repository, callerThreadID, targetThreadID string) (threadcancel.SpawnRef, error) {
	children, err := repo.ListSpawnChildren(rctx.Context, callerThreadID)
	if err != nil {
		return threadcancel.SpawnRef{}, fmt.Errorf("failed to verify agent relationship: %w", err)
	}

	var ref threadcancel.SpawnRef
	for _, child := range children {
		if child.ChildThreadID == nil || *child.ChildThreadID != targetThreadID {
			continue
		}
		// Keep overwriting: ascending order leaves the latest resumption.
		ref = threadcancel.SpawnRef{ThreadID: targetThreadID, ToolCallID: child.ToolCallID}
		if child.ChildWorkflowID != nil {
			ref.WorkflowID = *child.ChildWorkflowID
		}
	}
	return ref, nil
}
