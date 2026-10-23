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
//
// The journal is process-global (like the rest of this package's per-run
// state) and reset by BeginRollbackJournal at the head of each pipeline
// run. Non-pipeline callers (forge project upgrade, project creation) never call
// Begin, so journaling stays OFF and their writes are recorded nowhere —
// they have their own recovery stories.
package checksums

import (
	"bytes"
	"os"
	"path/filepath"
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

// BeginRollbackJournal turns journaling ON and clears any prior capture.
// Called once at the head of a `forge generate` run, before any writer
// fires, with the project root the run operates on. After this, every
// forge write captures its target's pre-run state (once per path) so
// RestoreRollback can undo the whole run.
func BeginRollbackJournal(root string) {
	resetScaffoldBirths()
	rollbackJournal = map[string]rollbackEntry{}
	rollbackTrees = map[string]bool{}
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
	rollbackJournal = nil
	rollbackTrees = nil
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
	info, statErr := os.Stat(full)
	if statErr != nil {
		// Absent (or unreadable) before this run: restore = delete.
		rollbackJournal[relPath] = rollbackEntry{existed: false, external: external}
		return
	}
	content, readErr := os.ReadFile(full)
	if readErr != nil {
		// Exists but unreadable — best effort: treat as absent so a
		// failed run at least removes the half-written forge output rather
		// than leaving a corrupt file claiming to be pristine.
		rollbackJournal[relPath] = rollbackEntry{existed: false, external: external}
		return
	}
	rollbackJournal[relPath] = rollbackEntry{existed: true, content: content, mode: info.Mode().Perm(), external: external}
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
			rollbackTrees[rel] = true
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
			}
		}
	}
}

func captureExternal(root, relPath string) {
	if _, seen := rollbackJournal[relPath]; seen {
		return
	}
	captureEntry(root, relPath, true)
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
		if entry.external && externalSettled(entry, filepath.Join(root, relPath)) {
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
func createdUnderTrees(root string) []string {
	var out []string
	seen := map[string]bool{} // nested captured trees walk a file twice
	for tree := range rollbackTrees {
		_ = filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || !d.Type().IsRegular() {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			rel = slashKey(rel)
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
// happen.
//
// Paths captured ahead of an external tool (CaptureExternalWrites) are
// rewritten and listed only when the tool actually changed them, and every
// file created under a captured directory during the run is deleted.
func RestoreRollbackReport(root string) (restored, failed []string) {
	if rollbackJournal == nil {
		return nil, nil
	}
	restored = make([]string, 0, len(rollbackJournal))
	for relPath, entry := range rollbackJournal {
		full := filepath.Join(root, relPath)
		if entry.external && externalSettled(entry, full) {
			continue
		}
		if entry.existed {
			mode := entry.mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				failed = append(failed, relPath)
				continue
			}
			if err := atomicfile.WriteFile(full, entry.content, mode); err != nil {
				failed = append(failed, relPath)
				continue
			}
			restored = append(restored, relPath)
			continue
		}
		// Did not exist pre-run: delete forge's new output.
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			failed = append(failed, relPath)
			continue
		}
		pruneEmptyParents(root, filepath.Dir(full))
		restored = append(restored, relPath)
	}
	// A scaffold-once file created THIS run was un-created above, so its
	// birth record must go with it: a surviving record would make the failed
	// run consume the path's one and only birth. The birth ledger is
	// journaled like every other write (saveScaffoldLedgerLocked), so the
	// loop already put .forge/scaffolded.json back to its pre-run bytes —
	// births and absent-set alike. What remains is the in-memory copy, which
	// still holds this run's records: drop it, so the next query reads the
	// restored file.
	ResetScaffoldLedgerCache()
	// Files an external tool CREATED under a captured directory: every
	// file that existed there at capture time is journaled, so anything
	// else is new this run.
	for _, rel := range createdUnderTrees(root) {
		full := filepath.Join(root, rel)
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			failed = append(failed, rel)
			continue
		}
		pruneEmptyParents(root, filepath.Dir(full))
		restored = append(restored, rel)
	}
	created := ScaffoldsCreatedThisRun()
	rollbackJournal = nil
	rollbackTrees = nil
	rollbackRoot = ""
	// Journaling is off now, so the reconcile's own ledger writes are not
	// captured into a journal that no longer exists.
	resetScaffoldBirths()
	reconcileScaffoldBirths(root, created)
	sort.Strings(restored)
	sort.Strings(failed)
	return restored, failed
}

// externalSettled reports whether a path captured ahead of an external tool
// needs nothing restored on its own account: it is in its pre-run state, or
// it was absent and the tool made it a DIRECTORY — whose files the
// created-under-trees sweep removes.
func externalSettled(entry rollbackEntry, full string) bool {
	if !entry.existed {
		info, err := os.Lstat(full)
		return os.IsNotExist(err) || (err == nil && info.IsDir())
	}
	current, err := os.ReadFile(full)
	return err == nil && bytes.Equal(current, entry.content)
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
