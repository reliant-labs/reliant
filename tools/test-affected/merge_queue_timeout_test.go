package main

import "testing"

// rulesetCheckTimeoutMinutes is the main ruleset's check timeout: a required
// check that has not reported by then fails the merge group on its own.
const rulesetCheckTimeoutMinutes = 45

// mergeGateTimeoutMinutes is the merge gate's budget. 30 was too little: a web
// change to a hub module (paths.ts, a primitive) reaches ~1100 vitest files at
// the merge tier's two-edge cut, and on the 4-CPU runner that gate ran past 30
// minutes and was cancelled, ejecting a green PR (#2935, #3009).
const mergeGateTimeoutMinutes = 44

// TestMergeQueueGateTimeout pins the merge gate's timeout: long enough for a
// hub-module web change, and under the ruleset's check timeout, so a runner
// that dies frees its queue entry before the ruleset gives up on it.
func TestMergeQueueGateTimeout(t *testing.T) {
	wf := loadMergeQueue(t)
	gate := wf.Jobs["merge-gate"]
	if gate == nil {
		t.Fatalf("merge-queue.yml has no merge-gate job (jobs %v)", sortedKeys(wf.Jobs))
	}
	if got := gate.TimeoutMinutes; got != mergeGateTimeoutMinutes {
		t.Errorf("merge-gate timeout-minutes is %d, want %d (a 30-minute gate cancelled #2935 and #3009 mid-run)", got, mergeGateTimeoutMinutes)
	}
	if gate.TimeoutMinutes >= rulesetCheckTimeoutMinutes {
		t.Errorf("merge-gate timeout-minutes %d is not under the ruleset's %d-minute check timeout", gate.TimeoutMinutes, rulesetCheckTimeoutMinutes)
	}
}
