package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// THE REGISTRY IS PER-REPO, NOT PER-DIRECTORY.
//
// A block is a claim against a MACHINE-WIDE resource: block N means "host
// ports base+N*100 on this machine", pre-mapped once in the cluster's port
// map and baked into the dev IdP's `iss` claim. A per-directory registry
// cannot express that claim, and it failed in both directions at once —
// which is what these tests lock.
//
// Observed (control-plane, 8 k3d-mapped stacks live on the machine):
// `forge env up dev` in a fresh linked worktree died with "this machine has
// already reached the 8-stack ceiling", while that worktree's own
// .forge/blocks.json did not exist and `forge env devstack list` printed
// nothing. The ceiling was counting an EMPTY registry. Had the ceiling been
// higher, the same emptiness would have handed this worktree block 1 — a
// block another running stack already holds — so the two stacks would have
// rendered identical host ports.

// TestRegistryIsSharedAcrossLinkedWorktrees is the core lock: a block
// allocated from one worktree must be VISIBLE from another worktree of the
// same repo, because they contend for the same machine-wide ports.
func TestRegistryIsSharedAcrossLinkedWorktrees(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	wtA := filepath.Join(t.TempDir(), "wt-a")
	wtB := filepath.Join(t.TempDir(), "wt-b")
	git(t, primary, "worktree", "add", "-q", "-b", "feat-a", wtA)
	git(t, primary, "worktree", "add", "-q", "-b", "feat-b", wtB)

	// Worktree A claims its block, exactly as `forge env up` would.
	blockA, err := AllocateBlock(wtA, "wt-a")
	if err != nil {
		t.Fatalf("AllocateBlock(wt-a): %v", err)
	}

	// Worktree B now claims ITS block. It must not be handed A's block.
	blockB, err := AllocateBlock(wtB, "wt-b")
	if err != nil {
		t.Fatalf("AllocateBlock(wt-b): %v", err)
	}
	if blockA == blockB {
		t.Fatalf("two live worktrees were handed the SAME block %d — "+
			"their stacks would render identical host ports and the second would lose the bind", blockA)
	}

	// Both entries live in ONE registry, at the primary checkout.
	if _, err := os.Stat(filepath.Join(primary, registryRel)); err != nil {
		t.Errorf("registry was not written at the repo anchor (the primary checkout): %v", err)
	}
	for _, wt := range []string{wtA, wtB} {
		if _, err := os.Stat(filepath.Join(wt, registryRel)); err == nil {
			t.Errorf("a per-worktree registry was written at %s — "+
				"a second registry is invisible to the ceiling and to the other worktrees", wt)
		}
	}

	// And the roster is complete from EITHER worktree's point of view.
	for _, from := range []string{primary, wtA, wtB} {
		list, err := List(from)
		if err != nil {
			t.Fatalf("List(%s): %v", from, err)
		}
		if len(list) != 2 {
			t.Errorf("registry read from %s has %d entries, want 2 — "+
				"a worktree that cannot see the others' blocks will collide with them", from, len(list))
		}
	}
}

// TestCeilingCountsBlocksFromAnotherWorktree reproduces the reported failure
// directly: the ceiling must be evaluated against the repo-wide roster, so a
// new worktree is refused only when the machine is GENUINELY full — and, the
// half that actually bit, is NOT refused when it is not.
func TestCeilingCountsBlocksFromAnotherWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)
	wt := filepath.Join(t.TempDir(), "wt-new")
	git(t, primary, "worktree", "add", "-q", "-b", "feat-new", wt)

	setMaxStacksLimit(t, 8)

	// The machine has room: only the implicit default stack exists. A fresh
	// worktree MUST be able to allocate. Before the anchor fix this passed
	// for the wrong reason (an empty per-worktree registry); the failure it
	// models is the real one, where the shared registry is consulted.
	if _, err := AllocateBlock(wt, "wt-new"); err != nil {
		t.Fatalf("a fresh worktree was refused a block on an almost-empty machine: %v", err)
	}

	// Now fill the repo-wide roster to the ceiling from the PRIMARY checkout,
	// then confirm a further NEW key is refused when read from the WORKTREE.
	// This is the direction that was silently broken: blocks allocated
	// elsewhere were invisible, so the ceiling could not see a full machine.
	for i := 0; i < 6; i++ {
		if _, err := AllocateBlock(primary, keyFor(i)); err != nil {
			t.Fatalf("AllocateBlock(%s) filling the roster: %v", keyFor(i), err)
		}
	}
	if _, err := AllocateBlock(wt, "wt-overflow"); err == nil {
		t.Fatal("a NEW block was allocated past the ceiling because blocks held by " +
			"other worktrees were invisible — it would render ports the cluster never pre-mapped")
	}
}

// TestNonRepoKeepsLocalRegistry: outside a git checkout there is no anchor to
// resolve, so the registry stays exactly where it always was.
func TestNonRepoKeepsLocalRegistry(t *testing.T) {
	dir := t.TempDir() // not git-init'ed
	if _, err := AllocateBlock(dir, "wt-a"); err != nil {
		t.Fatalf("AllocateBlock outside a repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, registryRel)); err != nil {
		t.Errorf("a non-repo project must keep its registry locally: %v", err)
	}
}
