package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// THE WORKTREE KEY MUST BE UNIQUE PER WORKTREE.
//
// The key is the stack's whole identity: it selects the port block AND
// composes the namespace suffix, the database names and the NATS credentials
// (control-plane's dev/main.k derives all of them from `_key`). So two
// worktrees sharing a key do not merely contend for ports — the second stack
// renders the first one's DATABASE NAMES and the two dev stacks silently
// become one.
//
// The bare working-tree basename is not unique in the nested-repo layout that
// agent worktrees use, where the container directory carries the meaningful
// name and every checkout underneath it is called after the repo:
//
//	~/worktrees/newtool-5709b18d/control-plane
//	~/worktrees/taxes-237184bf/control-plane
//
// Both produced the key "control-plane" — verified on the reporting machine.

// wtLayout creates a linked worktree at <container>/<repo>, the layout that
// collides, and returns its root.
func wtLayout(t *testing.T, primary, container, repo, branch string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), container, repo)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, primary, "worktree", "add", "-q", "-b", branch, root)
	return root
}

// TestNestedWorktreesGetDistinctKeys is the regression lock for the collision.
func TestNestedWorktreesGetDistinctKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	a := wtLayout(t, primary, "newtool-5709b18d", "control-plane", "feat-a")
	b := wtLayout(t, primary, "taxes-237184bf", "control-plane", "feat-b")

	keyA, keyB := Worktree(a), Worktree(b)
	if keyA == "" || keyB == "" {
		t.Fatalf("linked worktrees resolved empty keys: %q, %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("two worktrees resolved the SAME key %q — "+
			"their stacks would share a namespace and DATABASE NAMES, not just a port block", keyA)
	}
	// The container name is what distinguishes them, so that is the key.
	if keyA != "newtool-5709b18d" || keyB != "taxes-237184bf" {
		t.Errorf("keys = %q, %q; want the container names that actually differ", keyA, keyB)
	}
}

// TestUniqueBasenameKeyIsUnchanged guards the ordinary case, and it is the
// one that protects existing machines: a plain `git worktree add ../wt-feature`
// must keep resolving to "wt-feature". The key is memoized in the block
// registry and multiplied into pre-mapped cluster ports and a baked-in IdP
// `iss` claim, so re-deriving a working key to a new string would move a
// running stack's ports.
func TestUniqueBasenameKeyIsUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	wt := filepath.Join(t.TempDir(), "wt-feature")
	git(t, primary, "worktree", "add", "-q", "-b", "feature-x", wt)

	if key := Worktree(wt); key != "wt-feature" {
		t.Fatalf("unique worktree key = %q, want the unchanged %q", key, "wt-feature")
	}
}

// TestCollidingContainersFallBackToUniqueKey: when even the container name is
// shared, the key still has to separate — git's per-worktree admin name is
// unique by construction, so it is the last resort.
func TestCollidingContainersFallBackToUniqueKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	// Same container name AND same repo name, in different parents.
	a := wtLayout(t, primary, "shared", "control-plane", "feat-a")
	b := wtLayout(t, primary, "shared", "control-plane", "feat-b")

	keyA, keyB := Worktree(a), Worktree(b)
	if keyA == keyB {
		t.Fatalf("worktrees with identical container AND repo names collided on key %q", keyA)
	}
}

// TestDistinctKeysYieldDistinctBlocks ties the key fix back to the thing it
// exists to protect: distinct keys must claim distinct port blocks.
func TestDistinctKeysYieldDistinctBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	initRepoOnBranch(t, primary)

	a := wtLayout(t, primary, "newtool-5709b18d", "control-plane", "feat-a")
	b := wtLayout(t, primary, "taxes-237184bf", "control-plane", "feat-b")

	portA, err := AllocatePort(a, 28080, Worktree(a))
	if err != nil {
		t.Fatal(err)
	}
	portB, err := AllocatePort(b, 28080, Worktree(b))
	if err != nil {
		t.Fatal(err)
	}
	if portA == portB {
		t.Fatalf("both worktrees rendered host port %d — the second stack loses the bind", portA)
	}
}
