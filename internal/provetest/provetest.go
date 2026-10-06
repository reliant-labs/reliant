// Copyright (c) 2025 Reliant Labs

// Package provetest proves that a test exercises a fix: it runs the test with
// the fix reverted, expecting a failure, then with the fix in place, expecting
// a pass. It is the engine behind the prove_test agent tool.
//
// THE WORKING TREE IS SHARED, AND THIS PACKAGE CHANGES IT. While the
// before-run executes, each named file holds its baseline bytes on disk, so
// anyone building or testing in the same checkout during that window compiles
// the old code. That is the price of testing the files where they live — an
// isolated copy would lose everything git does not track (node_modules,
// generated code, env files) and break relative go.work paths — so the design
// is organised around keeping that window to exactly one test run and making
// the restore unconditional:
//
//   - The baseline is written immediately before the run starts and the
//     snapshot restored the moment it returns: on success, failure, timeout,
//     cancellation and panic alike.
//   - The snapshot is journalled under the repository's git dir BEFORE the
//     swap. A process that dies mid-run (the app quits, the OOM killer fires)
//     loses nothing: the next Prove in that repository puts it back.
//   - Restore never clobbers. A file that changed during the window was
//     written by someone else; their bytes are kept, ours are preserved under
//     the git dir, and the result reports a conflict.
//   - A per-file OS lock serialises two proofs touching the same file, across
//     goroutines and across processes.
//
// Nothing here runs git stash, checkout or reset. Nothing in the working tree
// is touched except the named files and any directory created to hold a file
// that exists only at baseline, which is removed again afterwards.
package provetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Verdict is the outcome of a proof.
type Verdict string

const (
	// VerdictProven: the test fails without the fix and passes with it.
	VerdictProven Verdict = "proven"
	// VerdictPassesWithoutFix: the test passes with the fix reverted, so it
	// does not exercise the change.
	VerdictPassesWithoutFix Verdict = "passes_without_fix"
	// VerdictFailsWithFix: the test fails with the fix in place.
	VerdictFailsWithFix Verdict = "fails_with_fix"
	// VerdictBaselineDoesNotCompile: the before-run failed to BUILD rather
	// than failing an assertion. Common when the fix adds a symbol the test
	// calls, and not proof that the test detects the missing behaviour.
	VerdictBaselineDoesNotCompile Verdict = "baseline_does_not_compile"
	// VerdictConflict: something else wrote a named file while the fix was
	// reverted. Their content was kept; ours was preserved.
	VerdictConflict Verdict = "conflict"
	// VerdictRestoreFailed: a file could not be put back. Its bytes are
	// preserved in the journal and the next Prove in the repository retries.
	VerdictRestoreFailed Verdict = "restore_failed"
	// VerdictInconclusive: a run did not complete (timed out, cancelled,
	// could not start), so nothing was established.
	VerdictInconclusive Verdict = "inconclusive"
)

// Defaults for Options fields left zero.
const (
	DefaultRunTimeout = 10 * time.Minute
	DefaultLockWait   = 2 * time.Minute

	// cancelGrace is how long a cancelled run is given to actually stop before
	// the files are restored underneath it. Restoring while the command is
	// still alive would let a late write of its own land after the restore,
	// unseen; waiting forever would hold the baseline in place for everyone.
	cancelGrace = 15 * time.Second
)

// Runner executes the test command once and reports how it ended. Prove calls
// it twice: once with the fix reverted, once with it in place. It must honour
// ctx: a cancelled ctx means the tool call was abandoned.
type Runner func(ctx context.Context) RunOutcome

// RunOutcome is one execution of the test command.
type RunOutcome struct {
	ExitCode int
	// Output is the command's interleaved stdout and stderr.
	Output   string
	Duration time.Duration
	// TimedOut reports that the command was stopped by a timeout.
	TimedOut bool
	// Err is set when the command did not run to completion at all: it could
	// not start, was cancelled, panicked, or was detached into the background.
	// ExitCode is meaningless when Err is set.
	Err error
}

// completed reports whether the command ran to an exit status of its own.
func (o *RunOutcome) completed() bool {
	return o != nil && o.Err == nil && !o.TimedOut
}

// Options configures one proof.
type Options struct {
	// Workspace is the absolute root the caller may touch. A file resolving
	// outside it — through "..", an absolute path or a symlinked directory —
	// is refused.
	Workspace string
	// Dir is what relative Files resolve against. Defaults to Workspace.
	Dir string
	// Files are the implementation files whose changes constitute the fix.
	Files []string
	// Baseline is the git revision the files are reverted to. Empty means HEAD.
	Baseline string
	// Run executes the test command.
	Run Runner
	// RunTimeout is a backstop for one run: a Runner still running this long
	// after it was called is abandoned and the files restored anyway. The
	// Runner should enforce its own, shorter timeout; this guards one that
	// does not.
	RunTimeout time.Duration
	// LockWait bounds how long Prove waits for another proof holding one of
	// the same files.
	LockWait time.Duration
}

