// Fresh-render rescue for the Tier-1 stomp guard.
//
// The guard reports a Tier-1 file as hand-edited when its bytes disagree
// with forge's record of them: the embedded forge:hash marker, or for a
// comment-incapable output (JSON) its .forge/hashes.json entry. That record
// can be stale while the bytes are exactly forge's own render:
//
//   - `forge generate` killed during "Validating generated code" had already
//     rewritten three observability dashboards, but saves hashes.json only
//     at the end of a run. The next generate refused all three as
//     hand-edited Tier-1 files.
//   - A merge that takes one side's generated file and the other side's
//     ledger, or merges a stamped file's body under one side's marker.
//
// Forge must never let its own outputs and its record of them disagree in a
// way that blocks the next run. Writing the ledger earlier only narrows the
// window, and does nothing for a merge. Comparing against the truth does
// both: the guard quarantines each in-scope drifted path (its writes go to a
// side render, the on-disk file is untouched), every emitter runs, and
// stepAdjudicateTier1Drift — after the emitters, BEFORE validation, so a
// run that will fail does not pay for a build first — re-records every file
// whose bytes are this run's render (checksums.RescueFreshRender). Only what
// is left is a hand-edit, reported exactly as the guard used to, and the run
// rolls back.
//
// The cost is rendering, which is cheap beside validation: a run with a
// genuine hand-edit now renders before it reports, where it used to stop at
// the guard.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// adjudicateDriftStep names the pipeline step that settles quarantined
// drift. The "Tier-1 file-stomp guard" phrase leads it because the step
// name wraps the error a hand-edit produces (`step "…": …`).
const adjudicateDriftStep = "Tier-1 file-stomp guard: compare drift with fresh renders"

// quarantineDriftForRescue records drift for stepAdjudicateTier1Drift and
// redirects each drifted path's writes to a side render, so the emitters
// produce what this run would write without touching the file.
func quarantineDriftForRescue(ctx *pipelineContext, drift []checksums.Tier1DriftEntry) {
	ctx.DriftPending = drift
	ctx.DriftMergeState = detectGitMergeState(ctx.AbsPath)
	for _, d := range drift {
		checksums.AddSideRenderOnly(d.Path)
	}
	fmt.Fprintf(os.Stderr, "🔎 %d Tier-1 file(s) differ from forge's record of them — each is compared with this run's render before it counts as a hand-edit:\n", len(drift))
	for _, d := range drift {
		fmt.Fprintf(os.Stderr, "   ? %s\n", d.Path)
	}
}

// stepAdjudicateTier1Drift settles the drift the stomp guard quarantined.
func stepAdjudicateTier1Drift(ctx *pipelineContext) error {
	pending := ctx.DriftPending
	if len(pending) == 0 {
		return nil
	}
	ctx.DriftPending = nil

	var rescued []string
	var unresolved []checksums.Tier1DriftEntry
	for _, d := range pending {
		if checksums.PreservedOwnershipTransfer(ctx.AbsPath, d.Path, d.OnDiskHash) {
			continue // Exact quarantined bytes survived a verified ownership migration.
		}
		if checksums.RescueFreshRender(ctx.AbsPath, d.Path, ctx.Checksums) {
			rescued = append(rescued, d.Path)
			continue
		}
		unresolved = append(unresolved, d)
	}
	if len(rescued) > 0 {
		fmt.Printf("♻️  %d of them are this run's own render under a stale record (an interrupted generate, or a merge) — not hand-edits; re-recorded:\n", len(rescued))
		for _, p := range rescued {
			fmt.Printf("   ✓ %s\n", p)
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	if ctx.DriftMergeState != "" {
		return midMergeTier1DriftError(ctx.DriftMergeState, unresolved)
	}
	// The FIRST line must stand alone — file names + remedies — because
	// truncating consumers (agent harnesses, wrap-and-rethrow callers)
	// often surface only the first line of an error (journey fr-a04f8c0609
	// saw a bare header with no file and no remedy). The body lives in
	// generate_drift_hints.go — it leads with each file's designated
	// extension point and documents `forge project disown` as a permanent,
	// one-way ownership transfer, so giving up regeneration never looks
	// like the path of least resistance for agents. The "Tier-1 file-stomp
	// guard" phrase reaches the user via the step-name wrap; repeating it
	// here produced the confusing 'guard: guard:' first line.
	return fmt.Errorf("%s\n%s", tier1DriftSummaryLine(unresolved), formatTier1DriftReport(unresolved))
}

// midMergeTier1DriftError is the drift report while git is in the middle of
// a merge / cherry-pick / rebase, where drift that no fresh render explains
// is still most likely what the operation brought in.
func midMergeTier1DriftError(state string, drift []checksums.Tier1DriftEntry) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Tier-1 stomp guard tripped while git is mid-%s.\n\n", state)
	fmt.Fprintf(&b, "%d Tier-1 file(s) drifted from their recorded checksum and do not match this run's render:\n", len(drift))
	for _, d := range drift {
		fmt.Fprintf(&b, "  • %s\n", d.Path)
	}
	fmt.Fprintf(&b, "\nDuring a mid-%s state this is almost always upstream changes the merge brought in (not real hand-edits). Two options:\n", state)
	fmt.Fprintf(&b, "  1. Resolve the %s first (`git status`), then re-run `forge generate`.\n", state)
	fmt.Fprintf(&b, "  2. Run `forge generate --force` to regenerate the drifted files from the current templates after the merge content lands.\n")
	return errMidMergeTier1Drift{state: state, msg: b.String()}
}

// withDriftAdjudication returns steps with the fresh-render check appended
// when the stomp guard is in the plan and its check is not. Quarantined
// drift that is never adjudicated would pass silently — the hand-edit kept,
// the run green — so a narrowed plan (--steps) that keeps the guard must
// keep its second half. Every preset lists it (TestStepPresetsKeepDriftAdjudication);
// this is the backstop for one that does not.
func withDriftAdjudication(steps []GenStep) []GenStep {
	hasGuard, hasCheck := false, false
	for _, s := range steps {
		switch s.Name {
		case "check Tier-1 file-stomp guard":
			hasGuard = true
		case adjudicateDriftStep:
			hasCheck = true
		}
	}
	if !hasGuard || hasCheck {
		return steps
	}
	for _, s := range generateSteps() {
		if s.Name == adjudicateDriftStep {
			return append(steps, s)
		}
	}
	return steps
}
