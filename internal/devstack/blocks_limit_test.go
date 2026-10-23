package devstack

import (
	"strings"
	"testing"
)

// The parallel-dev-stack ceiling closes a real incident (see DevStackConfig
// in internal/config): the allocator handed out blocks without limit, but a
// project's cluster config (control-plane's deploy/k3d.yaml) pre-maps host
// ports for only a finite block range, hand-maintained as a comment. The 8th
// worktree allocated a block cleanly, rendered ports the cluster never
// mapped, and failed much later as "gateway unreachable" — nowhere near the
// real cause. These tests lock the ceiling at the one place that can still
// refuse it: the moment a NEW block would be assigned.

// setMaxStacksLimit arms the ceiling for the duration of one test. Like the
// active worktree options (see setActiveWorktree in blocks_scope_test.go),
// this is a process global — restoring it is mandatory or a leaked value
// silently changes what later tests consider the ceiling.
func setMaxStacksLimit(t *testing.T, n int) {
	t.Helper()
	prev := MaxStacksLimit()
	SetMaxStacks(n)
	t.Cleanup(func() { SetMaxStacks(prev) })
}

// TestAllocateBlockAllowsUpToCeiling is the boundary case: with MaxStacks=8,
// blocks 0..7 are legal (8 stacks total, counting the default). The 7th
// NAMED key (block 7, the 8th stack overall) must still succeed.
func TestAllocateBlockAllowsUpToCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	setMaxStacksLimit(t, 8)

	// Blocks are 0-based and the default key "" is implicit block 0, so
	// seven NAMED keys fill blocks 1..7 — exactly up to the ceiling.
	for i := 0; i < 7; i++ {
		key := keyFor(i)
		block, err := AllocateBlock(dir, key)
		if err != nil {
			t.Fatalf("AllocateBlock(%s) at block %d (within an 8-stack ceiling): %v", key, block, err)
		}
		if block != i+1 {
			t.Fatalf("AllocateBlock(%s) = block %d, want %d", key, block, i+1)
		}
	}
}

// TestAllocateBlockRefusesPastCeiling is the core regression lock: the NEXT
// new key, which would land on block 8 (the 9th stack under an 8-stack
// ceiling), must be refused rather than silently handed a block the
// project's cluster config never pre-mapped.
func TestAllocateBlockRefusesPastCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	setMaxStacksLimit(t, 8)

	for i := 0; i < 7; i++ {
		if _, err := AllocateBlock(dir, keyFor(i)); err != nil {
			t.Fatalf("AllocateBlock(%s): %v", keyFor(i), err)
		}
	}

	const eighthKey = "wt-h" // the 8th named key -> would-be block 8
	_, err := AllocateBlock(dir, eighthKey)
	if err == nil {
		t.Fatal("AllocateBlock allocated a 9th stack past an 8-stack ceiling; " +
			"the cluster's port pre-map does not cover that block")
	}

	// The error is a runbook: it has to name the key being refused, the
	// ceiling, where it's configured, and the remedy — or a developer
	// hitting this has to go read this package's source to know what to do.
	msg := err.Error()
	for _, want := range []string{
		`"` + eighthKey + `"`,
		"8",
		"dev_stack.max_stacks",
		"forge env devstack prune",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}

	// The refused key must not have been persisted — a partial write here
	// would let a later, unrelated allocation collide with it.
	if list, _ := List(dir); len(list) != 7 {
		t.Errorf("registry has %d entries, want 7 (the refused 8th key must not be persisted): %v", len(list), list)
	}
}

// TestAllocatePortAvoidingForeignRefusesPastCeiling covers the second entry
// point, whose stepping loop has its own place a new block gets minted.
func TestAllocatePortAvoidingForeignRefusesPastCeiling(t *testing.T) {
	dir := t.TempDir()
	setMaxStacksLimit(t, 2) // only blocks 0 and 1 are legal

	// Block 0: base is busy, so this steps to block 1 — still within the
	// ceiling (blocks 0 and 1 legal for MaxStacks=2).
	freeForA := func(p int) bool { return p != 8080 }
	got, err := AllocatePortAvoidingForeign(dir, 8080, "wt-a", freeForA)
	if err != nil {
		t.Fatalf("AllocatePortAvoidingForeign at the ceiling boundary: %v", err)
	}
	if got != 8180 {
		t.Fatalf("port = %d, want 8180 (block 1, within the ceiling)", got)
	}

	// A second, distinct key: both the base AND the port wt-a just took are
	// occupied (as they would be on a real machine once wt-a is running),
	// so this key would need block 2 — past the ceiling.
	freeForB := func(p int) bool { return p != 8080 && p != 8180 }
	_, err = AllocatePortAvoidingForeign(dir, 8080, "wt-b", freeForB)
	if err == nil {
		t.Fatal("AllocatePortAvoidingForeign allocated a block past the ceiling")
	}
	if !strings.Contains(err.Error(), `"wt-b"`) {
		t.Errorf("error does not name the refused key: %v", err)
	}
}

