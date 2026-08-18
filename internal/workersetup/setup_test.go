// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"testing"
	"time"
)

// Temporal delivers a pending activity cancellation ONLY in a heartbeat RPC's
// response, and the SDK swallows any heartbeat that lands inside an open
// batching window entirely locally (no RPC, no cancellation check). So this
// value alone is the worker's cancel latency for any activity relying on the
// background heartbeater — raising it directly and silently raises how long a
// user waits for pause/interrupt to take effect. This test exists so that
// change requires touching this file, not a quiet edit to workerOpts.
func TestMaxHeartbeatThrottleIntervalStaysLow(t *testing.T) {
	const want = 500 * time.Millisecond
	if maxHeartbeatThrottleInterval != want {
		t.Errorf("maxHeartbeatThrottleInterval = %v, want %v; this value is the worker's activity cancel latency (see comment on its use in setup.go) — if you're intentionally raising it, update this test and the comment together and confirm the latency tradeoff is intended",
			maxHeartbeatThrottleInterval, want)
	}
}