// ChangeKind says what the before-run did to a file.
type ChangeKind string

const (
	// ChangeReverted: the file's baseline content replaced the fix.
	ChangeReverted ChangeKind = "reverted"
	// ChangeRemoved: the file does not exist at baseline (the fix added it),
	// so it was removed for the before-run.
	ChangeRemoved ChangeKind = "removed"
	// ChangeRecreated: the file exists only at baseline (the fix deleted it),
	// so it was put back for the before-run.
	ChangeRecreated ChangeKind = "recreated"
)

// FileChange is one file the before-run swapped.
type FileChange struct {
	// Path is relative to the repository root, slash-separated.
	Path   string
	Change ChangeKind
}

// RestoreOutcome says what happened when a file was put back.
type RestoreOutcome string

const (
	// RestoreOK: the file holds exactly the bytes and mode it had before.
	RestoreOK RestoreOutcome = "restored"
	// RestoreConflict: the file changed while it was swapped, so the other
	// writer's content was kept and ours preserved.
	RestoreConflict RestoreOutcome = "conflict"
	// RestoreFailed: the file could not be put back; ours is preserved and a
	// later Prove in the repository retries.
	RestoreFailed RestoreOutcome = "failed"
)

// RestoreResult is the restore of one file.
type RestoreResult struct {
	Path    string
	Outcome RestoreOutcome
	// Preserved is the absolute path of the bytes this proof could not put
	// back (set for RestoreConflict and RestoreFailed when the file existed).
	Preserved string
	Detail    string
}

// Report is the result of a proof that got as far as swapping files.
type Report struct {
	Verdict Verdict
	// Baseline is the revision as requested; BaselineCommit what it resolved to.
	Baseline       string
	BaselineCommit string
	// RepoRoot is the repository the files belong to.
	RepoRoot string
	Files    []FileChange
	// Skipped lists named files identical at baseline. They are not part of
	// the fix, so they were left alone.
	Skipped []string
	Before  *RunOutcome
	// After is nil when the after-run was skipped; AfterSkipped says why.
	After        *RunOutcome
	AfterSkipped string
	// CompileFailure is the before-run output line that marks a build failure
	// rather than a failed assertion, with what kind of failure it marks.
	CompileFailure *Marker
	Restored       []RestoreResult
	// Recovered are files an earlier, interrupted proof left swapped, found
	// and handled before this one started.
	Recovered []RestoreResult
	// Warnings are about how the tool was called; Hints about the output.
	Warnings []string
	Hints    []string
}

