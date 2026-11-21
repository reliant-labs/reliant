// Stage-then-validate rollback journal for `forge generate`.
//
// FRICTION cp-forge fr-40f7ec9bd9: `forge generate --force` on a clean
// clone rewrote Tier-1 files across the whole tree (CI workflows, skills,
// mocks, ORM, KCL), ran `go mod tidy`, and only THEN failed the final
// "go build (validate generated code)" step — exiting non-zero with the
// tree left mid-regen and recovery left to the user's `git checkout`.
// A generate run that fails its own validation must not leave the tree
// in a state the user has to hand-repair.
//
// The fix is a write journal recorded at the SINGLE chokepoint every
// forge write flows through (WriteGeneratedFile / WriteScaffoldIfMissing
// / writeUnstampable, plus the in-place restamp and disown marker-strip).
// Before any of those mutate a path on disk, the journal captures the
// path's EXACT pre-run bytes (or records that it did not exist). On a
// post-write failure — most importantly the final `go build` validate —
// the pipeline calls RestoreRollback, which rewrites every journaled path
// back to its captured pre-run state (re-creating, overwriting, or
// deleting as needed). On success the pipeline calls CommitRollback,
// which simply drops the journal.
//
// Scope — every file the run writes, by any route it declares:
//
//   - forge's own writers journal each file they touch this run (Tier-1
//     codegen, scaffold-once "yours" files, comment-incapable outputs,
//     restamps, disown marker strips). That is the "mid-regen broken tree"
//     the friction names.
//   - A step that runs an EXTERNAL tool (`buf generate` into gen/, `go mod
//     tidy` on go.mod/go.sum, protoc-gen-connect-openapi) declares the
//     paths that tool may write, and the pipeline captures them with
//     CaptureExternalWrites immediately before the tool runs. These used to
//     be left out on the theory that tool output is "deterministic from the
//     inputs". It is not: a failed control-plane run left 32 buf outputs
//     modified while reporting the tree "back to its pre-run state" — some
//     carried a different protoc-gen-go version header (the local plugin is
//     not the one that produced the committed stubs), others were
//     regenerated from another agent's uncommitted proto. A revert that
//     skips them does not restore the pre-run tree.
//   - It does NOT snapshot the whole working tree. Restoring paths a run
//     never declared would also revert edits another process made to them
//     DURING the run — another agent in a shared checkout, an editor. What
//     is outside the declared set is verified rather than restored: the
//     generate pipeline compares the tree to a pre-run fingerprint and names
//     anything else that changed instead of claiming a restore it did not
//     perform.
//   - goimports/restamp rewrite files that are THEMSELVES already in the
//     journal (forge wrote them earlier this run), so restoring the
//     journal also undoes those in-place rewrites.
//   - A DECLARED path can be edited by another writer too, after forge
//     wrote it: an agent editing a hand-owned file the run also touched.
//     Its pre-run bytes alone cannot tell that edit from forge's own
//     output, and a failed run once reverted 617 files, one of them a
//     helper another agent had just written, which broke the build for
//     everyone (forge#2267). So the journal also records what forge LAST
//     LEFT at each path: at the write chokepoint right after the write
//     (notePostWrite), and for every other write route at the end of each
//     pipeline step (SettleJournal). A restore rewinds only a path still
//     in that state, and spares and names one that is not. A file an
//     external tool created under a captured directory is deleted only
//     when it is the one the step left there.
//
// The journal is process-global (like the rest of this package's per-run
// state) and reset by BeginRollbackJournal at the head of each pipeline
// run. Non-pipeline callers (forge project upgrade, project creation) never call
// Begin, so journaling stays OFF and their writes are recorded nowhere —
// they have their own recovery stories.
package checksums

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// rollbackEntry is one journaled path's pre-run state. existed=false
// means the path was absent before forge first wrote it this run, so the
// restore deletes it; existed=true means content holds the exact bytes to
// restore (with the captured file mode).
type rollbackEntry struct {
	existed bool
	content []byte
	mode    os.FileMode
	// external is true for a path captured AHEAD of an external tool
	// (CaptureExternalWrites) that no forge writer has targeted since. The
	// tool may never write it at all, so it counts as written — in the
	// write ledger and in the restored list — only when its bytes changed.
	external bool
	// left is what this run's writers last left at the path: its bytes'
	// digest, or that they removed it. Set at the chokepoint right after a
	// write (notePostWrite) and at each settle point (SettleJournal). A
	// restore rewinds only a path still in that state; one that differs
	// was changed by another writer after forge's write, and is spared.
	left forgeLeft
	// unverifiable marks a path whose pre-run state could not be captured
	// faithfully: it existed but could not be read, or was not a regular
	// file (a symlink, a directory). A restore never rewrites or removes
	// it: it is left as is and reported (forge#2329 review).
	unverifiable bool
}