// TestExistingBlockPastCeilingStillResolves is the CRITICAL guard: lowering
// max_stacks after blocks were already issued must NEVER retroactively move
// or refuse an existing allocation. A block is multiplied into k3d's
// pre-mapped host ports and baked into the dev IdP's `iss` claim and
// registered redirect URIs, so an already-issued key has to resolve forever,
// independent of whatever the ceiling is set to later.
func TestExistingBlockPastCeilingStillResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()

	// Allocate with NO ceiling armed — the key lands on block 9, e.g. a
	// project that raised max_stacks, issued a block, then lowered it again.
	setMaxStacksLimit(t, 0)
	const key = "wt-legacy"
	for i := 0; i < 9; i++ {
		if _, err := AllocateBlock(dir, keyFor(i)); err != nil {
			t.Fatalf("AllocateBlock(%s) unbounded: %v", keyFor(i), err)
		}
	}
	block, err := AllocateBlock(dir, key)
	if err != nil {
		t.Fatalf("AllocateBlock(%s) unbounded: %v", key, err)
	}
	if block != 10 {
		t.Fatalf("setup: %s landed on block %d, want 10", key, block)
	}
	port, err := AllocatePort(dir, 3000, key)
	if err != nil {
		t.Fatalf("AllocatePort(%s) unbounded: %v", key, err)
	}
	if port != 3000+10*100 {
		t.Fatalf("port = %d, want %d", port, 3000+10*100)
	}

	// Now the ceiling is lowered to 8 — well below block 10 — exactly the
	// "max_stacks was raised, then lowered back down" scenario.
	setMaxStacksLimit(t, 8)

	againBlock, err := AllocateBlock(dir, key)
	if err != nil {
		t.Fatalf("a LOWERED ceiling must not refuse an already-issued key: %v", err)
	}
	if againBlock != 10 {
		t.Fatalf("a lowered ceiling moved an already-issued block: %d, want 10", againBlock)
	}

	againPort, err := AllocatePort(dir, 3000, key)
	if err != nil {
		t.Fatalf("a LOWERED ceiling must not refuse an already-issued key's port: %v", err)
	}
	if againPort != port {
		t.Fatalf("a lowered ceiling moved an already-issued port: %d, want %d (this would break a live k3d host mapping / IdP iss claim)", againPort, port)
	}

	// Same guarantee through AllocatePortAvoidingForeign's own lookup path.
	viaForeign, err := AllocatePortAvoidingForeign(dir, 3000, key, func(int) bool { return true })
	if err != nil {
		t.Fatalf("AllocatePortAvoidingForeign refused an already-issued key under a lowered ceiling: %v", err)
	}
	if viaForeign != port {
		t.Fatalf("AllocatePortAvoidingForeign moved an already-issued port under a lowered ceiling: %d, want %d", viaForeign, port)
	}
}

// TestDefaultKeyNeverBlockedByCeiling: the default stack (key "") is block 0
// and must never be refused, no matter how low the ceiling — refusing block
// 0 would mean forge itself cannot render the primary checkout.
func TestDefaultKeyNeverBlockedByCeiling(t *testing.T) {
	dir := t.TempDir()
	setMaxStacksLimit(t, 1) // the tightest legal ceiling: default stack only

	if _, err := AllocatePort(dir, 3000, ""); err != nil {
		t.Fatalf("the default stack must never be refused by the ceiling: %v", err)
	}
}

// TestUnarmedCeilingPreservesTodaysBehavior: a caller that never calls
// SetMaxStacks (every existing devstack test, `forge env devstack port`,
// `forge ci`) must keep allocating without limit — the ceiling is opt-in via
// forge.yaml, not a default this package imposes on every consumer.
func TestUnarmedCeilingPreservesTodaysBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	setMaxStacksLimit(t, 0) // explicit unarmed, matching the true zero value

	for i := 0; i < 20; i++ {
		if _, err := AllocateBlock(dir, keyFor(i)); err != nil {
			t.Fatalf("AllocateBlock(%s) with no ceiling armed: %v", keyFor(i), err)
		}
	}
	if list, _ := List(dir); len(list) != 20 {
		t.Errorf("registry has %d entries, want 20 — an unarmed ceiling must not limit allocation", len(list))
	}
}
