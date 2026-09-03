// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// A skipped node reported the status of the machinery that skipped it.
//
// The activity that records a skip runs to completion, so the lifecycle event
// is "completed" — and status was derived from the lifecycle alone. Every
// skipped node therefore came out COMPLETED, which is how `workflow watch`
// printed "✓ node review completed" for a reviewer that was configured off.
// NODE_EXECUTION_STATUS_SKIPPED existed in the proto and in db.models the whole
// time with nothing ever assigning it.
func TestSkippedNodeGetsSkippedStatus(t *testing.T) {
	t.Parallel()
	if got := nodeStatusFor("completed", true); got != db.NodeStatusSkipped {
		t.Fatalf("a skipped node reports status %v, want %v — a check that never ran is indistinguishable from one that passed",
			got, db.NodeStatusSkipped)
	}
}

// The lifecycle mapping for nodes that actually ran is unchanged: skipped is a
// new state, not a reclassification of the existing ones.
func TestNodeStatusForLifecycleEvents(t *testing.T) {
	t.Parallel()
	cases := map[string]db.NodeExecutionStatus{
		"started":   db.NodeStatusRunning,
		"completed": db.NodeStatusCompleted,
		"failed":    db.NodeStatusFailed,
		"cancelled": db.NodeStatusCancelled,
		"progress":  db.NodeStatusRunning,
	}
	for eventType, want := range cases {
		t.Run(eventType, func(t *testing.T) {
			if got := nodeStatusFor(eventType, false); got != want {
				t.Fatalf("nodeStatusFor(%q, false) = %v, want %v", eventType, got, want)
			}
		})
	}
}
