// Copyright (c) 2026 Reliant Labs
package runtime

import "go.temporal.io/sdk/workflow"

// routePendingEventsChangeID gates routing events a scheduling pass produced
// itself (a node skipped by its condition, a loop or approval that finished
// in place) before waiting for a running step.
//
// Each executor's loop launches the triggered nodes, then waits for one
// running step to complete. A skipped node completes at once, as an event in
// the same pass, but that event was routed only after the wait: while a
// parallel branch ran a long step, the skipped node's successors waited for
// it. The PR pipeline's heartbeat runs beside the whole attempt for up to
// 12h, so every pr-fix, main-red-fix and pr-regen run stopped at its first
// skipped node after the heartbeat started (control-plane #3127; pr-fix runs
// 019b8006, 937801cb on 2026-11-20 sat 40m+ after `tools [skipped]`).
//
// Versioned: a history recorded before it replays the old order.
const routePendingEventsChangeID = "route-pending-events-before-wait"

// routePendingFirst reports whether the loop routes its pending events now,
// without waiting for a running step. Only asked when events are pending, so
// a history with none records no marker.
func routePendingFirst(ctx workflow.Context, pending int) bool {
	if pending == 0 {
		return false
	}
	return workflow.GetVersion(ctx, routePendingEventsChangeID, workflow.DefaultVersion, 1) >= 1
}
