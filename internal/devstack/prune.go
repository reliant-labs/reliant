package devstack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// PrunedBlock describes one registry entry Prune reclaimed (or, in dry-run
// mode, would reclaim).
type PrunedBlock struct {
	Key   string
	Index int
	// Worktree is the gone worktree that made this entry reclaimable: the
	// key itself for a dev-stack key, or the recorded origin for a key
	// derived from one. Never empty — an entry tied to no worktree is never
	// reclaimable.
	Worktree string
}

// Prune reclaims registry entries whose WORKTREE no longer exists on disk,
// and returns what it reclaimed (or, when dryRun is true, what it WOULD
// reclaim, without writing anything).
//
// An entry is reclaimable when it names a worktree and that worktree is gone.
// Two kinds of entry name one, and the second is the one this used to miss:
//
//   - A DEV-STACK key (Stack == true) — the key IS the worktree name.
//   - A DERIVED key (Origin != "") — the key was COMPOSED from a worktree,
//     e.g. prod's web port keying on `"prod-" + option("worktree")`, so
//     "prod-cp-obs" belongs to worktree "cp-obs" just as surely as a stack key
//     does. Prune used to skip every non-stack key on the assumption that it
//     was "not tied to any worktree", and for a derived key that assumption is
//     simply false: delete the worktree and the block was held forever. Since
//     the ceiling counts BLOCKS, enough of those make a new stack impossible to
//     allocate at all — three stranded blocks out of eight is what blocked
//     `forge env render prod` in control-plane.
//
// Two things are NEVER touched, unconditionally, regardless of what git
// reports:
//
//   - A STANDALONE port-block key (Stack == false, Origin == "") — e.g.
//     "prod", allocated from the primary checkout where there is no worktree
//     to derive from. It is tied to no worktree, so it can never legitimately
//     look dead, and reclaiming it would silently move a live stack's port.
//     Note the discriminator is the RECORDED origin, never the key's shape:
//     "prod" and "prod-cp-obs" are told apart by what allocation observed, not
//     by how composed the name looks. That is the whole reason Origin is a
//     field (see blocks.go's entry doc) — a standalone key can look every bit
//     as composed as a derived one, and guessing wrong moves a live port.
//   - The default key "" (block 0). It is implicit and never stored by any
//     normal write path, but Prune checks for it explicitly rather than
//     relying on that invariant holding forever.
//
// Liveness is determined by enumerating real worktrees via
// `git worktree list --porcelain` and comparing the entry's worktree against
// the SANITIZED basename of every surviving worktree root — the exact
// derivation Worktree() uses, so a key that genuinely still backs a live
// worktree is never mistaken for dead.
//
// If that enumeration fails for ANY reason (git missing, projectDir is not a
// git checkout, the command errors), Prune reclaims NOTHING and returns the
// error. A failed enumeration gives no way to tell live from dead, and
// deleting a block that is actually still live would move a running stack's
// ports out from under it — the one mistake this command must never make.
//
// The registry mutation happens under the same lock AllocateBlock uses
// (withLock), so a concurrent `forge env up` claiming a new block cannot
// race a prune.
func Prune(projectDir string, dryRun bool) ([]PrunedBlock, error) {
	liveKeys, err := liveWorktreeKeys(projectDir)
	if err != nil {
		return nil, fmt.Errorf("enumerate live worktrees (reclaiming nothing): %w", err)
	}

	var pruned []PrunedBlock
	err = withLock(projectDir, func() error {
		reg, err := readRegistry(projectDir)
		if err != nil {
			return err
		}
		changed := false
		for key, e := range reg {
			worktree := entryWorktree(key, e)
			if worktree == "" {
				// Tied to no worktree: the default block, or a standalone
				// port-block key (e.g. "prod"). Nothing on disk could ever
				// make it dead.
				continue
			}
			if liveKeys[worktree] {
				continue // worktree still exists
			}
			pruned = append(pruned, PrunedBlock{Key: key, Index: e.Block, Worktree: worktree})
			if !dryRun {
				delete(reg, key)
				changed = true
			}
		}
		if dryRun || !changed {
			return nil
		}
		return writeRegistry(projectDir, reg)
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pruned, func(a, b int) bool { return pruned[a].Index < pruned[b].Index })
	return pruned, nil
}

// UnattributedBlock is a block forge cannot classify: a non-stack entry with
// no recorded origin. It is never reclaimed — it is REPORTED, so a human can
// decide.
//
// This exists because the conservative migration has a terminal case. An entry
// written before entry.Origin existed learns its origin from the next render
// that allocates the key from its own worktree — but if that worktree is
// ALREADY gone, no such render will ever happen, and the block is stranded not
// just until the next run but permanently. That is exactly the state
// control-plane was in: three pre-origin entries ("prod-cp204-fix-st",
// "prod-cp-litellm", "prod-cp-obs") whose worktrees had been deleted long
// before forge recorded origins at all.
//
// Inferring their origin from their names is the thing the origin field exists
// to avoid, and the asymmetry has not changed: guessing wrong moves a live
// stack's ports. So forge does not guess. It reports what it cannot prove, with
// whatever evidence it does have, and leaves the decision — via
// [ReleaseKey] — to the person who knows which worktrees they deleted.
type UnattributedBlock struct {
	Key   string
	Index int
	// LikelyLiveOrigin names a LIVE worktree that appears as a segment of
	// Key, when one does. It is a strong signal the entry is derived and
	// still in use — i.e. exactly what you must NOT release. Empty when no
	// live worktree matches, which is the case worth a human's attention.
	LikelyLiveOrigin string
}

// Unattributed reports the blocks forge cannot classify — non-stack entries
// with no recorded origin — so a human can see what the conservative migration
// left behind instead of it silently occupying the ceiling forever. It never
// mutates anything.
//
// A caller that wants to act on one uses [ReleaseKey], which requires naming
// the key explicitly.
func Unattributed(projectDir string) ([]UnattributedBlock, error) {
	liveKeys, err := liveWorktreeKeys(projectDir)
	if err != nil {
		return nil, fmt.Errorf("enumerate live worktrees: %w", err)
	}
	reg, err := readRegistry(projectDir)
	if err != nil {
		return nil, err
	}
	var out []UnattributedBlock
	for key, e := range reg {
		if key == "" || e.Stack || e.Origin != "" {
			continue // classified: default, a stack, or origin recorded
		}
		u := UnattributedBlock{Key: key, Index: e.Block}
		for live := range liveKeys {
			if keyHasSegment(key, live) {
				u.LikelyLiveOrigin = live
				break
			}
		}
		out = append(out, u)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Index < out[b].Index })
	return out, nil
}

