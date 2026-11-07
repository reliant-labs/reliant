// Marker-driven stale-artifact cleanup.
//
// Forge's pre-2026-06-05 cleanup walked generated-artifact directories
// and removed anything whose name didn't match a re-derived "expected"
// set computed from forge.yaml — and deleted user code when the
// re-derivation disagreed with on-disk snake_case proto layouts. The
// manifest era fixed that by deleting only manifest-recorded paths; the
// self-certifying era keeps the same safety property without the
// manifest: only files that carry a forge:hash certification marker are
// candidates. The marker IS forge's authorship record, embedded in the
// file itself — paths without one are user content and forge doesn't
// get to delete them.
//
// A candidate is a marker-bearing file the current run did NOT re-emit
// (per the WrittenThisRun set). Guardrails before deletion:
//
//   - Owner-step gate. The tier1OwnerRegistry in generate_tier1_scope.go
//     maps generated paths back to the step that emits them. A path
//     whose owning step is gated off this run is left in place; that
//     step wasn't going to write it regardless, so missing-from-
//     WrittenThisRun is uninformative.
//   - Upgrade-managed paths. `forge project upgrade` is their emitter; `forge
//     generate` deliberately leaves them alone.
//   - Scaffold-once paths. A path in the scaffold ledger is the user's,
//     marker or not.
//   - Verification. A PRISTINE marker (certified machine output) is
//     deleted: those bytes are exactly what forge rendered, so nothing
//     of the user's goes with them. A file whose marker fails
//     verification was hand-edited — the bytes are the user's now, so
//     it is reported and kept unless --force-cleanup asks for it too.
//   - Disowned paths are never candidates.
//
// Deletion is the default because a report nobody acts on leaves a
// stale file in the build: a factories test for an entity forge no
// longer generates keeps compiling against helpers that are gone, and
// the only way out used to be a flag --help did not show. Every delete
// is journaled, so a run that fails later rewinds it; and the sweep
// runs only in a full pipeline (no --steps preset or --templates-only
// run includes it) after every emitter, so "not written this run"
// means no emitter writes the path any more. The caller (stepCleanupStale)
// skips the sweep when a step failed as a warning: that step's outputs
// are unwritten for a reason other than staleness.
//
// Scoped-fallback entries (.forge/hashes.json, comment-incapable
// formats) get the same treatment keyed off the recorded hash.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

// upgradeManagedPaths is a single-load cache of the upgrade-managed
// path set. The set is constant for the binary's lifetime (it's
// derived from compiled-in upgrade.go file lists), so loading it once
// avoids re-walking the kind/binary matrix every cleanup pass. Tests
// that need to swap it can do so by reassigning before invoking
// cleanupStaleArtifacts.
var upgradeManagedPaths = generator.UpgradeManagedPaths()

// staleSweep is what one stale-artifact sweep found and did. Paths are
// project-relative and sorted.
type staleSweep struct {
	// Removed are the stale files deleted this run: every pristine one,
	// and the hand-edited ones too under --force-cleanup.
	Removed []string
	// HandEdited are the stale files whose marker no longer verifies.
	// They are kept unless --force-cleanup was passed, in which case
	// each one deleted is also in Removed.
	HandEdited []string
}