// forgeLeft is the state this run's writers last left a path in: absent, or
// a regular file with these bytes. known=false when it could not be
// observed, or was not a regular file, and then nothing can show the path
// is still forge's.
type forgeLeft struct {
	known  bool
	absent bool
	// dirAbsent: the path was absent and so was its parent directory —
	// forge's own removal pruned it. Only then may a restore recreate a
	// missing parent; one removed after forge's step is another writer's.
	dirAbsent bool
	sum       [sha256.Size]byte
}

// pathKind is what a path is on disk, observed WITHOUT following a symlink.
type pathKind int

const (
	kindAbsent     pathKind = iota // Lstat says it does not exist
	kindRegular                    // a regular file, read
	kindOther                      // a symlink, directory, device, ...
	kindUnreadable                 // exists (or Lstat failed otherwise) but could not be read
)

// observe reads full's kind and, for a regular file, its bytes. Absence is
// reported only when Lstat confirms it; a symlink is never followed.
func observe(full string) (pathKind, []byte) {
	info, err := os.Lstat(full)
	switch {
	case os.IsNotExist(err):
		return kindAbsent, nil
	case err != nil:
		return kindUnreadable, nil
	case !info.Mode().IsRegular():
		return kindOther, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return kindUnreadable, nil
	}
	return kindRegular, data
}

// rollbackJournal is the per-run capture set: relPath -> pre-run state.
// nil when journaling is OFF (the default — non-pipeline callers never
// enable it). Populated lazily: each path is captured exactly once, on
// the first write that targets it this run.
var rollbackJournal map[string]rollbackEntry

// rollbackRoot is the project root the current journal is armed for.
// Needed by RecordPreWriteAbs, whose call sites (the scaffold-once raw
// writers in internal/codegen) address files by a single joined path and
// have no separate (root, relPath) pair to hand us. Set by
// BeginRollbackJournal; cleared with the journal.
var rollbackRoot string

// rollbackTrees are the declared external-tool output paths captured this
// run (project-relative, slash form). Every regular file that existed under
// one when it was captured is in the journal, so on restore a file under a
// tree that is NOT in the journal was created during the run, and is
// deleted.
var rollbackTrees map[string]bool

// rollbackTreeIDs is the directory each captured tree was: at capture, or at
// the settle point after the tool created or swapped it in. A tree that is
// no longer that directory — replaced by another one, by a symlink, or
// reached through a symlinked parent — is another writer's, and nothing
// beneath it is walked or deleted (forge#2329 review).
var rollbackTreeIDs map[string]os.FileInfo

// rollbackDirty are the journaled paths written since the last settle point
// whose post-write state is not recorded yet, and rollbackDirtyTrees the
// captured directories an external tool may have written into since then.
// SettleJournal records their current state as forge's and clears both.
var (
	rollbackDirty      map[string]bool
	rollbackDirtyTrees map[string]bool
)

// rollbackCreated is the digest of each file an external tool created under
// a captured directory, as the step that created it left it. A file there
// that is not in it, or no longer matches, is another writer's.
var rollbackCreated map[string][sha256.Size]byte

// BeginRollbackJournal turns journaling ON and clears any prior capture.
// Called once at the head of a `forge generate` run, before any writer
// fires, with the project root the run operates on. After this, every
// forge write captures its target's pre-run state (once per path) so
// RestoreRollback can undo the whole run.
func BeginRollbackJournal(root string) {
	resetScaffoldBirths()
	rollbackJournal = map[string]rollbackEntry{}
	rollbackTrees = map[string]bool{}
	rollbackTreeIDs = map[string]os.FileInfo{}
	rollbackDirty = map[string]bool{}
	rollbackDirtyTrees = map[string]bool{}
	rollbackCreated = map[string][sha256.Size]byte{}
	if abs, err := filepath.Abs(root); err == nil {
		rollbackRoot = abs
	} else {
		rollbackRoot = root
	}
}

// CommitRollback drops the journal without restoring anything — the
// run's writes stand. Called on the success path; also turns journaling
// back OFF so a subsequent non-pipeline write in the same process isn't
// silently recorded.
func CommitRollback() {
	resetScaffoldBirths()
	clearRollbackState()
}

// clearRollbackState drops the journal and everything kept beside it.
func clearRollbackState() {
	rollbackJournal = nil
	rollbackTrees = nil
	rollbackTreeIDs = nil
	rollbackDirty = nil
	rollbackDirtyTrees = nil
	rollbackCreated = nil
	rollbackRoot = ""
}

// RollbackEnabled reports whether journaling is currently ON. Exposed so
// tests can assert the pipeline armed/disarmed it correctly.
func RollbackEnabled() bool { return rollbackJournal != nil }

// recordPreWrite captures relPath's current on-disk state into the
// journal, exactly once. No-op when journaling is OFF, or when this path
// was already captured this run (the FIRST capture holds the true pre-run
// bytes; later writes this run are forge's own and must not overwrite the
// baseline). Capture failures are swallowed: a path we cannot read is one
// we cannot faithfully restore, and journaling must never itself abort a
// write — the pipeline's own error handling owns the failure surface.
func recordPreWrite(root, relPath string) {
	if rollbackJournal == nil {
		return
	}
	relPath = slashKey(relPath)
	rollbackDirty[relPath] = true
	if entry, seen := rollbackJournal[relPath]; seen {
		// Already captured — possibly ahead of an external tool. The
		// first capture holds the pre-run bytes either way; a forge
		// writer targeting it now makes it a write this run made.
		if entry.external {
			entry.external = false
			rollbackJournal[relPath] = entry
		}
		return
	}
	captureEntry(root, relPath, false)
}

