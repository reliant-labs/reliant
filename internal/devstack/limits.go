package devstack

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// The parallel-dev-stack ceiling armed for this process's subsequent
// allocations — see [SetMaxStacks]. It is a process-global, mirroring the
// Active/SetActive git-facts pair in options.go and the blockAlloc /
// UseBlockAllocator hook in internal/kclplugin: one process-wide value set
// ONCE before the first render, read by every allocation that follows.
//
// The zero value (0) is UNARMED, meaning unbounded — exactly the behavior
// every allocation had before this ceiling existed. That is deliberate, not
// just a convenient zero value: this package has no opinion on forge.yaml or
// its dev_stack.max_stacks default, so every existing devstack test, plus
// any caller that never calls SetMaxStacks (`forge env devstack port`,
// `forge ci`, `forge generate`), keeps allocating without limit exactly as
// before this change. Only the up/deploy render path
// (internal/cli/devstack_activate.go) arms a concrete ceiling, resolved from
// forge.yaml via config.DevStackConfig.EffectiveMaxStacks().
var (
	maxStacksMu sync.RWMutex
	maxStacks   int
)

// SetMaxStacks arms the ceiling for this process's subsequent NEW block
// allocations. n <= 0 disarms it (unbounded). Call once, before the first
// allocation, on the up/deploy path only — the same discipline SetActive
// already requires of its callers.
func SetMaxStacks(n int) {
	maxStacksMu.Lock()
	maxStacks = n
	maxStacksMu.Unlock()
}

// MaxStacksLimit returns the armed ceiling (0 meaning unbounded/unarmed). A
// project's cluster port pre-map (e.g. k3d.yaml's hand-mapped host-port
// range) should be derived from this SAME number rather than hand-
// maintained separately — the duplication between an unbounded allocator and
// a hand-maintained pre-map comment is the root cause this ceiling exists to
// close (see DevStackConfig in internal/config).
func MaxStacksLimit() int {
	maxStacksMu.RLock()
	defer maxStacksMu.RUnlock()
	return maxStacks
}

// checkCeiling refuses a NEW block at or past the armed ceiling. It must
// NEVER be consulted for a key that already holds a registry entry — an
// already-issued block resolves to its recorded port forever, regardless of
// where MaxStacks is set later. AllocateBlock and AllocatePortAvoidingForeign
// both return via lookupBlock, before ever reaching this, for exactly that
// reason.
//
// reg is the registry the decision was made against, and it is here for the
// MESSAGE, not the rule. The ceiling counts BLOCKS — every block maps to a
// pre-mapped host-port range whether a worktree or a plain port-block key
// holds it — but the message used to call them all "stacks" and send the
// reader to `forge env devstack list`, which prints ONLY stack keys.
//
// Observed in control-plane: the ceiling reported "8 stacks already
// registered" while `devstack list` printed exactly one, because the other
// six blocks were "prod", "prod-cp-obs" and friends — port-block keys that
// list deliberately hides and that `prune` will never reclaim. Both remedies
// in the message were therefore dead ends, in a message whose whole purpose
// is to be a runbook. It now reports the real composition and says which part
// prune can actually touch. A nil reg falls back to counts only.
func checkCeiling(key string, block int, reg registry) error {
	limit := MaxStacksLimit()
	if limit <= 0 || block < limit {
		return nil
	}
	return fmt.Errorf(
		"refusing to allocate a NEW port block for key %q: this repo has already reached the "+
			"%d-block ceiling (dev_stack.max_stacks in forge.yaml), counting the blocks already "+
			"registered in .forge/blocks.json at the primary checkout.\n"+
			"\n"+
			"%s"+
			"A block multiplies into a project's cluster port pre-map (e.g. k3d.yaml's hand-mapped\n"+
			"host-port range) and into the dev IdP's baked-in `iss` claim and registered redirect URIs.\n"+
			"Handing out one past the ceiling is how a stack allocates cleanly here and then fails much\n"+
			"later as an unrelated \"gateway unreachable\" — nowhere near this cause.\n"+
			"\n"+
			"NOTE: the ceiling counts BLOCKS, not just dev stacks. A port-block key (a prod web port,\n"+
			"say) consumes a block exactly like a worktree does. What decides whether prune can reclaim\n"+
			"one is not its kind but whether it is TIED TO A WORKTREE: a derived key like\n"+
			"\"prod-<worktree>\" is reclaimed once that worktree is gone, while a standalone key like\n"+
			"\"prod\" never is. The listing above says which each holder is.\n"+
			"\n"+
			"Remedy, in order:\n"+
			"  1. `forge env devstack list` shows every holder and what it is tied to. If a holder above\n"+
			"     names a worktree you have finished with, remove the worktree\n"+
			"     (`git worktree remove <path>`) and run `forge env devstack prune` to reclaim its\n"+
			"     block — and every derived key's block along with it (`--apply` to actually rewrite).\n"+
			"  2. If the blocks are genuinely all still in use, this is NOT a stale-state problem and\n"+
			"     pruning will reclaim nothing — you need a wider ceiling. Raise dev_stack.max_stacks in\n"+
			"     forge.yaml, then run `forge generate` to REGENERATE the cluster port pre-map for the\n"+
			"     new range, then recreate the cluster so it forwards the new host ports. Raising the\n"+
			"     number alone renders ports the cluster never mapped",
		key, limit, blockHolders(reg))
}

// blockHolders renders the current block holders for the ceiling message,
// labelling each so the reader can tell at a glance which ones `prune` could
// ever reclaim from which it could not.
//
// It shares Block.Kind() with `forge env devstack list` deliberately. The
// defect this message was written to fix was the two disagreeing — the ceiling
// naming eight holders while list printed one — so the fix only holds if there
// is one labelling, not two that must be kept in step by hand.
func blockHolders(reg registry) string {
	if len(reg) == 0 {
		return ""
	}
	holders := make([]Block, 0, len(reg))
	reclaimable := 0
	for k, e := range reg {
		holders = append(holders, Block{Key: k, Index: e.Block, Stack: e.Stack, Origin: e.Origin})
		if entryWorktree(k, e) != "" {
			reclaimable++
		}
	}
	sort.Slice(holders, func(a, b int) bool { return holders[a].Index < holders[b].Index })

	var b strings.Builder
	fmt.Fprintf(&b, "The %d blocks in use — %d tied to a worktree (prune can reclaim once it is gone), %d standalone:\n",
		len(holders), reclaimable, len(holders)-reclaimable)
	for _, h := range holders {
		fmt.Fprintf(&b, "  block %d: %-28s %s\n", h.Index, h.DisplayKey(), h.Kind())
	}
	b.WriteString("\n")
	return b.String()
}