// ReleaseKey reclaims the block held by exactly the named key, on the caller's
// explicit assertion that it is no longer in use. It is the manual counterpart
// to Prune: Prune acts on evidence forge recorded itself and will never touch
// an entry it cannot prove is dead, while this acts on a human's assertion
// about an entry forge has no evidence for.
//
// Keeping the two separate is the point. Folding "release what I assert" into
// Prune would make a command people run to SEE what accumulated capable of
// moving a live stack's ports, which is the one mistake this whole area must
// not make. Here the caller names the key, so nothing is inferred and nothing
// is swept up alongside it.
//
// The default key "" is refused: block 0 is the primary checkout's implicit
// block and releasing it is meaningless. A key with no entry is refused too,
// rather than silently succeeding, so a typo does not read as success.
//
// Returns the released block index.
func ReleaseKey(projectDir, key string) (int, error) {
	if key == "" {
		return 0, fmt.Errorf("refusing to release the default key \"\": block 0 is the primary checkout's implicit block")
	}
	var index int
	err := withLock(projectDir, func() error {
		reg, err := readRegistry(projectDir)
		if err != nil {
			return err
		}
		e, ok := reg[key]
		if !ok {
			return fmt.Errorf("no block is registered for key %q", key)
		}
		index = e.Block
		delete(reg, key)
		return writeRegistry(projectDir, reg)
	})
	if err != nil {
		return 0, err
	}
	return index, nil
}

// entryWorktree returns the worktree whose disappearance would make this entry
// reclaimable, or "" when the entry is tied to no worktree and is therefore
// never reclaimable.
//
// The default key is excluded explicitly rather than by relying on the
// invariant that it is never stored with a stack flag or an origin.
func entryWorktree(key string, e entry) string {
	if key == "" {
		return ""
	}
	if e.Stack {
		return key // a stack key IS its worktree's name
	}
	return e.Origin // "" for a standalone key
}

