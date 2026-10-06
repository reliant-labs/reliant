// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/provetest"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// ProveTestParams are the prove_test tool's parameters.
type ProveTestParams struct {
	Command   string   `json:"command" jsonschema:"required,description=The test command. It runs twice: once with the fix reverted and once with it in place. Keep it narrow — one package or one test — e.g. 'go test -run TestAdd ./internal/calc' or 'npx vitest run src/calc.test.ts'."`
	Files     []string `json:"files" jsonschema:"required,description=The IMPLEMENTATION files whose changes are the fix. Never the test file: the test must stay in place for both runs."`
	Baseline  string   `json:"baseline,omitempty" jsonschema:"description=Git revision the files are reverted to for the before-run (default: HEAD). A file absent at this revision is removed for the before-run."`
	Timeout   int      `json:"timeout,omitempty" jsonschema:"description=Timeout for EACH run in milliseconds (default: 300000\\, max: 480000)."`
	TailLines int      `json:"tail_lines,omitempty" jsonschema:"description=Lines of each run's output to include (default: 40)."`
	Repo      string   `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo the command runs in and relative paths resolve against: 'root' for the project root\\, or a repo name. Omit in single-repo projects."`
}

// ProveTestResponseMetadata is persisted with the tool call; it never reaches
// the model.
type ProveTestResponseMetadata struct {
	Verdict        string   `json:"verdict"`
	BaselineCommit string   `json:"baseline_commit"`
	Files          []string `json:"files"`
	BeforeExitCode *int     `json:"before_exit_code,omitempty"`
	AfterExitCode  *int     `json:"after_exit_code,omitempty"`
	Conflicts      int      `json:"conflicts,omitempty"`
	Recovered      int      `json:"recovered,omitempty"`
}

type proveTestTool struct{}

const (
	ProveTestToolName = "prove_test"

	proveTestDefaultTimeout = 5 * time.Minute
	// proveTestMaxTimeout is per run. Two of them, plus the lock wait, must
	// fit under toolexec.DefaultToolTimeout or the executor cancels the call
	// mid-run — which still restores the files, but answers nothing.
	proveTestMaxTimeout = 8 * time.Minute
	proveTestLockWait   = 2 * time.Minute
	// proveTestBackstopSlack is how far past its own timeout a run may go
	// before the engine stops waiting for it: enough for the daemon's
	// graceful terminate-then-kill to finish first.
	proveTestBackstopSlack = 30 * time.Second

	proveTestDefaultTailLines = 40
	proveTestMaxTailLines     = 400
	// proveTestMaxTailBytes bounds each run's excerpt independently of the
	// line count, so one minified-JS line cannot eat the whole result.
	proveTestMaxTailBytes = 6000

	proveTestDescription = `Prove a regression test actually tests your fix: one call runs the test with the
fix reverted (it must FAIL) and again with the fix in place (it must PASS). This
replaces patching the old code back in by hand — and never use git stash for it.

WHEN TO USE:
- After writing a fix and a test for it, before reporting the work done.
  "Confirm a new test fails before the fix and passes after" is exactly this call.
- When you are not sure a test exercises the code you changed.

HOW IT WORKS:
1. Snapshots the exact bytes and mode of every file in files.
2. Writes each file's baseline version (default: git HEAD; any git revision works).
   A file that does not exist at baseline is removed for the run; a file your fix
   deleted is put back.
3. Runs command and expects a NON-ZERO exit.
4. Restores your files — always, including on timeout, cancellation or a crash —
   and verifies them byte for byte.
5. Runs command again and expects exit 0.

PARAMETERS THAT MATTER:
- files: the IMPLEMENTATION files of the fix. NOT the test file — the test must
  stay in place for both runs.
- command: as narrow as possible — one package, one test
  ('go test -run TestX ./pkg/foo', 'npx vitest run src/x.test.ts'). It runs twice.

VERDICTS:
- proven: fails without the fix, passes with it.
- passes_without_fix: the test does not exercise the change. Strengthen it.
- fails_with_fix: the fix does not make the test pass.
- baseline_does_not_compile: the before-run failed to BUILD (typical when the fix
  adds a symbol the test calls). That is NOT proof; report it as such.
- conflict: another writer changed a named file mid-run. Their content was kept
  and yours preserved; the result says where.
- restore_failed / inconclusive: see the result text.

SHARED CHECKOUTS — READ THIS:
While the before-run executes, the named files hold their OLD content on disk.
Anything else building or testing in the same working tree during that window
sees the old code. The window is one run of command, which is why it should be
narrow. Two prove_test calls on the same file wait for each other, and if another
agent edits a named file meanwhile, prove_test keeps their edit instead of
overwriting it.`
)

