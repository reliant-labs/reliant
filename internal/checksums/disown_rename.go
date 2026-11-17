package checksums

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ── Renames must carry a disown with them ─────────────────────────────
//
// `forge project disown` is the sanctioned one-way exit: after it, the path
// is the user's and no `WriteGeneratedFile*` call touches it again. That
// guarantee is keyed on a PATH, and forge occasionally renames an emitter's
// output (mounts_services.go → mounts_services_gen.go, pkg/config/config.go
// → config_gen.go, db/embed.go → embed_gen.go, …).
//
// A rename used to void the disown silently. The record still named the OLD
// path, so the NEW path looked unowned, and forge emitted a second file
// declaring the same symbols right beside the user's — a package that does
// not compile, produced with no error and no warning. That is the worst way
// to break the exit: not by refusing, but by generating code that collides
// with work the user was promised forge would leave alone.
//
// WHY CARRY OVER RATHER THAN REFUSE. Refusing would be defensible — forge
// could stop and tell the user to re-disown under the new name — but it
// makes forge's OWN internal bookkeeping the user's problem. The user
// disowned a FILE, meaning "this output is mine now"; which filename forge
// happens to emit it under is forge's decision, taken between two versions,
// and the user did nothing to invalidate their own intent. Refusing would
// also hard-block `forge generate` on a project that is otherwise correct,
// for a condition the user cannot see coming and forge can resolve exactly.
//
// So: the disowned FILE MOVES to the new path and the record moves with it.
// The user's bytes are preserved verbatim; only the name changes, which is
// precisely the change forge made. Afterwards the new path is disowned, so
// the emitter's write skips it exactly as it would have before the rename —
// no collision is possible, because the emitter's target path is the one
// holding the user's file. It is loud (DisownCarryNoticeFn) because a file
// changing name under the user is a thing they must be told.
//
// The move is SAFE-ONLY: if something already occupies the new path, forge
// does not overwrite it. See CarryDisownAcrossRename.

// DisownCarryNoticeFn is invoked once per disown record carried across an
// emitter rename. Package var so the CLI can capture the report; the
// default prints to stderr. Never nil it out — assign a no-op in tests.
var DisownCarryNoticeFn = func(oldRel, newRel string) {
	fmt.Fprintf(os.Stderr,
		"📦 disowned file renamed: %s → %s — forge renamed this generated output; your disowned copy moved with it and is still yours. Nothing was overwritten.\n",
		oldRel, newRel)
}

// blockedThisRun is the set of paths a refused disown carry-over has taken
// off the table for the rest of this run.
//
// It is what makes REFUSAL actually safe. Reporting the conflict is not
// enough on its own: the emitter calls the retirement helper and then writes
// its new path regardless, and that write is exactly the collision this
// whole mechanism exists to prevent — a fresh render landing beside the
// user's disowned copy, both declaring the same symbols. So a refusal also
// BLOCKS the write. The user is left with the two files they already had
// (nothing destroyed, nothing added) plus a runbook naming the fix.
var blockedThisRun = map[string]bool{}

// blockWrite takes relPath off the table for the rest of this run.
func blockWrite(relPath string) { blockedThisRun[filepath.ToSlash(relPath)] = true }

// WriteBlocked reports whether a refused carry-over has blocked relPath.
// Consulted by the Tier-1 write chokepoint.
func WriteBlocked(relPath string) bool { return blockedThisRun[filepath.ToSlash(relPath)] }

// ResetWriteBlocks clears the per-run block set. Called at the start of a
// generate run (and by tests) so a refusal never leaks between runs.
func ResetWriteBlocks() { blockedThisRun = map[string]bool{} }

// DisownCarryConflictFn is invoked when a disowned old path cannot be
// carried because the new path is already occupied by a file forge may not
// remove. This is the one case forge cannot resolve alone, so it is
// reported as a runbook rather than resolved silently.
var DisownCarryConflictFn = func(oldRel, newRel string) {
	fmt.Fprintf(os.Stderr,
		"⚠️  disowned %s could not be carried to its new name %s: that path already exists and is not forge's to replace.\n"+
			"    Both files may declare the same symbols. Resolve by keeping ONE:\n"+
			"      - keep yours:  mv %s %s && forge project disown %s\n"+
			"      - drop yours:  rm %s   (forge then regenerates %s)\n",
		oldRel, newRel, oldRel, newRel, newRel, oldRel, newRel)
}

// refuseCarry is the single refusal path: report the runbook AND block the
// write, so the emitter cannot proceed to lay a fresh render beside the
// user's disowned copy.
func refuseCarry(oldRel, newRel string) {
	blockWrite(newRel)
	if DisownCarryConflictFn != nil {
		DisownCarryConflictFn(oldRel, newRel)
	}
}