// Prove runs opts.Run with the named files reverted to baseline, restores
// them, verifies the restore byte for byte, and runs it again.
//
// An error means the proof did not run — the request was refused or could not
// be prepared — and no named file was left changed. When a Report comes back
// alongside an error it says what WAS done first: files recovered from an
// earlier interrupted proof, and the restore of anything already swapped. A
// panic inside Prove is converted to such an error after the restore runs.
func Prove(ctx context.Context, opts Options) (report *Report, err error) {
	if opts.Run == nil {
		return nil, errors.New("provetest: no runner")
	}
	if len(opts.Files) == 0 {
		return nil, errors.New("files is required: name the implementation files your fix changed")
	}
	if opts.RunTimeout <= 0 {
		opts.RunTimeout = DefaultRunTimeout
	}
	if opts.LockWait <= 0 {
		opts.LockWait = DefaultLockWait
	}
	baseline := strings.TrimSpace(opts.Baseline)
	if baseline == "" {
		baseline = "HEAD"
	}

	files, err := resolveFiles(opts.Workspace, opts.Dir, opts.Files)
	if err != nil {
		return nil, err
	}
	repo, files, err := openRepo(ctx, files)
	if err != nil {
		return nil, err
	}
	commit, err := repo.resolveCommit(ctx, baseline)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.baseline, err = repo.baselineState(ctx, commit, f.rel, baseline); err != nil {
			return nil, err
		}
	}

	held, err := repo.lockFiles(ctx, files, opts.LockWait)
	if err != nil {
		return nil, err
	}
	defer held.release()

	report = &Report{
		Baseline:       baseline,
		BaselineCommit: commit,
		RepoRoot:       repo.top,
		Warnings:       testFileWarnings(files),
	}
	// Recover BEFORE snapshotting: an earlier proof that died mid-run leaves a
	// file holding its baseline, and snapshotting that would make the baseline
	// the thing restored — losing the fix for good.
	report.Recovered = repo.recoverOrphans(held)

	// From here on an error returns the report too: it carries any recovery
	// just done, which the caller must hear about even though nothing ran.
	var targets []*target
	for _, f := range files {
		current, readErr := readState(f.abs)
		if readErr != nil {
			return report, readErr
		}
		switch {
		case !current.Exists && !f.baseline.Exists:
			return report, fmt.Errorf("%s does not exist on disk or at %s: nothing to revert", f.rel, baseline)
		case sameContent(current, f.baseline):
			report.Skipped = append(report.Skipped, f.rel)
			continue
		}
		targets = append(targets, &target{file: f, original: current})
	}
	if len(targets) == 0 {
		return report, fmt.Errorf("every named file is identical at %s, so there is no fix to revert: name the implementation files your change modified (not the test file)", baseline)
	}

	p := &proof{repo: repo, targets: targets, report: report, runID: newRunID(), commit: commit}

	// Runs LAST of Prove's defers bar the lock release, so by the time it
	// recovers a panic swapAndRun's own deferred restore has already run.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("prove_test panicked (%v); %s", r, restoreSummary(report.Restored))
		}
	}()

	report.Before, err = p.swapAndRun(ctx, opts.Run, opts.RunTimeout)
	if err != nil {
		// Nothing was run. Any file already swapped has been restored, and
		// report.Restored says how that went.
		return report, err
	}

	if blocking := restoreProblem(report.Restored); blocking != "" {
		// The fix is not what is on disk (someone else's content is, or our
		// restore failed), so an after-run would test the wrong thing.
		report.AfterSkipped = blocking
	} else if ctx.Err() != nil {
		report.AfterSkipped = "the tool call was cancelled"
	} else {
		after := runGuarded(ctx, opts.Run, opts.RunTimeout)
		report.After = &after
		report.Warnings = append(report.Warnings, p.changedSinceRestore()...)
	}

	classify(report)
	return report, nil
}

// restoreProblem names why an after-run would be meaningless, or "".
func restoreProblem(results []RestoreResult) string {
	for _, r := range results {
		switch r.Outcome {
		case RestoreConflict:
			return "a named file was changed by someone else while the fix was reverted, so the fix is not what is on disk"
		case RestoreFailed:
			return "a named file could not be restored, so the fix is not what is on disk"
		}
	}
	return ""
}

func restoreSummary(results []RestoreResult) string {
	if len(results) == 0 {
		return "no files had been swapped"
	}
	parts := make([]string, 0, len(results))
	for _, r := range results {
		s := fmt.Sprintf("%s: %s", r.Path, r.Outcome)
		if r.Preserved != "" {
			s += " (your version preserved at " + r.Preserved + ")"
		}
		parts = append(parts, s)
	}
	return "restore: " + strings.Join(parts, "; ")
}

// runGuarded calls run with a backstop. A Runner that panics is reported as a
// failed run instead of taking the process down — which matters here, because
// a panic in a goroutine of its own would kill the process without running the
// caller's deferred restore. A Runner that ignores cancellation or overruns
// the backstop is abandoned so the restore can proceed.
func runGuarded(ctx context.Context, run Runner, backstop time.Duration) RunOutcome {
	start := time.Now()
	done := make(chan RunOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- RunOutcome{ExitCode: -1, Duration: time.Since(start), Err: fmt.Errorf("the command runner panicked: %v", r)}
			}
		}()
		done <- run(ctx)
	}()

	timer := time.NewTimer(backstop)
	defer timer.Stop()
	select {
	case out := <-done:
		if out.Err == nil && ctx.Err() != nil {
			out.Err = fmt.Errorf("cancelled: %w", ctx.Err())
		}
		return out
	case <-ctx.Done():
		select {
		case out := <-done:
			if out.Err == nil {
				out.Err = fmt.Errorf("cancelled: %w", ctx.Err())
			}
			return out
		case <-time.After(cancelGrace):
			return RunOutcome{ExitCode: -1, Duration: time.Since(start),
				Err: fmt.Errorf("cancelled (%v), and the command had not stopped %s later", ctx.Err(), cancelGrace)}
		}
	case <-timer.C:
		return RunOutcome{ExitCode: -1, Duration: time.Since(start), TimedOut: true,
			Err: fmt.Errorf("the command was still running %s after it started and was abandoned", backstop)}
	}
}