func NewProveTestTool() Tool {
	return NewToolWrapper[ProveTestParams, ToolResponse](&proveTestTool{})
}

func (t *proveTestTool) Name() string { return ProveTestToolName }

func (t *proveTestTool) Description() string { return proveTestDescription }

// RequiresPermission is always true: the tool runs an arbitrary command and,
// for the length of one run, rewrites files in the working tree.
func (t *proveTestTool) RequiresPermission(params ProveTestParams) (bool, error) {
	if strings.TrimSpace(params.Command) == "" {
		return false, fmt.Errorf("missing command")
	}
	return true, nil
}

func (t *proveTestTool) Execute(tc *rctx.ToolContext, params ProveTestParams) (ToolResponse, error) {
	if tc.Daemon == nil {
		return NewTextErrorResponse("prove_test requires a connected daemon"), nil
	}
	// The engine reads and writes the files directly, so it must be running
	// on the machine that holds them. Daemon placement guarantees that; a
	// remote client here means it was dispatched somewhere it cannot work.
	if _, remote := tc.Daemon.(*daemon.RemoteClient); remote {
		return NewTextErrorResponse("prove_test must execute on the daemon that holds the files, but this call is running elsewhere"), nil
	}
	command := strings.TrimSpace(params.Command)
	if command == "" {
		return NewTextErrorResponse("command is required"), nil
	}
	if len(params.Files) == 0 {
		return NewTextErrorResponse("files is required: name the implementation files your fix changed"), nil
	}

	workspace, err := GetWorkingDirectory(tc)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	dir, err := ResolveRepoPath(tc, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	timeout := proveTestDefaultTimeout
	if params.Timeout > 0 {
		timeout = min(time.Duration(params.Timeout)*time.Millisecond, proveTestMaxTimeout)
	}
	tailLines := proveTestDefaultTailLines
	if params.TailLines > 0 {
		tailLines = min(params.TailLines, proveTestMaxTailLines)
	}

	ctx := tc.Context
	if ctx == nil {
		ctx = context.Background()
	}
	run := func(ctx context.Context) provetest.RunOutcome {
		res, runErr := tc.Daemon.RunCommand(ctx, &daemon.RunCommandRequest{
			Command:    command,
			WorkingDir: dir,
			TimeoutMs:  int(timeout.Milliseconds()),
		})
		if runErr != nil {
			return provetest.RunOutcome{ExitCode: -1, Err: runErr}
		}
		if res.Backgrounded {
			// Detached mid-run: it is still going, against files that are
			// about to be restored, so its result can prove nothing.
			return provetest.RunOutcome{ExitCode: -1, Err: fmt.Errorf(
				"the command was moved to the background (process %s) before it finished, so its result cannot be attributed to either version", res.ProcessID)}
		}
		output := res.Combined
		if output == "" {
			output = res.Stdout + res.Stderr
		}
		return provetest.RunOutcome{
			ExitCode: res.ExitCode,
			Output:   output,
			Duration: time.Duration(res.DurationMs) * time.Millisecond,
			TimedOut: res.TimedOut,
		}
	}

	report, err := provetest.Prove(ctx, provetest.Options{
		Workspace:  workspace,
		Dir:        dir,
		Files:      params.Files,
		Baseline:   params.Baseline,
		Run:        run,
		RunTimeout: timeout + proveTestBackstopSlack,
		LockWait:   proveTestLockWait,
	})
	if err != nil {
		msg := err.Error()
		if report != nil {
			if len(report.Recovered) > 0 {
				msg += "\n\nRECOVERED from an earlier, interrupted prove_test run:\n" + renderRestores(report.Recovered)
			}
			if len(report.Restored) > 0 {
				msg += "\n\nFiles swapped before the failure:\n" + renderRestores(report.Restored)
			}
		}
		return NewTextErrorResponse(msg), nil
	}

	return WithResponseMetadata(NewTextResponse(renderProveTest(report, tailLines)), proveTestMetadata(report)), nil
}

var proveTestVerdictText = map[provetest.Verdict]string{
	provetest.VerdictProven: "The test fails with the fix reverted and passes with it in place: it tests your change.",
	provetest.VerdictPassesWithoutFix: "The test PASSES with the fix reverted, so it does not test your change. " +
		"Make it assert the behaviour the fix introduces, then prove it again.",
	provetest.VerdictFailsWithFix: "The test FAILS with your fix in place. Either the fix is incomplete or the test is wrong; the after-run output says which.",
	provetest.VerdictBaselineDoesNotCompile: "The before-run failed to BUILD, not on an assertion. That is common when the fix adds a symbol the test calls, " +
		"and it is NOT proof: the test never ran against the old behaviour. Make the test compile against the old code " +
		"(exercise the behaviour through an API that already existed), or report this result as it is rather than as proven.",
	provetest.VerdictConflict: "Someone else wrote a named file while the fix was reverted. Their content was KEPT on disk and your version was " +
		"preserved at the path below. Reconcile that file before anything else; the after-run was skipped.",
	provetest.VerdictRestoreFailed: "A named file could NOT be restored. Your version is preserved at the path below, and the next prove_test call " +
		"in this repository retries the restore. Check the file before doing anything else.",
	provetest.VerdictInconclusive: "A run did not complete, so nothing was established.",
}

func renderProveTest(r *provetest.Report, tailLines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VERDICT: %s\n%s\n", r.Verdict, proveTestVerdictText[r.Verdict])
	if r.CompileFailure != nil {
		fmt.Fprintf(&b, "Build failure marker (%s): %s\n", r.CompileFailure.Kind, r.CompileFailure.Line)
	}

	if len(r.Recovered) > 0 {
		b.WriteString("\nRECOVERED from an earlier, interrupted prove_test run:\n")
		b.WriteString(renderRestores(r.Recovered))
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\nBaseline: %s (%s)\nFiles:\n", r.Baseline, shortCommit(r.BaselineCommit))
	restoredByPath := make(map[string]provetest.RestoreResult, len(r.Restored))
	for _, res := range r.Restored {
		restoredByPath[res.Path] = res
	}
	for _, f := range r.Files {
		fmt.Fprintf(&b, "  - %s — %s; %s\n", f.Path, changeText(f.Change), restoreLine(restoredByPath[f.Path]))
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "  - %s — identical at baseline, so not part of the fix; left alone\n", s)
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nWARNING: %s\n", w)
	}
	for _, h := range r.Hints {
		fmt.Fprintf(&b, "\nHINT: %s\n", h)
	}

	b.WriteString("\n")
	b.WriteString(renderRun("BEFORE (fix reverted)", r.Before, false, tailLines))
	if r.After != nil {
		b.WriteString("\n")
		b.WriteString(renderRun("AFTER (fix in place)", r.After, true, tailLines))
	} else {
		fmt.Fprintf(&b, "\nAFTER (fix in place) — skipped: %s\n", r.AfterSkipped)
	}
	return b.String()
}

func changeText(c provetest.ChangeKind) string {
	switch c {
	case provetest.ChangeRemoved:
		return "absent at baseline, removed for the before-run"
	case provetest.ChangeRecreated:
		return "deleted by your fix, put back for the before-run"
	default:
		return "reverted to baseline for the before-run"
	}
}

func restoreLine(res provetest.RestoreResult) string {
	switch res.Outcome {
	case provetest.RestoreOK:
		if res.Detail != "" {
			return "restored byte for byte (" + res.Detail + ")"
		}
		return "restored byte for byte"
	case provetest.RestoreConflict:
		s := "CONFLICT: " + res.Detail
		if res.Preserved != "" {
			s += "; your version is preserved at " + res.Preserved
		}
		return s
	case provetest.RestoreFailed:
		s := "RESTORE FAILED: " + res.Detail
		if res.Preserved != "" {
			s += "; your version is preserved at " + res.Preserved
		}
		return s
	}
	return "not restored"
}

func renderRestores(results []provetest.RestoreResult) string {
	lines := make([]string, 0, len(results))
	for _, res := range results {
		lines = append(lines, fmt.Sprintf("  - %s: %s", res.Path, restoreLine(res)))
	}
	return strings.Join(lines, "\n")
}

func renderRun(label string, run *provetest.RunOutcome, wantPass bool, tailLines int) string {
	if run == nil {
		return label + " — did not run\n"
	}
	var b strings.Builder
	expect := "expected non-zero"
	if wantPass {
		expect = "expected 0"
	}
	switch {
	case run.TimedOut:
		fmt.Fprintf(&b, "%s — TIMED OUT after %s — %s ✗\n", label, formatWaitDuration(run.Duration), expect)
	case run.Err != nil:
		fmt.Fprintf(&b, "%s — did not complete: %v\n", label, run.Err)
	default:
		met := (run.ExitCode == 0) == wantPass
		mark := "✓"
		if !met {
			mark = "✗"
		}
		fmt.Fprintf(&b, "%s — exit %d after %s — %s %s\n", label, run.ExitCode, formatWaitDuration(run.Duration), expect, mark)
	}
	if out := tailOutput(run.Output, tailLines); out != "" {
		b.WriteString(out)
		if !strings.HasSuffix(out, "\n") {
			b.WriteString("\n")
		}
	} else if run.Err == nil {
		b.WriteString("(no output)\n")
	}
	return b.String()
}

// tailOutput keeps the end of a run's output — where test runners put the
// failure summary — bounded by lines and bytes.
func tailOutput(output string, lines int) string {
	output = strings.TrimRight(output, "\n")
	if output == "" {
		return ""
	}
	total := strings.Count(output, "\n") + 1
	tail := getTailLines(output, lines)
	if len(tail) > proveTestMaxTailBytes {
		tail = tail[len(tail)-proveTestMaxTailBytes:]
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		}
	}
	if shown := strings.Count(tail, "\n") + 1; shown < total {
		return fmt.Sprintf("... (%d earlier lines omitted)\n%s", total-shown, tail)
	}
	return tail
}

func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func proveTestMetadata(r *provetest.Report) ProveTestResponseMetadata {
	meta := ProveTestResponseMetadata{
		Verdict:        string(r.Verdict),
		BaselineCommit: r.BaselineCommit,
		Recovered:      len(r.Recovered),
	}
	for _, f := range r.Files {
		meta.Files = append(meta.Files, f.Path)
	}
	for _, res := range r.Restored {
		if res.Outcome == provetest.RestoreConflict {
			meta.Conflicts++
		}
	}
	if r.Before != nil && r.Before.Err == nil {
		code := r.Before.ExitCode
		meta.BeforeExitCode = &code
	}
	if r.After != nil && r.After.Err == nil {
		code := r.After.ExitCode
		meta.AfterExitCode = &code
	}
	return meta
}