// liveWorktreeKeys returns the set of registry keys that `git worktree list
// --porcelain` reports as currently present for projectDir — the exact key
// Worktree() would derive for each one.
//
// IT MUST DERIVE THE KEY THE SAME WAY Worktree() DOES, INCLUDING disambiguate.
// This used to be Sanitize(filepath.Base(root)) alone, and the divergence was
// not theoretical — it made prune offer to reclaim a LIVE stack's block, the
// single mistake this command must never make.
//
// The nested-repo layout is where it breaks. Every worktree is created as
// <container>/<repo>, so the container carries the distinguishing name and the
// basenames all collide:
//
//	~/.reliant/worktrees/reliant-labs/my-new-feature-50f77334/control-plane
//	~/.reliant/worktrees/reliant-labs/newtool-5709b18d/control-plane
//
// Both have basename "control-plane". Worktree() already knows this — that is
// exactly what disambiguate() is for, and it keys those stacks on
// "my-new-feature-50f77334" and "newtool-5709b18d". But the live set built from
// bare basenames contained only "control-plane", so EVERY such stack key looked
// dead. Observed on a real machine: prune reported it would reclaim
// "my-new-feature-50f77334" while that worktree was sitting on disk, running.
//
// Deriving through the same function both directions is what keeps the two from
// drifting again. A key that Worktree() would produce is a key prune recognizes,
// by construction rather than by two implementations agreeing.
//
// A worktree git itself marks "prunable" (its administrative record
// survives in .git/worktrees/<name> but the working directory is gone) is
// deliberately EXCLUDED from the live set: "prunable" is git's own word for
// the condition this function exists to detect. As a second, independent
// check — because a "prunable" annotation depends on git's own bookkeeping
// noticing the directory is gone, which is exactly the kind of implicit
// state this whole registry-lifecycle gap was about — a worktree root that
// no longer os.Stat()s is also excluded even if git hasn't yet marked it
// prunable.
func liveWorktreeKeys(projectDir string) (map[string]bool, error) {
	out, err := gitWorktreeListPorcelain(projectDir)
	if err != nil {
		return nil, err
	}

	keys := make(map[string]bool)
	var currentRoot string
	prunable := false
	flush := func() {
		if currentRoot != "" && !prunable {
			if _, statErr := os.Stat(currentRoot); statErr == nil {
				for _, key := range candidateKeys(currentRoot) {
					keys[key] = true
				}
			}
		}
		currentRoot = ""
		prunable = false
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			currentRoot = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "prunable"):
			prunable = true
		case line == "":
			flush()
		}
	}
	flush()
	return keys, nil
}

// candidateKeys returns EVERY key a live worktree at root could be registered
// under: the key Worktree() derives today, plus the other forms disambiguate()
// could have produced when the registration happened.
//
// A live worktree gets protection from all of them, and that breadth is the
// point. disambiguate() is not STABLE over time — it returns the bare basename
// when that basename is unique among live worktrees, and the container name
// only while a sibling collides. So a stack registered as "wt-feature" while a
// sibling existed re-derives to "control-plane" the moment that sibling is
// deleted, and a live set holding only today's derivation would declare the
// running stack dead and offer to move its ports.
//
// Being generous here is safe in the only direction that matters. An extra key
// in the live set can only ever PREVENT a reclaim; it can never cause one. The
// cost of a superfluous entry is that a genuinely dead block survives one more
// prune — recoverable, and visible in the report. The cost of a missing entry
// is a live stack's ports moving underneath it, which is not recoverable by
// re-running anything.
//
// Note this is NOT the same judgement as inferring an origin from a key's name:
// there, forge would be inventing an association it never observed in order to
// DELETE something. Here it is enumerating the forms of a worktree that
// demonstrably exists on disk, in order to PROTECT it.
func candidateKeys(root string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	// Today's authoritative derivation, disambiguation included.
	add(Worktree(root))
	// The bare basename: what disambiguate() returns whenever this worktree's
	// basename is unique, which it becomes as soon as colliding siblings go.
	add(Sanitize(filepath.Base(root)))
	// The container directory: what disambiguate() returns while a sibling
	// DOES collide — the nested <container>/<repo> layout's real name.
	add(Sanitize(filepath.Base(filepath.Dir(root))))
	// git's own per-worktree admin dir name, disambiguate()'s last resort.
	add(Sanitize(filepath.Base(gitOut(root, "rev-parse", "--absolute-git-dir"))))
	return out
}

func gitWorktreeListPorcelain(projectDir string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", "worktree", "list", "--porcelain")
	if projectDir != "" {
		cmd.Dir = projectDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git worktree list --porcelain (in %s): %w", projectDir, err)
	}
	return string(out), nil
}

// FirstAllocationNotice is the one-line diagnostic forge should print the
// FIRST time a key is assigned a port block — the moment AllocateBlock
// actually mutates .forge/blocks.json rather than just reading an existing
// entry.
//
// Why this needs to exist at all: a caller like `forge env render` looks
// like a pure query, but rendering a new env for the first time allocates a
// REAL block through this same path — that silent write is exactly how the
// key "prod" appeared in control-plane's registry with nobody aware a write
// had happened. The write itself is legitimate and must stay (render has to
// produce the ports `up` will really use), so this only makes it visible,
// it does not gate it.
//
// Call it at the exact point AllocateBlock decides a key has NO existing
// entry and is about to assign it the next free block — i.e. right where
// `block = nextFreeBlock(reg)` runs, before `writeRegistry` — with:
//
//	fmt.Fprintln(os.Stderr, FirstAllocationNotice(key, block))
func FirstAllocationNotice(key string, block int) string {
	return fmt.Sprintf(
		"forge: allocated new port block %d for key %q (.forge/blocks.json) — "+
			"first time this key was seen; its ports are now offset by +%d00",
		block, key, block,
	)
}
