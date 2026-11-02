// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"strings"
	"testing"
)

// An archived request that carries no fence is never removed, by either
// command, in either phase. The server's orphan probe rests on this: it asks
// every connected machine about an archived workspace with no machine on
// record by sending it archived and WITHOUT a fence (worktreesweep.describe),
// trusting that the answer can only be "held" or "gone". The fixture's own
// helpers fill in a fence whenever one is missing, so this rule had no test.
//
// The workspace here is the easiest case to remove — clean, merged, idle — so
// nothing but the missing fence can be what keeps it.
func TestFencelessArchivedRequestIsNeverRemoved(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.idle()
	w := f.worktree("nofence", "feat/nofence")
	w.Fence = ""

	for _, tc := range []struct {
		name string
		run  func(Worktree) Result
	}{
		{"reconcile", func(w Worktree) Result {
			return f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
		}},
		{"snapshot_remove", func(w Worktree) Result {
			return f.r.SnapshotRemove(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
		}},
	} {
		for _, remove := range []bool{false, true} {
			req := w
			// Remove asks for the second phase outright: a server that skipped
			// the claim must not get a removal either.
			req.Remove = remove
			got := tc.run(req)
			if got.Outcome != OutcomeHeld || got.Reason != ReasonUnverified || got.Detail != "the request carried no archive fence" {
				t.Fatalf("%s (remove=%v): got %+v, want held/unverified for the missing fence", tc.name, remove, got)
			}
			if !exists(w.Path) {
				t.Fatalf("%s (remove=%v): the directory was removed", tc.name, remove)
			}
			if out := run(t, f.project, "worktree", "list", "--porcelain"); !strings.Contains(out, w.Path) {
				t.Fatalf("%s (remove=%v): the registration was removed:\n%s", tc.name, remove, out)
			}
		}
	}
}
