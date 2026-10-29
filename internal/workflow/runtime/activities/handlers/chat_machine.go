package handlers

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// chatDaemonSelector fills only a missing run selector from the durable chat
// pin. Workflow inputs are immutable snapshots: a live workflow can have no
// session_daemon_id even after launch-time pinning/backfill pins its chat.
// Letting that nil reach the router reselects from the user's machines on
// every call; registering/reconnecting another machine then moves its tools.
// Explicit workflow/node selectors remain explicit, and threadBoundExplicit
// still makes a spawned thread's own worktree authoritative.
func chatDaemonSelector(chat *db.Chat, explicit *types.DaemonSelector) *types.DaemonSelector {
	if explicit != nil || chat == nil || chat.NoMachine || chat.ActiveDaemonID == nil || *chat.ActiveDaemonID == "" {
		return explicit
	}
	return &types.DaemonSelector{ID: *chat.ActiveDaemonID}
}

// configMachine is the machine whose synced project config (skills, memory,
// MCP servers) a call routed by selector reads: the selector's machine id,
// as tool dispatch resolves it (toolDaemonSelector: an explicit node or
// workflow selector, the chat pin, the worktree's owner). A selector that
// names no id (by name, type or labels) or none at all reads the project's
// record, the union.
func configMachine(selector *toolexec.DaemonSelector) string {
	if selector == nil {
		return ""
	}
	return selector.ID
}