// captureEntry records relPath's current on-disk state. external marks a
// capture made ahead of an external tool rather than at a forge write.
func captureEntry(root, relPath string, external bool) {
	full := filepath.Join(root, relPath)
	kind, content := observe(full)
	switch kind {
	case kindAbsent:
		// Confirmed absent before this run: restore = delete.
		rollbackJournal[relPath] = rollbackEntry{existed: false, external: external}
	case kindRegular:
		mode := os.FileMode(0o644)
		if info, err := os.Lstat(full); err == nil {
			mode = info.Mode().Perm()
		}
		rollbackJournal[relPath] = rollbackEntry{existed: true, content: content, mode: mode, external: external}
	default:
		// There, but not as bytes a restore could put back: unreadable,
		// or not a regular file. Never treated as absent — a restore would
		// delete it — and never rewritten: left as is, and reported.
		rollbackJournal[relPath] = rollbackEntry{existed: true, unverifiable: true, external: external}
	}
}

// CaptureExternalWrites journals the paths an external tool is about to
// write, so a failed run restores them like any forge write. Call it
// immediately before the tool runs, with project-relative paths: a file, or
// a directory the tool writes into (captured recursively; files created
// under it during the run are deleted on restore). A path that does not
// exist yet is both — whatever the tool creates there is removed.
//
// First-capture-wins, exactly as recordPreWrite: a path forge already wrote
// earlier this run keeps its true pre-run bytes. Paths outside the root are
// ignored. No-op when journaling is OFF.
func CaptureExternalWrites(root string, relPaths ...string) {
	if rollbackJournal == nil {
		return
	}
	for _, rel := range relPaths {
		rel = slashKey(filepath.Clean(rel))
		if rel == "." || rel == "" || isRooted(rel) || hasDotDotPrefix(rel) {
			continue
		}
		full := filepath.Join(root, rel)
		info, err := os.Lstat(full)
		switch {
		case err == nil && info.IsDir():
			if !throughRealDirs(root, rel) {
				// Reached through a symlinked parent: not ours to capture.
				continue
			}
			rollbackTrees[rel] = true
			rollbackDirtyTrees[rel] = true
			if _, seen := rollbackTreeIDs[rel]; !seen {
				rollbackTreeIDs[rel] = info
			}
			_ = filepath.WalkDir(full, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil || !d.Type().IsRegular() {
					return nil
				}
				if fileRel, relErr := filepath.Rel(root, path); relErr == nil {
					captureExternal(root, slashKey(fileRel))
				}
				return nil
			})
		case err == nil && !info.Mode().IsRegular():
			// A symlink or device: not ours to rewrite.
		default:
			captureExternal(root, rel)
			if err != nil {
				rollbackTrees[rel] = true
				rollbackDirtyTrees[rel] = true
			}
		}
	}
}

func captureExternal(root, relPath string) {
	// The tool about to run may rewrite it, whenever it was captured.
	rollbackDirty[relPath] = true
	if _, seen := rollbackJournal[relPath]; seen {
		return
	}
	captureEntry(root, relPath, true)
}

// notePostWrite records content as what forge left at relPath, right after
// the chokepoint wrote it: the precise form of SettleJournal for the writes
// that hold their bytes. No-op when journaling is OFF or the path is not
// journaled.
func notePostWrite(relPath string, content []byte) {
	if rollbackJournal == nil {
		return
	}
	relPath = slashKey(relPath)
	entry, ok := rollbackJournal[relPath]
	if !ok {
		return
	}
	entry.left = forgeLeft{known: true, sum: sha256.Sum256(content)}
	rollbackJournal[relPath] = entry
	delete(rollbackDirty, relPath)
}

// SettleJournal records, as what this run's writers left there, the current
// state of every journaled path written since the last call, and of every
// file under a directory an external tool was about to write into. The
// generate pipeline calls it at the end of each step, so a later edit by
// another writer is told apart from forge's output when a failed run is
// rewound. No-op when journaling is OFF.
func SettleJournal(root string) {
	if rollbackJournal == nil {
		return
	}
	for relPath := range rollbackDirty {
		entry, ok := rollbackJournal[relPath]
		if !ok {
			continue
		}
		entry.left = currentState(filepath.Join(root, relPath))
		rollbackJournal[relPath] = entry
	}
	for tree := range rollbackDirtyTrees {
		// The tool ran since the last settle point, so the directory there
		// now is the one it left: the one it created when the path was
		// absent at capture, or the twin it swapped in for the captured one
		// (a generator that writes to scratch and renames the result into
		// place, so a failure never leaves the tree half-written). Either
		// way it is the tree from here on; the pre-run files are already in
		// the journal. A directory replaced AFTER this point is another
		// writer's (treeIntact), and a symlink never becomes the tree.
		if info, err := os.Lstat(filepath.Join(root, tree)); err == nil && info.IsDir() && throughRealDirs(root, tree) {
			if id, seen := rollbackTreeIDs[tree]; !seen || !os.SameFile(id, info) {
				rollbackTreeIDs[tree] = info
			}
		}
		if !treeIntact(root, tree) {
			continue
		}
		_ = filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || !d.Type().IsRegular() {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			rel = slashKey(rel)
			if entry, journaled := rollbackJournal[rel]; journaled {
				entry.left = currentState(path)
				rollbackJournal[rel] = entry
				return nil
			}
			if data, err := os.ReadFile(path); err == nil {
				rollbackCreated[rel] = sha256.Sum256(data)
			}
			return nil
		})
	}
	clear(rollbackDirty)
	clear(rollbackDirtyTrees)
}

