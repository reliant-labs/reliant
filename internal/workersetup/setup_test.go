// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"testing"
	"time"
)

// This one value sets two things that pull in opposite directions, which is why
// it is pinned rather than left to be tuned by feel.
//
//  1. The heartbeat RPC's own deadline, as max(value, minRPCTimeout=1s)
//     (internal_task_handlers.go internalHeartBeat). Too LOW and a busy
//     Temporal server blows the deadline, the SDK cancels a healthy activity
//     mid-stream, and the step burns a retry attempt. That is a measured
//     failure mode here, not a hypothetical: 872 in one day at 500ms.
//  2. The worker's activity cancel latency. Temporal delivers a pending
//     cancellation ONLY in a heartbeat RPC's response, and the SDK swallows
//     any heartbeat landing inside an open batching window entirely locally
//     (no RPC, no cancellation check). Too HIGH and the user waits that long
//     for pause/interrupt to take effect.
//
// The trap this test exists to prevent: a previous change read the 1s floor as
// making (1) insensitive to this value and reverted 3s to 500ms believing the
// RPC budget was unaffected. The floor only applies BELOW 1s — the revert
// really did narrow a 3s budget to 1s, and re-broke what the 3s had fixed.
func TestMaxHeartbeatThrottleInterval(t *testing.T) {
	const want = 2 * time.Second
	if maxHeartbeatThrottleInterval != want {
		t.Errorf("maxHeartbeatThrottleInterval = %v, want %v; this value is BOTH the heartbeat RPC deadline and the activity cancel latency (see comment on its use in setup.go) — if you're intentionally changing it, update this test and the comment together and confirm both tradeoffs are intended",
			maxHeartbeatThrottleInterval, want)
	}

	// Pin the floor interaction itself, since misreading it is what caused the
	// regression this test documents. Lowering the constant below 1s does not
	// buy a tighter RPC deadline — it only costs cancel latency headroom.
	const minRPCTimeout = time.Second
	budget := maxHeartbeatThrottleInterval
	if budget < minRPCTimeout {
		budget = minRPCTimeout
	}
	if budget <= minRPCTimeout {
		t.Errorf("effective heartbeat RPC budget = %v, which is still at the %v floor; the whole point of this constant's current value is to widen that budget beyond the floor",
			budget, minRPCTimeout)
	}
}
