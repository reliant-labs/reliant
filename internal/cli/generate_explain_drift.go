// `forge generate --explain-drift` — show what regeneration would
// change before the user picks an escape hatch.
//
// The Tier-1 stomp guard knows a file drifted (hash mismatch) but only
// has hashes for prior renders, not content — so it can't show the user
// WHAT changed. The actionable comparison is on-disk content vs a fresh
// render of the *current* templates: that diff is simultaneously "what
// did I hand-edit" and "what would --force destroy".
//
// Mechanism: instead of aborting at the guard, --explain-drift marks
// every drifted path side-render-only (checksums.AddSideRenderOnly) and
// lets the pipeline proceed. The emitters render normally, but writes
// for drifted paths land at .forge/render/<path>; the user's on-disk
// content and the checksum entries stay untouched (the entries are
// snapshot-restored at the end because stepRehashTracked would
// otherwise re-stamp the drifted on-disk hash and silently bless the
// drift). After the step loop, each drifted file is diffed against its
// parked render via `git diff --no-index`, bounded, and the run still
// FAILS with the standard drift report — the flag explains, it does not
// approve.
//
// Kept out of generate_pipeline.go (a parallel-edit hotspot): the
// pipeline file gains only the ctx fields and a three-line branch in
// stepCheckTier1Drift.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// explainDriftDiffCapLines bounds the printed diff per file. Generated
// files run to thousands of lines; past the first screenfuls the diff
// stops informing the keep-vs-regenerate decision. Var (not const) so
// tests can pin the truncation note with a tiny cap.
var explainDriftDiffCapLines = 120

// prepareExplainDrift is called by stepCheckTier1Drift instead of
// erroring when --explain-drift is set. It records the drift set on
// the context and redirects each drifted path's writes to
// .forge/render/. The drifted files (and their embedded markers) are
// never touched, so no state snapshot/restore dance is needed — the
// truth lives in the files.
func prepareExplainDrift(ctx *pipelineContext, drift []checksums.Tier1DriftEntry) {
	ctx.ExplainDriftEntries = drift
	for _, d := range drift {
		checksums.AddSideRenderOnly(d.Path)
	}
	fmt.Fprintf(os.Stderr, "🔎 --explain-drift: %d drifted Tier-1 file(s) — fresh renders will be parked under %s/ and diffed after the run\n",
		len(drift), checksums.RenderDir)
}

// finishExplainDrift runs after the step loop (success or failure).
// It prints the bounded diffs and returns the standard drift error so
// the run still exits non-zero. No-op (nil) when --explain-drift
// didn't trigger.
func finishExplainDrift(ctx *pipelineContext) error {
	if len(ctx.ExplainDriftEntries) == 0 {
		return nil
	}
	printExplainDriftDiffs(os.Stderr, ctx)
	// Same first-line contract as the guard step's error: the summary
	// (files + remedies) must precede the first newline so truncating
	// consumers still see something actionable.
	return fmt.Errorf("Tier-1 file-stomp guard (--explain-drift report above): %s\n%s",
		tier1DriftSummaryLine(ctx.ExplainDriftEntries),
		formatTier1DriftReport(ctx.ExplainDriftEntries))
}

// printExplainDriftDiffs prints, per drifted file, a unified diff of
// the on-disk content against the fresh render parked this run.
func printExplainDriftDiffs(w io.Writer, ctx *pipelineContext) {
	gitAvailable := true
	if _, err := exec.LookPath("git"); err != nil {
		gitAvailable = false
	}
	fmt.Fprintf(w, "\n🔎 --explain-drift: on-disk content vs fresh render of the current templates\n")
	for _, d := range ctx.ExplainDriftEntries {
		onDisk := filepath.Join(ctx.AbsPath, d.Path)
		render := filepath.Join(ctx.AbsPath, checksums.RenderDir, d.Path)
		fmt.Fprintf(w, "\n── %s ──\n", d.Path)
		if _, err := os.Stat(render); err != nil {
			fmt.Fprint(w, explainDriftNoRenderNote(d.Path))
			continue
		}
		if !gitAvailable {
			fmt.Fprintf(w, "   (git not on PATH — compare manually: %s vs %s)\n", d.Path, checksums.SideRenderRelPath(d.Path))
			continue
		}
		diff, err := gitNoIndexDiff(onDisk, render)
		if err != nil {
			fmt.Fprintf(w, "   (diff failed: %v)\n", err)
			continue
		}
		if strings.TrimSpace(diff) == "" {
			// Hash mismatch but textually identical renders shouldn't
			// happen (hashes are over exact bytes) — keep the branch for
			// safety so an empty diff isn't printed as silence.
			fmt.Fprintf(w, "   (no textual difference)\n")
			continue
		}
		lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
		truncated := false
		if len(lines) > explainDriftDiffCapLines {
			lines = lines[:explainDriftDiffCapLines]
			truncated = true
		}
		for _, line := range lines {
			fmt.Fprintf(w, "   %s\n", line)
		}
		if truncated {
			fmt.Fprintf(w, "   … diff truncated at %d lines — full render at %s\n",
				explainDriftDiffCapLines, checksums.SideRenderRelPath(d.Path))
		}
	}
	fmt.Fprintln(w)
}

// explainDriftNoRenderNote explains why a drifted file has no fresh render
// to diff against — and, when a diff is genuinely impossible, says so
// instead of implying one is coming.
//
// The two causes look identical on disk (nothing under .forge/render/) but
// are opposites in what the user should do next, and collapsing them into
// one message is what made this flag useless exactly when it was needed. A
// project upgrading across the frontend `_gen` rename ran `--explain-drift`
// on its dashboard_gen.tsx — the remedy the drift error itself names — and
// was told the emitter "may be gated off", which sent it looking for a
// --steps flag it had not passed and a feature it had not disabled. The
// truth was that forge had STOPPED EMITTING that path (the dashboard became
// the scaffold-once dashboard.tsx), so there was no render to produce, no
// flag to change, and nothing the user could do to make the diff appear.
//
// Tier1TargetSet is what separates them: every Tier-1 writer records its
// path there before any ownership check, so a drifted path ABSENT from it
// is one no emitter even attempted this run.
func explainDriftNoRenderNote(relPath string) string {
	if checksums.IsTier1Target(relPath) {
		// An emitter claimed the path but produced no side render: the
		// step really was skipped this run.
		return "   (no fresh render this run — the file's emitter step was gated off or excluded by --steps; re-run without `--steps=…` to get a diff)\n"
	}
	return fmt.Sprintf("   (forge no longer emits this file, so there is no current render to diff against — nothing you can pass will produce one.\n"+
		"    It carries a generated banner from an older forge that did emit it. The file is now yours: keep it and remove the stale banner, or delete it if nothing imports it.\n"+
		"    `forge project disown %s --reason \"<why>\"` records the decision and stops this guard reporting it.)\n", relPath)
}

// gitNoIndexDiff shells `git diff --no-index` over two absolute paths
// and returns the unified diff. Exit code 1 means "files differ" — the
// expected outcome, not an error.
func gitNoIndexDiff(a, b string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-index", "--unified=3", "--", a, b)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return stdout.String(), nil
	}
	return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
}
