package devstack

import (
	"strings"
	"testing"
)

// THE CEILING COUNTS BLOCKS; THE REMEDY IT NAMED ONLY TOUCHES STACKS.
//
// Found while fixing the long-key rejection (blocks_key_length_test.go). Once
// a 26-character prod key was accepted, `forge env render prod` reached the
// ceiling instead and reported:
//
//	this repo has already reached the 8-stack ceiling ... counting 8 stacks
//	already registered in .forge/blocks.json
//	1. `forge env devstack list` shows which worktrees hold the 8 blocks ...
//	   run `forge env devstack prune` to reclaim the block.
//
// `forge env devstack list` printed exactly ONE line. Six of the eight blocks
// were held by port-block keys ("prod", "prod-cp-obs", "prod-cp-litellm", …),
// which list deliberately hides and which Prune refuses to reclaim by design
// — reclaiming one would move a live stack's port. So the message asserted a
// count the tool it recommended contradicts, and steered the reader to a
// remedy that could not work, in a message whose entire job is to be a
// runbook.
//
// The rule is unchanged and correct: a block is a block, and a port-block key
// consumes the cluster's pre-mapped host-port range exactly as a worktree
// does. Only the DIAGNOSIS was wrong.

// TestCeilingMessageDistinguishesStacksFromPortBlockKeys reproduces the
// control-plane registry shape and pins that the message tells the reader
// which holders prune can actually reclaim.
func TestCeilingMessageDistinguishesStacksFromPortBlockKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	setMaxStacksLimit(t, 8)

	// One real worktree stack — marked by allocating it while it is the
	// active worktree, which is how isStackKey records the flag.
	setActiveWorktree(t, "my-new-feature-50f77334")
	if _, err := AllocateBlock(dir, "my-new-feature-50f77334"); err != nil {
		t.Fatalf("AllocateBlock(stack): %v", err)
	}
	// Six plain port-block keys, exactly the kind `devstack list` hides.
	setActiveWorktree(t, "")
	for _, key := range []string{
		"prod", "prod-cp-obs", "prod-cp-litellm",
		"prod-cp204-fix-st", "prod-newtool-5709b18d", "newtool-5709b18d",
	} {
		if _, err := AllocateBlock(dir, key); err != nil {
			t.Fatalf("AllocateBlock(%q): %v", key, err)
		}
	}

	_, err := AllocateBlock(dir, "prod-forge-deploy-882e308d")
	if err == nil {
		t.Fatal("AllocateBlock allocated past the ceiling")
	}
	msg := err.Error()

	// It must name the holders, so the reader can see WHY the ceiling is
	// full without hand-parsing .forge/blocks.json.
	for _, want := range []string{"prod-cp-obs", "my-new-feature-50f77334"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ceiling message does not name the block holder %q:\n%s", want, msg)
		}
	}
	// It must say the ceiling counts blocks, not stacks — the claim that
	// contradicted `devstack list`.
	if strings.Contains(msg, "8-stack ceiling") {
		t.Errorf("ceiling still reports blocks as stacks, contradicting `devstack list`:\n%s", msg)
	}
	if !strings.Contains(msg, "port-block key") {
		t.Errorf("ceiling message does not distinguish port-block keys from stacks:\n%s", msg)
	}
	// And it must warn that prune cannot reclaim those, instead of sending
	// the reader to a remedy that reclaims nothing.
	if !strings.Contains(msg, "NOT reclaim") {
		t.Errorf("ceiling message does not say prune cannot reclaim port-block keys:\n%s", msg)
	}
}

// TestCeilingMessageSurvivesAnEmptyRegistry: the holder list is a diagnostic
// built from state the caller happens to hold, never a precondition of the
// rule. With no registry to describe, the ceiling must still refuse and still
// produce a usable message.
func TestCeilingMessageSurvivesAnEmptyRegistry(t *testing.T) {
	setMaxStacksLimit(t, 1)

	err := checkCeiling("prod-something", 1, nil)
	if err == nil {
		t.Fatal("checkCeiling accepted a block at the ceiling with a nil registry")
	}
	for _, want := range []string{`"prod-something"`, "dev_stack.max_stacks"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