// currentState is full's state as a forgeLeft; unknown when it cannot be
// read or is not a regular file, so a restore spares it.
func currentState(full string) forgeLeft {
	switch kind, data := observe(full); kind {
	case kindAbsent:
		_, err := os.Lstat(filepath.Dir(full))
		return forgeLeft{known: true, absent: true, dirAbsent: os.IsNotExist(err)}
	case kindRegular:
		return forgeLeft{known: true, sum: sha256.Sum256(data)}
	}
	return forgeLeft{}
}

// changedSinceForge reports whether a path observed as (kind, data) is no
// longer in the state this run's writers left it in: another writer changed
// it after forge did. The caller has already refused a path that is not
// absent or a regular file.
func changedSinceForge(left forgeLeft, kind pathKind, data []byte) bool {
	if kind == kindAbsent {
		return !left.absent
	}
	return left.absent || sha256.Sum256(data) != left.sum
}

// JournaledPaths returns every path the journal holds (slash form, sorted)
// — the set a restore puts back. Callers verifying a restore read it BEFORE
// RestoreRollback, which clears the journal.
func JournaledPaths() []string {
	out := make([]string, 0, len(rollbackJournal))
	for p := range rollbackJournal {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// ChangedJournalPaths returns every path this run actually changed, of the
// ones a restore owns: each journaled path whose bytes or existence differ
// from what was captured before the run, and each file an external tool
// created under a captured directory. A journaled path rewritten with its
// pre-run bytes is not a change — every write goes through the journal, and
// most rewrite a file identically. Slash form, sorted. Read it BEFORE
// RestoreRollbackReport, which clears the journal.
func ChangedJournalPaths(root string) []string {
	var out []string
	for relPath, entry := range rollbackJournal {
		if entryChanged(entry, filepath.Join(root, relPath)) {
			out = append(out, relPath)
		}
	}
	out = append(out, createdUnderTrees(root)...)
	sort.Strings(out)
	return out
}

// entryChanged reports whether full is no longer in the state entry
// captured. A path that was absent and is now a directory is not itself a
// change: the files inside it are, and createdUnderTrees names them.
func entryChanged(entry rollbackEntry, full string) bool {
	if !entry.existed {
		info, err := os.Lstat(full)
		return err == nil && !info.IsDir()
	}
	current, err := os.ReadFile(full)
	return err != nil || !bytes.Equal(current, entry.content)
}

// RecordPreWrite is the exported shim for pipeline steps that mutate a
// forge-owned path DIRECTLY (a raw os.Remove / os.WriteFile) instead of
// through the WriteGeneratedFile* chokepoint. Call it immediately before
// the mutation so the rollback journal can restore the path on a failed
// run. No-op when journaling is OFF or the path was already captured.
func RecordPreWrite(root, relPath string) { recordPreWrite(root, relPath) }

// RecordPreWriteAbs is RecordPreWrite for call sites that only hold the
// joined destination path (the scaffold-once raw writers in
// internal/codegen address files as filepath.Join(projectDir, rel) and
// never thread the pair through). The path is resolved against the
// journal's armed root; a path outside that root is not captured — the
// journal restores relative to the root, so an outside path is not ours
// to rewind.
//
// This closes the defect where a failed generate run reverted the Tier-1
// files it wrote but left the scaffold-once files written THE SAME RUN
// (e.g. handlers_crud.go surviving while its handlers_crud_ops_gen.go
// dependency was rolled back), stranding the tree in a state that
// compiles against neither the pre-run nor the post-run world.
func RecordPreWriteAbs(path string) {
	if rollbackJournal == nil {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(rollbackRoot, abs)
	if err != nil || rel == "." || filepath.IsAbs(rel) || hasDotDotPrefix(rel) {
		return
	}
	recordPreWrite(rollbackRoot, rel)
}

// RemoveJournaled deletes a forge-owned path after capturing its pre-run
// bytes, so a `forge generate` that aborts later restores it. It is the
// DELETE-side twin of the WriteGeneratedFile chokepoint, and every forge
// deletion of a tracked file should go through it.
//
// WHY A DELETE NEEDS JOURNALING AT ALL. The journal is populated lazily, at
// the first write that targets a path. A raw os.Remove is invisible to it, and
// the consequence is worse than "not restored" — it INVERTS the rewind:
//
//  1. a step deletes internal/<pkg>/middleware_gen.go   (unjournaled)
//  2. a later step rewrites it, and recordPreWrite — running for the first
//     time, seeing no file — records existed=false, "absent before this run"
//  3. the run fails, and RestoreRollback honours existed=false by DELETING
//
// So the rewind whose entire purpose is to hand the tree back clean is what
// removes the file. Observed in a shared checkout: a proto validation error
// left internal/coupon with no middleware_gen.go, and `go build ./...` failed
// for every agent with `undefined: couponsvc.NewServiceWithForgeMiddleware` —
// an error naming a package that had nothing to do with the failure.
//
// Capture-then-delete fixes both halves at once, because recordPreWrite is
// first-write-wins: the capture here holds the TRUE pre-run bytes, and a
// later rewrite's capture is a no-op. A missing file is not an error, and
// journaling never itself fails a delete.
func RemoveJournaled(path string) error {
	RecordPreWriteAbs(path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SnapshotJournalTargets copies the CURRENT on-disk content of every
// journaled path — i.e. the failed run's output — into preserveDir,
// mirroring each file's project-relative path. Called BEFORE
// RestoreRollback on the failure path so the revert (which is the right
// default: the user's tree must come back clean) does not also destroy
// the only evidence of WHY the run failed: a `go build` validate error
// cites file:line coordinates in generated sources, and pre-fix the
// revert deleted those sources, leaving the user to debug a compiler
// error against code they could not read.
//
// The preserve directory is recreated fresh on every call so successive
// failures never interleave. Journaled paths whose current bytes equal
// their captured pre-run bytes are skipped (nothing new to inspect), as
// are paths that no longer exist. Best-effort per file — a copy failure
// drops that file from the returned list, never aborts. Returns the
// sorted relative paths preserved; nil when journaling is OFF, the
// journal is empty, or nothing qualified.
func SnapshotJournalTargets(root, preserveDir string) []string {
	if len(rollbackJournal) == 0 {
		return nil
	}
	if err := os.RemoveAll(preserveDir); err != nil {
		return nil
	}
	preserved := make([]string, 0, len(rollbackJournal))
	for relPath, entry := range rollbackJournal {
		current, err := os.ReadFile(filepath.Join(root, relPath))
		if err != nil {
			continue // gone or unreadable — nothing to preserve
		}
		if entry.existed && bytes.Equal(current, entry.content) {
			continue // byte-identical to pre-run — the revert won't touch it
		}
		dest := filepath.Join(preserveDir, relPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			continue
		}
		if err := atomicfile.WriteFile(dest, current, 0o644); err != nil {
			continue
		}
		preserved = append(preserved, relPath)
	}
	if len(preserved) == 0 {
		return nil
	}
	sort.Strings(preserved)
	return preserved
}

// WriteSummary is the run's write ledger: what forge's writers actually
// did to the tree, derived from the same journal the rollback uses.
//
// The three fields answer three DIFFERENT questions, and conflating them
// is what made `✅ Code generation complete!` unfalsifiable:
//
//	Touched == 0            no emitter fired at all. The pipeline ran and
//	                        produced nothing. This is the silent-no-op
//	                        signature (`--steps mocks` omitting its own
//	                        emitter printed success from exactly here).
//	Touched > 0, changed 0  every emitter fired and the bytes already
//	                        matched — a healthy idempotent re-run.
//	changed > 0             real work landed; the names are the proof.
type WriteSummary struct {
	// Touched is every path a forge writer targeted this run, whether or
	// not the bytes changed.
	Touched int
	// Created are paths that did not exist before the run.
	Created []string
	// Updated are paths that existed and whose bytes differ now.
	Updated []string
	// Deleted are paths that existed and are gone now — a retired
	// generated file (RemoveJournaled) is a change like any write.
	Deleted []string
}

// Changed returns the number of paths whose on-disk bytes differ from
// their pre-run state: created, updated or deleted.
func (s WriteSummary) Changed() int { return len(s.Created) + len(s.Updated) + len(s.Deleted) }

// SummarizeWrites compares every journaled path's CURRENT bytes against
// the pre-run bytes the journal captured, and reports what changed. It
// mutates nothing — call it any time before CommitRollback (which drops
// the journal) to learn what the run actually did.
//
// Returns a zero WriteSummary when journaling is OFF, so callers that run
// outside the pipeline degrade to "no claim" rather than to a false one.
func SummarizeWrites(root string) WriteSummary {
	if rollbackJournal == nil {
		return WriteSummary{}
	}
	var sum WriteSummary
	for relPath, entry := range rollbackJournal {
		if entry.external && externalSettled(root, relPath, entry) {
			// Captured ahead of a tool that left it alone: not a write.
			continue
		}
		sum.Touched++
		current, err := os.ReadFile(filepath.Join(root, relPath))
		if err != nil {
			// Gone now. A path that existed before the run was deleted —
			// a retired generated file — and that is a change; one that
			// did not exist was created and removed again, which is none.
			// Unreadable-but-present cannot be called anything, so it
			// stays counted in Touched only.
			if os.IsNotExist(err) && entry.existed {
				sum.Deleted = append(sum.Deleted, relPath)
			}
			continue
		}
		if !entry.existed {
			sum.Created = append(sum.Created, relPath)
			continue
		}
		if !bytes.Equal(current, entry.content) {
			sum.Updated = append(sum.Updated, relPath)
		}
	}
	// Files an external tool created under a captured directory are new
	// this run too, though no journal entry names them.
	for _, rel := range createdUnderTrees(root) {
		sum.Touched++
		sum.Created = append(sum.Created, rel)
	}
	sort.Strings(sum.Created)
	sort.Strings(sum.Updated)
	sort.Strings(sum.Deleted)
	return sum
}

// createdUnderTrees lists the regular files under a captured external-tool
// directory that are not in the journal — i.e. that did not exist when the
// directory was captured.
//
// A tree that is no longer the directory captured (replacedTrees) is not
// walked, nor is a nested captured tree inside a walked one that was
// replaced: what is beneath it is another writer's.
func createdUnderTrees(root string) []string {
	var out []string
	seen := map[string]bool{} // nested captured trees walk a file twice
	replaced := map[string]bool{}
	for _, tree := range replacedTrees(root) {
		replaced[tree] = true
	}
	for tree := range rollbackTrees {
		if replaced[tree] {
			continue
		}
		_ = filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			rel = slashKey(rel)
			if d.IsDir() && replaced[rel] {
				return filepath.SkipDir
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if _, journaled := rollbackJournal[rel]; !journaled && !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// ScaffoldsCreatedThisRun returns the scaffold-once files this run brought
// into existence: a birth recorded this run for a file that was absent
// before it. Sorted; read it BEFORE RestoreRollbackReport, which clears both
// the journal and the birth set.
//
// A birth recorded for a file that was already there (an older project's
// scaffold, adopted by ScaffoldOnceDecision) is not one: a rollback leaves
// that file, restores the ledger without the adoption, and the next run
// adopts it again.
func ScaffoldsCreatedThisRun() []string {
	var created []string
	for _, rel := range ScaffoldBirthsThisRun() {
		if entry, ok := rollbackJournal[slashKey(rel)]; ok && !entry.existed {
			created = append(created, rel)
		}
	}
	return created
}

// RestoreRollback rewinds every journaled path to its captured pre-run
// state and returns the sorted list of paths it restored. A path that
// existed before the run is rewritten with its original bytes + mode; a
// path that did NOT exist is removed (deleting forge's freshly-written
// output and pruning any now-empty parent directories forge created).
// Best-effort per path: an individual restore error does not abort the
// rest (a partially-restored tree still beats a fully mid-regen one), but
// the path is omitted from the returned list so the caller can report
// exactly what was recovered. Clears the journal and turns journaling
// OFF — a restored run is over.
func RestoreRollback(root string) []string {
	restored, _ := RestoreRollbackReport(root)
	return restored
}

// RestoreRollbackReport is RestoreRollback that also returns the paths it
// could NOT restore, so a caller can refuse to claim a restore that did not
// happen. A path spared because another writer changed it after forge did
// (RestoreResult.Spared) is one of them.
func RestoreRollbackReport(root string) (restored, failed []string) {
	res := RestoreRollbackDetailed(root)
	failed = append(append(res.Failed, res.Spared...), res.Unverified...)
	sort.Strings(failed)
	return res.Restored, failed
}

// RestoreResult is what a rewind did, path by path (slash form, sorted).
type RestoreResult struct {
	// Restored are put back to their pre-run state.
	Restored []string
	// Failed could not be put back (a write or delete error).
	Failed []string
	// Spared were left alone because another writer changed them after
	// this run's writers did — new bytes, or a different kind of path (a
	// symlink, a directory): rewinding them would destroy that writer's
	// work (forge#2267). They are neither the pre-run nor forge's state.
	Spared []string
	// Unverified were left alone because their state could not be
	// verified — unreadable now or before the run, or with no readable
	// record of what forge left. A restore fails closed: could not verify,
	// left as is.
	Unverified []string
}

// RestoreRollbackDetailed rewinds the journal like RestoreRollbackReport and
// says which paths it spared.
//
// Paths captured ahead of an external tool (CaptureExternalWrites) are
// rewritten and listed only when the tool actually changed them, and every
// file created under a captured directory during the run is deleted — each
// only while it is still what this run left there (see the file header).
// What was written since the last settle point is settled first: forge
// wrote it, nothing has run since.
func RestoreRollbackDetailed(root string) RestoreResult {
	if rollbackJournal == nil {
		return RestoreResult{}
	}
	SettleJournal(root)
	var res RestoreResult
	restored, failed := make([]string, 0, len(rollbackJournal)), []string(nil)
	var prune []string
	replaced := replacedTrees(root)
	for relPath, entry := range rollbackJournal {
		full := filepath.Join(root, relPath)
		if underAny(relPath, replaced) {
			// Beneath a captured directory another writer replaced: the
			// path is theirs now, whatever it holds.
			res.Spared = append(res.Spared, relPath)
			continue
		}
		if entry.external && externalSettled(root, relPath, entry) {
			continue
		}
		// Fail closed. A path whose state cannot be verified — unreadable
		// now, not a regular file now (a symlink is never followed, written
		// through or removed), unverifiable before the run, or with no
		// readable record of what forge left — is left as is and reported.
		// So is one reached through a parent another writer made a symlink.
		kind, data := observe(full)
		parentsIntact, parentMissing := parentChain(root, relPath)
		switch {
		case !parentsIntact:
			res.Spared = append(res.Spared, relPath)
			continue
		case kind == kindUnreadable || entry.unverifiable:
			res.Unverified = append(res.Unverified, relPath)
			continue
		case kind == kindOther:
			res.Spared = append(res.Spared, relPath)
			continue
		case !entry.left.known:
			res.Unverified = append(res.Unverified, relPath)
			continue
		case changedSinceForge(entry.left, kind, data) && !atPreRunState(entry, kind, data):
			res.Spared = append(res.Spared, relPath)
			continue
		}
		if entry.existed {
			switch rewritePreRun(root, relPath, entry, parentMissing) {
			case rewriteDone:
				restored = append(restored, relPath)
			case rewriteSpared:
				res.Spared = append(res.Spared, relPath)
			default:
				failed = append(failed, relPath)
			}
			continue
		}
		// Did not exist pre-run: delete forge's new output.
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			failed = append(failed, relPath)
			continue
		}
		prune = append(prune, filepath.Dir(full))
		restored = append(restored, relPath)
	}
	// Pruned only after every rewrite, so a directory emptied here is never
	// mistaken, by a later path's parent check, for one another writer
	// removed.
	for _, dir := range prune {
		pruneEmptyParents(root, dir)
	}
	// A scaffold-once file created THIS run was un-created above, so its
	// birth record must go with it: a surviving record would make the failed
	// run consume the path's one and only birth. The birth ledger is
	// journaled like every other write (saveScaffoldLedgerLocked), so the
	// loop already put .forge/scaffolded/ back to its pre-run bytes —
	// births and absent-set alike. What remains is the in-memory copy, which
	// still holds this run's records: drop it, so the next query reads the
	// restored file.
	ResetScaffoldLedgerCache()
	// Files an external tool CREATED under a captured directory: every
	// file that existed there at capture time is journaled, so anything
	// else is new this run.
	// Only a file still as the step left it is deleted: one another
	// writer created there, or changed since, is theirs. A tree that is
	// no longer the directory captured is not walked at all: it is
	// spared and reported, with everything beneath it.
	res.Spared = append(res.Spared, replaced...)
	for _, rel := range createdUnderTrees(root) {
		full := filepath.Join(root, rel)
		switch kind, data := observe(full); {
		case kind == kindUnreadable:
			res.Unverified = append(res.Unverified, rel)
			continue
		case kind == kindAbsent:
			continue
		case kind != kindRegular || !createdByThisRun(rel, data):
			res.Spared = append(res.Spared, rel)
			continue
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			failed = append(failed, rel)
			continue
		}
		pruneEmptyParents(root, filepath.Dir(full))
		restored = append(restored, rel)
	}
	created := ScaffoldsCreatedThisRun()
	clearRollbackState()
	// Journaling is off now, so the reconcile's own ledger writes are not
	// captured into a journal that no longer exists.
	resetScaffoldBirths()
	reconcileScaffoldBirths(root, created)
	sort.Strings(restored)
	sort.Strings(failed)
	res.Spared = dedupeSorted(res.Spared)
	sort.Strings(res.Unverified)
	res.Restored, res.Failed = restored, failed
	return res
}

// rewriteOutcome is what rewritePreRun did with a path.
type rewriteOutcome int

const (
	rewriteDone rewriteOutcome = iota
	rewriteSpared
	rewriteFailed
)

// rewritePreRun puts a path that existed before the run back to its pre-run
// bytes. A missing parent is recreated only when forge's own step removed it
// (entry.left.dirAbsent): one removed after that is another writer's, and
// recreating it would resurrect what they deleted (forge#2329 review). The
// parents are checked again after they are made, so a parent that became a
// symlink or a file since the caller's check is never written through.
func rewritePreRun(root, relPath string, entry rollbackEntry, parentMissing bool) rewriteOutcome {
	if parentMissing && !entry.left.dirAbsent {
		return rewriteSpared
	}
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return rewriteFailed
	}
	if !throughRealDirs(root, relPath) {
		return rewriteSpared
	}
	mode := entry.mode
	if mode == 0 {
		mode = 0o644
	}
	if err := atomicfile.WriteFile(full, entry.content, mode); err != nil {
		return rewriteFailed
	}
	return rewriteDone
}

// atPreRunState reports whether a path observed as (kind, data) is already
// in the state a restore of entry would put it in — there is nothing to
// spare then, whoever wrote it.
func atPreRunState(entry rollbackEntry, kind pathKind, data []byte) bool {
	if !entry.existed {
		return kind == kindAbsent
	}
	return kind == kindRegular && bytes.Equal(data, entry.content)
}

// createdByThisRun reports whether a regular file under a captured
// directory, absent from the journal, holds the bytes this run's tool left
// there.
func createdByThisRun(rel string, data []byte) bool {
	sum, ok := rollbackCreated[rel]
	return ok && sha256.Sum256(data) == sum
}

// externalSettled reports whether a path captured ahead of an external tool
// needs nothing restored on its own account: it is in its pre-run state, or
// it was absent and the tool made it a DIRECTORY — whose files the
// created-under-trees sweep removes.
//
// The path is observed without following a symlink, so a symlink, an
// unreadable path, or a directory that is not the tree this run captured is
// never settled: the fail-closed checks of the rewind see it.
func externalSettled(root, relPath string, entry rollbackEntry) bool {
	full := filepath.Join(root, relPath)
	if !throughRealDirs(root, relPath) {
		return false
	}
	kind, data := observe(full)
	if !entry.existed {
		if kind == kindAbsent {
			return true
		}
		info, err := os.Lstat(full)
		return err == nil && info.IsDir() && rollbackTrees[relPath] && treeIntact(root, relPath)
	}
	return kind == kindRegular && bytes.Equal(data, entry.content)
}

// throughRealDirs reports whether every parent of rel under root that exists
// is a real directory: a path reached through a symlink is somewhere else
// than the one captured, and a parent that is a file, or that Lstat cannot
// read, cannot be shown to be the one captured either. A missing parent is
// allowed here; parentMissing tells a restore about it.
func throughRealDirs(root, rel string) bool {
	intact, _ := parentChain(root, rel)
	return intact
}

// parentChain walks the parents of rel under root without following a
// symlink: intact when every one that exists is a real directory, missing
// when one of them does not exist.
func parentChain(root, rel string) (intact, missing bool) {
	dir := filepath.Dir(filepath.FromSlash(rel))
	for dir != "." && dir != string(filepath.Separator) && dir != "" {
		info, err := os.Lstat(filepath.Join(root, dir))
		switch {
		case os.IsNotExist(err):
			missing = true
		case err != nil || !info.IsDir():
			return false, missing
		}
		dir = filepath.Dir(dir)
	}
	return true, missing
}

// treeIntact reports whether a captured tree may be walked: absent, or still
// the very directory this run captured (or the tool created), reached
// without a symlink.
func treeIntact(root, tree string) bool {
	if !throughRealDirs(root, tree) {
		return false
	}
	info, err := os.Lstat(filepath.Join(root, tree))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	id, recorded := rollbackTreeIDs[tree]
	if !recorded {
		// Absent at capture and not settled since: a directory the tool
		// is creating, or a regular file it wrote in its place (journaled
		// itself). A rewind settles first, so it always has a record of a
		// directory that existed at its last settle point.
		return info.IsDir() || info.Mode().IsRegular()
	}
	return info.IsDir() && os.SameFile(id, info)
}

// replacedTrees lists, sorted, the captured trees that are no longer the
// directory this run captured: replaced by another directory or a symlink,
// or reached through a symlinked parent.
func replacedTrees(root string) []string {
	var out []string
	for tree := range rollbackTrees {
		if !treeIntact(root, tree) {
			out = append(out, tree)
		}
	}
	sort.Strings(out)
	return out
}

// underAny reports whether rel is strictly beneath one of trees.
func underAny(rel string, trees []string) bool {
	for _, tree := range trees {
		if strings.HasPrefix(rel, tree+"/") {
			return true
		}
	}
	return false
}

// dedupeSorted sorts s and drops repeats.
func dedupeSorted(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

// pruneEmptyParents removes now-empty directories from dir up toward
// (but never including) root. A forge write may have created nested dirs
// (handlers/<svc>/, internal/db/) that should not linger after a
// rollback deletes the only file inside them. Stops at the first
// non-empty (or unremovable) directory, and never ascends past root.
func pruneEmptyParents(root, dir string) {
	rootClean := filepath.Clean(root)
	for {
		dir = filepath.Clean(dir)
		if dir == rootClean || !isUnder(dir, rootClean) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// isUnder reports whether dir is strictly within root (a proper
// descendant), guarding the prune walk against ascending past the
// project root via a stray relative component.
func isUnder(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
		!hasDotDotPrefix(rel)
}

// hasDotDotPrefix reports whether rel escapes its base via a leading
// "..". filepath.Rel can return paths like "../sibling"; those must not
// be pruned.
func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}

// isRooted reports whether a slash-keyed path names somewhere other than a
// path under its base. filepath.IsAbs alone is not enough: on Windows "/abs"
// has no volume, so IsAbs is false, yet it names the current drive's root,
// and filepath.Join(root, "/abs") would silently turn it into root\abs.
func isRooted(rel string) bool {
	return filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || filepath.VolumeName(rel) != ""
}
