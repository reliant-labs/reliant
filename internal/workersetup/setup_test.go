// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/version"
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

// The workflow-task cap is what keeps a burst of history replays from taking
// every core the activities in the same process need (prod, 2026-10-23). The
// derivation must leave a core free above the floor, and a configured value
// must win.
func TestWorkflowTaskSlots(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		configured, gomaxprocs int
		want                   int
	}{
		{"derived: a 3-core pod leaves activities a core", 0, 3, 2},
		{"derived: a 4-core node", 0, 4, 3},
		{"derived: the SDK's floor of 2 at 2 cores", 0, 2, 2},
		{"derived: the floor holds below 2 cores", 0, 1, 2},
		{"configured wins over the derivation", 6, 3, 6},
		{"configured may sit at the floor on a big node", 2, 16, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WorkflowTaskSlots(tc.configured, tc.gomaxprocs); got != tc.want {
				t.Errorf("WorkflowTaskSlots(%d, %d) = %d, want %d", tc.configured, tc.gomaxprocs, got, tc.want)
			}
		})
	}
}

// workerOptions carries the config into the SDK's fields, and leaves the
// activity side uncapped: the cap is workflow-task-only by design.
func TestWorkerOptionsWorkflowTaskLimits(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		opts := workerOptions(&Config{}, nil, 3)
		if opts.MaxConcurrentWorkflowTaskExecutionSize != 2 {
			t.Errorf("MaxConcurrentWorkflowTaskExecutionSize = %d, want 2 (max(2, GOMAXPROCS-1) at 3)", opts.MaxConcurrentWorkflowTaskExecutionSize)
		}
		if opts.MaxConcurrentWorkflowTaskPollers != defaultWorkflowTaskPollers {
			t.Errorf("MaxConcurrentWorkflowTaskPollers = %d, want %d", opts.MaxConcurrentWorkflowTaskPollers, defaultWorkflowTaskPollers)
		}
	})
	t.Run("configured", func(t *testing.T) {
		opts := workerOptions(&Config{WorkflowTaskSlots: 4, WorkflowTaskPollers: 3}, nil, 3)
		if opts.MaxConcurrentWorkflowTaskExecutionSize != 4 {
			t.Errorf("MaxConcurrentWorkflowTaskExecutionSize = %d, want 4", opts.MaxConcurrentWorkflowTaskExecutionSize)
		}
		if opts.MaxConcurrentWorkflowTaskPollers != 3 {
			t.Errorf("MaxConcurrentWorkflowTaskPollers = %d, want 3", opts.MaxConcurrentWorkflowTaskPollers)
		}
	})
	t.Run("activities keep their own budget", func(t *testing.T) {
		opts := workerOptions(&Config{}, nil, 3)
		if opts.MaxConcurrentActivityExecutionSize != 0 || opts.MaxConcurrentLocalActivityExecutionSize != 0 {
			t.Errorf("activity slots = %d, local activity slots = %d; want 0 (the SDK's 1000) for both",
				opts.MaxConcurrentActivityExecutionSize, opts.MaxConcurrentLocalActivityExecutionSize)
		}
		if opts.Tuner != nil {
			t.Error("Tuner is set; it is mutually exclusive with the fixed workflow-task cap")
		}
	})
}

// The worker's BuildID is the binary's version, never a constant shared by
// every build: it is what a workflow task's history and the SDK's panic log
// name, and a mid-rollout non-determinism panic (TMPRL1100, prod 2026-10-23)
// is only diagnosable when the two builds involved have different names.
func TestWorkerOptionsBuildIDIsTheBinaryVersion(t *testing.T) {
	opts := workerOptions(&Config{}, nil, 3)
	if opts.BuildID != version.TemporalBuildID() || opts.BuildID == "reliant-worker" {
		t.Errorf("BuildID = %q, want version.TemporalBuildID() = %q", opts.BuildID, version.TemporalBuildID())
	}
	if opts.UseBuildIDForVersioning {
		t.Error("UseBuildIDForVersioning is set; the BuildID only labels the build")
	}
}