// cleanupStaleArtifacts deletes the forge-certified files the current
// pipeline run did not re-emit, subject to the guardrails in the file
// comment: pristine ones always, hand-edited ones only under
// ctx.ForceCleanup. A removal that fails (permission denied and the
// like) is warned about and left out of Removed rather than aborting
// the sweep.
func cleanupStaleArtifacts(ctx *pipelineContext) (staleSweep, error) {
	var sweep staleSweep
	if ctx == nil {
		return sweep, nil
	}
	cs := ctx.Checksums

	markers := checksums.ScanMarkers(ctx.AbsPath)
	rels := make([]string, 0, len(markers))
	for rel := range markers {
		rels = append(rels, rel)
	}
	if cs != nil {
		for rel := range cs.Unstampable {
			if _, dup := markers[rel]; !dup {
				rels = append(rels, rel)
			}
		}
	}
	sort.Strings(rels)

	for _, rel := range rels {
		// This run wrote this path — definitely not stale. Nor is one an
		// emitter targeted but did not write: the stomp guard parks a
		// drifted file's render aside until the fresh-render check settles
		// it (generate_drift_rescue.go), and that file is current output,
		// not an artifact no emitter produces any more.
		if checksums.WasWrittenThisRun(rel) || checksums.IsTier1Target(rel) {
			continue
		}
		// Disowned: user-owned by recorded intent (markers are stripped
		// at disown time; this is a belt-and-braces check).
		if cs.IsDisowned(rel) {
			continue
		}
		// Upgrade-managed paths: certified, but `forge generate` is not
		// the emitter — `forge project upgrade` writes these on version bumps.
		if upgradeManagedPaths[rel] {
			continue
		}
		// Per-frontend managed files (frontends/<name>/eslint.config.mjs)
		// are the same lane, but their paths depend on the frontends a
		// project declares, so they cannot be in the static union above.
		// Without this the sweep reads them as "certified, not written this
		// run" and DELETES the pristine ones — the exact data loss
		// UpgradeManagedPaths exists to prevent.
		if generator.IsFrontendManagedPath(rel) {
			continue
		}
		// Scaffold-once paths are the user's. A file that was Tier-1 in an
		// older forge and is scaffold-once now still carries the old marker,
		// pristine, and its emitter never rewrites it — but that emitter
		// asked the scaffold ledger about it (ScaffoldOnceDecision records
		// a present file), so the ledger says it is live. Without this a
		// missing UpgradeManagedPaths entry deletes, say, a project's
		// command root on the first run after the transition.
		if checksums.ScaffoldRecorded(ctx.ProjectDir, rel) {
			continue
		}
		// Owner-step gate. If the owning emitter step is gated off this
		// run, we can't conclude the path is stale.
		if gate := tier1OwnerGate(rel); gate != nil && !gate(ctx) {
			continue
		}

		full := filepath.Join(ctx.ProjectDir, rel)

		// Pristineness decides delete-vs-report. For marker files the
		// scan already classified them; fallback entries compare the
		// recorded hash.
		pristine := false
		if info, ok := markers[rel]; ok {
			pristine = info.Status == checksums.Pristine
		} else if cs != nil {
			content, rerr := os.ReadFile(full)
			if rerr != nil {
				continue // gone or unreadable — nothing to clean
			}
			pristine = checksums.BodyHash(content) == cs.Unstampable[rel]
		}
		if !pristine {
			// Stale AND hand-edited: the bytes are the user's now, so
			// they go only when --force-cleanup asks for them.
			sweep.HandEdited = append(sweep.HandEdited, rel)
			if !ctx.ForceCleanup {
				continue
			}
		}

		// Journaled: the sweep must rewind with an aborted run.
		if removeErr := checksums.RemoveJournaled(full); removeErr != nil && !os.IsNotExist(removeErr) {
			// Permission-denied and friends: surface a warning rather
			// than aborting — a single locked file shouldn't tank the
			// sweep.
			fmt.Fprintf(os.Stderr, "⚠️  cleanup partial: could not remove %s (%v)\n", full, removeErr)
			continue
		}
		sweep.Removed = append(sweep.Removed, rel)
		if cs != nil {
			delete(cs.Unstampable, rel)
		}

		// Best-effort: prune the now-empty parent directory. A single
		// os.Remove on the dir succeeds only if it's empty; "directory
		// not empty" is the dominant, expected case.
		if rmDirErr := os.Remove(filepath.Dir(full)); rmDirErr != nil && !os.IsNotExist(rmDirErr) {
			if ctx.Verbose {
				fmt.Fprintf(os.Stderr, "  ℹ️  cleanup: empty-dir prune failed for %s (%v) — usually expected (sibling files present)\n", filepath.Dir(full), rmDirErr)
			}
		}
	}

	// rels is sorted, so both slices already are.
	return sweep, nil
}
