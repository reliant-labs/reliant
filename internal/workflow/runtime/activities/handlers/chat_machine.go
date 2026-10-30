package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
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
	return &types.DaemonSelector{ID: *chat.ActiveDaemonID, Session: true}
}

// configMachine is the machine whose synced project config (skills, memory,
// MCP servers) a call routed by selector reads: the daemon tool dispatch sends
// that call to (toolDaemonSelector: an explicit node or workflow selector, the
// chat pin, the worktree's owner). An id selector names it outright; a selector
// by name, type or labels, or none at all, is resolved through router the way
// dispatch resolves it, so a workflow that picks machine B by name reads B's
// skills rather than the project's union. A run that resolves no machine (no
// router, no machine connected, a no-machine run) reads the project's record,
// the union: its commands reach no machine either.
func configMachine(ctx context.Context, router toolexec.DaemonRouter, userID string, selector *toolexec.DaemonSelector) string {
	if selector != nil && selector.ID != "" {
		return selector.ID
	}
	if router == nil || userID == "" {
		return ""
	}
	id, err := toolexec.ResolveDaemonIDForSelector(ctx, router, userID, selector)
	if err != nil {
		logging.DebugContext(ctx, "[config] no machine resolved for the call's selector; reading the project's config", "error", err)
		return ""
	}
	return id
}
