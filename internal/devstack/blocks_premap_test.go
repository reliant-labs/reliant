package devstack

import (
	"strings"
	"testing"
)

// A PRE-MAPPED PORT THAT ANSWERS IS NOT A TAKEN BLOCK.
//
// This is the defect that actually broke `forge env up` on a machine with a
// live k3d cluster, and it is worth stating precisely because the symptom
// pointed somewhere else entirely.
//
// The cluster pre-maps EVERY stack's host port at cluster-CREATE time — that
// is deliberate, and it is what lets a new worktree start without recreating
// the cluster (see control-plane's generated deploy/k3d-ports.yaml, "Covers
// all 8 parallel dev stacks"). The consequence is that all 8 ports answer a
// TCP dial from the moment the cluster exists, whether or not any stack is
// using them.
//
// AllocatePortAvoidingForeign then probed those ports to pick a block for a
// NAMED key, found every one of them answering, walked to block 8 and hit the
// ceiling. So forge reported "this machine has already reached the 8-stack
// ceiling" for the FIRST worktree on the machine, while the registry that the
// ceiling supposedly counted was empty and `forge env devstack list` printed
// nothing. The advice in the message (`prune`) could not help, because there
// was nothing stale to reclaim.
//
// The fix is a boundary, not a threshold: a NAMED key takes its block from the
// registry, deterministically. Probing is reserved for the default key "",
// which is the only case it was introduced for (two different PROJECTS on one
// machine both wanting block 0's base port).

// TestNamedKeyIgnoresPreMappedPorts is the regression lock for the reported
// failure. Every port in the block range answers, exactly as a live k3d
// cluster makes them, and the allocation must still succeed at block 1.
func TestNamedKeyIgnoresPreMappedPorts(t *testing.T) {
	dir := t.TempDir()
	setMaxStacksLimit(t, 8)
	setActiveWorktree(t, "wt-new")

	// The exact shape of the reporting machine: k3d's serverlb publishes the
	// 8 pre-mapped stack ports 28080..28780, so they all answer. 28880 is
	// past the pre-map, so it is genuinely free — and that is the trap. The
	// probe walks the 8 answering ports, finds block 8 "free", and asks for a
	// block the ceiling must refuse. Modelling only "everything is busy"
	// would miss this, because that case falls through to the deterministic
	// fallback and passes either way.
	preMappedByCluster := func(p int) bool { return p >= 28880 }

	port, err := AllocatePortAvoidingForeign(dir, 28080, "wt-new", preMappedByCluster)
	if err != nil {
		t.Fatalf("a named key was refused because the cluster's pre-mapped ports answer: %v\n"+
			"pre-mapped ports are how a new worktree avoids a cluster recreate — "+
			"they must never be read as consumed blocks", err)
	}
	// First named key in an empty registry ⇒ block 1, deterministically.
	if port != 28180 {
		t.Errorf("port = %d, want 28180 (block 1 from the registry, not a probe)", port)
	}
}

// TestNamedKeyBlockIsDeterministicNotProbed: the block must come from the
// registry, so a key's port does not depend on which ports happened to answer
// at that moment. Two runs with opposite probe results must agree.
func TestNamedKeyBlockIsDeterministicNotProbed(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	setMaxStacksLimit(t, 8)
	setActiveWorktree(t, "wt-a")

	busy := t.TempDir()
	fromBusy, err := AllocatePortAvoidingForeign(busy, 28080, "wt-a", func(int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}

	quiet := t.TempDir()
	fromQuiet, err := AllocatePortAvoidingForeign(quiet, 28080, "wt-a", func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}

	if fromBusy != fromQuiet {
		t.Errorf("a named key's port depended on port availability: %d vs %d — "+
			"up and deploy would disagree whenever a stack happened to be running", fromBusy, fromQuiet)
	}
}

// TestDefaultKeyStillAvoidsForeignPort guards the behavior this probing was
// actually built for, so the fix above does not quietly delete it: TWO
// PROJECTS on one machine. The registry is per-repo, so key "" is block 0 in
// every project and both ask for the same base port. The dev IdP cannot share
// one — forge refuses to adopt an identity provider it did not start — so the
// default key must still step past a foreign holder.
func TestDefaultKeyStillAvoidsForeignPort(t *testing.T) {
	dir := t.TempDir()
	setMaxStacksLimit(t, 8)
	setActiveWorktree(t, "")

	// Another PROJECT holds the base port.
	got, err := AllocatePortAvoidingForeign(dir, 8080, "", func(p int) bool { return p != 8080 })
	if err != nil {
		t.Fatalf("the default key must still step past a foreign project's port: %v", err)
	}
	if got != 8180 {
		t.Errorf("port = %d, want 8180 (stepped past the foreign holder)", got)
	}
}

// TestCeilingMessageNamesTheRealRemedy: when the ceiling is hit for real, the
// message has to distinguish the two situations a developer can be in, since
// the remedy differs and the wrong one wastes the whole debugging session.
// `prune` only helps if a listed stack is stale; if every stack is live the
// only path forward is a wider ceiling AND a regenerated cluster pre-map.
func TestCeilingMessageNamesTheRealRemedy(t *testing.T) {
	dir := t.TempDir()
	setMaxStacksLimit(t, 2)
	setActiveWorktree(t, "")

	if _, err := AllocateBlock(dir, "wt-a"); err != nil {
		t.Fatal(err)
	}
	_, err := AllocateBlock(dir, "wt-b")
	if err == nil {
		t.Fatal("expected the ceiling to refuse a third stack")
	}
	msg := err.Error()
	for _, want := range []string{
		"forge env devstack list", // which stacks hold the blocks
		"pruning will reclaim nothing",
		"forge generate", // regenerate the pre-map after raising the ceiling
		"dev_stack.max_stacks",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("ceiling message does not mention %q — a developer whose stacks are all live\n"+
				"cannot tell from it that pruning is useless. Message:\n%s", want, msg)
		}
	}
}
