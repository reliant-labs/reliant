package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// PRUNE OFFERED TO RECLAIM A RUNNING STACK'S BLOCK.
//
// Found by running the fixed `forge env devstack prune --dry-run` against a
// real machine: it reported it would reclaim "my-new-feature-50f77334" while
// that worktree was on disk with a stack running in it. That is the exact
// failure this command is built to never commit — reclaiming moves ports, which
// invalidates the k3d host-port mapping and the dev IdP's baked-in `iss` claim.
//
// Cause: two derivations of the same key that had drifted apart.
// liveWorktreeKeys built the live set with Sanitize(filepath.Base(root)), while
// the ALLOCATOR keys a stack on Worktree(), which additionally runs
// disambiguate(). In the nested-repo layout every worktree is <container>/<repo>:
//
//	~/.reliant/worktrees/reliant-labs/my-new-feature-50f77334/control-plane
//	~/.reliant/worktrees/reliant-labs/newtool-5709b18d/control-plane
//
// so every basename is "control-plane" and the live set collapsed to a single
// entry that matched no registry key at all. Every stack in that layout looked
// dead simultaneously.
//
// The fix derives the live set through Worktree() itself, so the two cannot
// drift again by construction rather than by two implementations agreeing.

// nestedWorktree creates <container>/<repo> under a fresh temp dir, the layout
// that makes every worktree root share one basename.
func nestedWorktree(t *testing.T, primary, container, repo, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), container, repo)
	pruneGit(t, primary, "worktree", "add", "-q", "-b", branch, dir)
	return dir
}

// TestPruneNeverReclaimsLiveStackInNestedLayout is the regression lock: two
// live worktrees sharing a basename must BOTH survive a prune.
func TestPruneNeverReclaimsLiveStackInNestedLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	// Two live worktrees whose basenames collide, exactly as on the real
	// machine. Their keys come from the container dir via disambiguate().
	aDir := nestedWorktree(t, primary, "my-new-feature-50f77334", "control-plane", "feat-a")
	nestedWorktree(t, primary, "newtool-5709b18d", "control-plane", "feat-b")

	keyA := Worktree(aDir)
	if keyA != "my-new-feature-50f77334" {
		t.Fatalf("Worktree() keyed the nested worktree as %q, want the container name — "+
			"the test's premise depends on disambiguate()", keyA)
	}

	// The stack registers under the key the allocator derives.
	registerStackKey(t, primary, keyA, 28080)
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("Prune reclaimed a LIVE stack in the nested layout: %+v — "+
			"its worktree is on disk; reclaiming moves its ports, k3d host mapping and IdP issuer",
			pruned)
	}
	if stacks, _ := ListStacks(primary); len(stacks) != 1 || stacks[0] != keyA {
		t.Fatalf("live nested stack missing from the registry after prune: %v", stacks)
	}
}

// TestPruneStillReclaimsDeadNestedWorktree: the fix must not overshoot. A
// nested worktree that is genuinely deleted is still reclaimed, so making the
// live set correct did not simply disable pruning for this layout.
func TestPruneStillReclaimsDeadNestedWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	liveDir := nestedWorktree(t, primary, "wt-live-container", "control-plane", "live")
	deadDir := nestedWorktree(t, primary, "wt-dead-container", "control-plane", "dead")

	liveKey := Worktree(liveDir)
	deadKey := Worktree(deadDir)
	registerStackKey(t, primary, liveKey, 28080)
	registerStackKey(t, primary, deadKey, 29080)
	// A derived port-block key from the dead worktree too, since both kinds
	// must be reclaimed together for the block budget to actually recover.
	registerDerivedKey(t, primary, deadKey, "prod-"+deadKey, 3000)

	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	got := map[string]bool{}
	for _, p := range pruned {
		got[p.Key] = true
	}
	for _, want := range []string{deadKey, "prod-" + deadKey} {
		if !got[want] {
			t.Errorf("dead nested worktree's key %q not reclaimed; reclaimed = %v", want, sortedKeys(pruned))
		}
	}
	if got[liveKey] {
		t.Errorf("Prune reclaimed the LIVE nested worktree %q", liveKey)
	}
}

// TestLiveWorktreeKeyStableWhenCollidingSiblingDeleted pins the third defect
// found here, which is subtler than the basename collision and strictly worse.
//
// disambiguate() is not STABLE over time. It returns the container name only
// while a sibling worktree's basename collides, and falls back to the bare
// basename once that sibling is gone. So a stack registered as
// "wt-live-container" re-derives to "control-plane" the instant an unrelated
// worktree is deleted — and a live set built from only today's derivation then
// declares the still-running stack dead.
//
// Deleting one worktree must never make a DIFFERENT, untouched worktree's block
// reclaimable.
func TestLiveWorktreeKeyStableWhenCollidingSiblingDeleted(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	liveDir := nestedWorktree(t, primary, "wt-live-container", "control-plane", "live")
	deadDir := nestedWorktree(t, primary, "wt-dead-container", "control-plane", "dead")

	// Registered while both exist, so the key is the container name.
	liveKey := Worktree(liveDir)
	if liveKey != "wt-live-container" {
		t.Fatalf("premise: Worktree() = %q while a sibling collides, want the container name", liveKey)
	}
	registerStackKey(t, primary, liveKey, 28080)

	// An UNRELATED worktree is deleted. The live one is untouched on disk.
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	setActiveWorktree(t, "")

	if now := Worktree(liveDir); now == liveKey {
		t.Skip("disambiguate() became stable; this regression is structurally impossible now")
	}

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, p := range pruned {
		if p.Key == liveKey {
			t.Fatalf("deleting an UNRELATED worktree made live stack %q reclaimable: %+v — "+
				"its key merely re-derived differently; the worktree is still on disk", liveKey, pruned)
		}
	}
}

// TestLiveWorktreeKeysMatchesAllocatorDerivation pins the invariant directly,
// rather than only through Prune's behaviour: for every live worktree, the key
// the live set contains is the key the allocator would register. Two
// derivations of one key drifting apart is the actual defect, so this is the
// assertion that would catch it returning in any form.
func TestLiveWorktreeKeysMatchesAllocatorDerivation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	roots := []string{
		nestedWorktree(t, primary, "container-one", "control-plane", "one"),
		nestedWorktree(t, primary, "container-two", "control-plane", "two"),
		addWorktree(t, primary, "plain-worktree", "three"), // the unique-basename case
	}

	live, err := liveWorktreeKeys(primary)
	if err != nil {
		t.Fatalf("liveWorktreeKeys: %v", err)
	}
	for _, root := range roots {
		key := Worktree(root)
		if key == "" {
			t.Fatalf("Worktree(%s) returned empty", root)
		}
		if !live[key] {
			t.Errorf("live set does not contain %q, the key the ALLOCATOR registers for %s "+
				"(live set = %v) — prune would treat this running stack as dead", key, root, live)
		}
	}
}
