// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// Sub-agents a chat inherited by being BRANCHED from the conversation that
// spawned them.
//
// A branch copies the transcript — the spawn calls and their "agent_id: …"
// handles included — but not ownership. The original conversation keeps
// running, keeps receiving those agents' reports, and stays the only thread
// that may message or stop them: one owner at a time is what keeps two
// orchestrators from steering the same agent. So the spawn tools treat an
// inherited agent as visible but read-only, and say exactly that, instead of
// calling an agent the chat's own transcript spawned "not a sub-agent
// spawned from this thread".

// findInheritedSpawnChild returns agentID's record when it is a sub-agent
// callerThreadID inherited through a branch, or nil when it is not.
//
// Best-effort: it backs a read-only view and a better refusal message, so a
// failed lookup degrades to "not inherited" — the answer every caller gave
// before inheritance existed — rather than failing the tool call.
func findInheritedSpawnChild(rctx *rctx.ToolContext, repo db.Repository, callerThreadID, agentID string) *db.InheritedSpawnChild {
	inherited, err := repo.ListInheritedSpawnChildren(rctx.Context, callerThreadID)
	if err != nil {
		logging.Warn("[spawn] Failed to resolve inherited sub-agents", "thread", callerThreadID, "error", err)
		return nil
	}
	var found *db.InheritedSpawnChild
	for _, child := range inherited {
		if child.ChildThreadID != nil && *child.ChildThreadID == agentID {
			// Keep overwriting: within one source the rows are in request
			// order, so the last match is the latest resumption.
			found = child
		}
	}
	return found
}

// inheritedSourceLabel names the conversation that owns an inherited agent.
func inheritedSourceLabel(child *db.InheritedSpawnChild) string {
	if child.SourceChatTitle != "" {
		return fmt.Sprintf("%q (chat %s)", child.SourceChatTitle, child.SourceChatID)
	}
	return "chat " + child.SourceChatID
}

// inheritedSpawnRefusal is the error for trying to CONTROL an inherited agent
// (action is "message", "stop" or "wait on"). It names the owner, the reason,
// and what the caller can still do.
func inheritedSpawnRefusal(action string, child *db.InheritedSpawnChild, agentID string) string {
	return fmt.Sprintf(
		"agent_id %q was spawned by thread %s in the conversation %s, before this chat was branched from it. "+
			"It still belongs to that conversation: its result is delivered there, not here, and only that conversation can %s it. "+
			"From this branch you can only check on it read-only: spawn_status(agent_id=%q) without wait shows its status and latest answer.",
		agentID, child.SourceThreadID, inheritedSourceLabel(child), action, agentID)
}

// inheritedReadOnlyNote prefixes spawn_status output about an inherited agent,
// so a model reading the agent's result does not also assume it can act on it.
func inheritedReadOnlyNote(child *db.InheritedSpawnChild) string {
	return fmt.Sprintf(
		"[Inherited, read-only] This agent was spawned by thread %s in the conversation %s, before this chat was branched from it. "+
			"Its result is delivered to that conversation, not here, and this chat cannot message or stop it.\n\n",
		child.SourceThreadID, inheritedSourceLabel(child))
}