// CarryDisownAcrossRename moves a disown record — and the user's file with
// it — from oldRel to newRel when forge has renamed an emitter's output.
//
// It is called by the rename-retirement helpers BEFORE the emitter writes
// newRel, which is what makes a collision impossible: by the time the write
// happens, newRel is a disowned path and the writer skips it.
//
// Returns true when a carry-over happened (the caller must then NOT treat
// oldRel as a reclaimable forge file).
//
// The safety rules, in order:
//
//   - No disown on oldRel → nothing to carry; the normal retirement path
//     (reclaim forge's own pristine copy) applies.
//   - oldRel is already gone → the record alone is re-keyed. The user
//     deleted their copy, which is the documented re-adoption gesture; the
//     stale record must not keep pointing at a path that no longer exists.
//   - newRel already disowned → the user has already taken ownership under
//     the new name too. Drop the old record and leave both files alone.
//   - newRel occupied by forge's own PRISTINE render → that copy is forge's
//     to remove (same test the retirement path uses), so remove it and move
//     the user's file in.
//   - newRel occupied by anything else → REFUSE, loudly, via
//     DisownCarryConflictFn. Forge will not overwrite a file it did not
//     write, and the old record is KEPT so the user's copy stays protected.
func CarryDisownAcrossRename(root string, cs *FileChecksums, oldRel, newRel string) bool {
	if root == "" || cs == nil {
		return false
	}
	oldRel = filepath.ToSlash(oldRel)
	newRel = filepath.ToSlash(newRel)
	if oldRel == newRel {
		return false
	}
	return cs.carryDisownIn(root, oldRel, newRel)
}

// carryDisownIn is the *FileChecksums-scoped worker. Split out so the
// exported entry point can stay a thin, path-only signature for the
// emitters while the tracker plumbing lives in one place.
func (cs *FileChecksums) carryDisownIn(root, oldRel, newRel string) bool {
	if cs == nil || !cs.IsDisowned(oldRel) {
		return false
	}

	oldFull := filepath.Join(root, filepath.FromSlash(oldRel))
	newFull := filepath.Join(root, filepath.FromSlash(newRel))

	entry := cs.Disowned[oldRel]

	// The user already disowned the new name as well — both files are
	// theirs. The old record is redundant; drop it and touch nothing.
	if cs.IsDisowned(newRel) {
		delete(cs.Disowned, oldRel)
		return true
	}

	oldInfo, oldErr := os.Stat(oldFull)
	if oldErr != nil || oldInfo.IsDir() {
		// The user's copy is gone: re-adoption by deletion. Drop the record
		// so the emitter writes the new name cleanly.
		delete(cs.Disowned, oldRel)
		return false
	}

	// Decide what to do about whatever sits at the new path.
	if _, err := os.Stat(newFull); err == nil {
		body, readErr := os.ReadFile(newFull)
		reclaimable := readErr == nil && Verify(body) == Pristine
		if !reclaimable {
			// Not forge's to replace. Refuse loudly and keep the user's
			// protection on the old path.
			refuseCarry(oldRel, newRel)
			return true // still "handled": the caller must not delete oldRel
		}
		recordPreWrite(root, newRel)
		if rmErr := os.Remove(newFull); rmErr != nil {
			refuseCarry(oldRel, newRel)
			return true
		}
	} else if !os.IsNotExist(err) {
		refuseCarry(oldRel, newRel)
		return true
	}

	// Journal BOTH endpoints before mutating, so a failed run rewinds to a
	// tree where the user's file is back under its original name.
	recordPreWrite(root, oldRel)
	recordPreWrite(root, newRel)

	if err := os.MkdirAll(filepath.Dir(newFull), 0o755); err != nil {
		refuseCarry(oldRel, newRel)
		return true
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		// Fall back to copy+remove (rename fails across filesystems).
		body, readErr := os.ReadFile(oldFull)
		if readErr != nil {
			refuseCarry(oldRel, newRel)
			return true
		}
		if writeErr := atomicfile.WriteFile(newFull, body, oldInfo.Mode().Perm()); writeErr != nil {
			refuseCarry(oldRel, newRel)
			return true
		}
		_ = os.Remove(oldFull)
	}

	delete(cs.Disowned, oldRel)
	cs.Disowned[newRel] = entry

	// Forge adjudicated both paths this run — keep the stale-artifact
	// sweep away from the file it just moved.
	MarkWrittenThisRun(newRel)
	MarkWrittenThisRun(oldRel)

	if DisownCarryNoticeFn != nil {
		DisownCarryNoticeFn(oldRel, newRel)
	}
	return true
}
