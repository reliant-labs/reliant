// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	buggyCalc = "package calc\n\nfunc Add(a, b int) int { return a - b }\n"
	fixedCalc = "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
)

// gitRepo creates a repository whose HEAD holds the buggy calc.go and whose
// working tree holds the fix — the state an agent is in after writing a fix.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	writeFile(t, dir, "calc.go", buggyCalc)
	commitAll(t, dir, "buggy add")
	writeFile(t, dir, "calc.go", fixedCalc)
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.name=prove-test", "-c", "user.email=prove-test@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", msg)
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

// observingRunner fails while calc.go holds the bug and passes once it holds
// the fix, recording what each run saw — the behaviour of a real regression
// test, minus the compiler.
type observingRunner struct {
	dir  string
	mu   sync.Mutex
	seen []string
}

func (o *observingRunner) run(ctx context.Context) RunOutcome {
	content, err := os.ReadFile(filepath.Join(o.dir, "calc.go"))
	if err != nil {
		return RunOutcome{Err: err}
	}
	o.mu.Lock()
	o.seen = append(o.seen, string(content))
	o.mu.Unlock()
	if strings.Contains(string(content), "a - b") {
		return RunOutcome{ExitCode: 1, Output: "--- FAIL: TestAdd\n    calc_test.go:6: Add(1, 2) = -1, want 3\nFAIL"}
	}
	return RunOutcome{ExitCode: 0, Output: "ok  \texample.com/calc\t0.01s"}
}

func prove(t *testing.T, dir string, run Runner, files ...string) (*Report, error) {
	t.Helper()
	return Prove(context.Background(), Options{Workspace: dir, Files: files, Run: run})
}

func activeJournalEntries(t *testing.T, dir string) []string {
	t.Helper()
	gitDir := git(t, dir, "rev-parse", "--absolute-git-dir")
	return journal{dir: filepath.Join(gitDir, stateDirName)}.keys()
}

func TestProve_Proven(t *testing.T) {
	dir := gitRepo(t)
	runner := &observingRunner{dir: dir}

	report, err := prove(t, dir, runner.run, "calc.go")
	require.NoError(t, err)

	assert.Equal(t, VerdictProven, report.Verdict)
	assert.Equal(t, []string{buggyCalc, fixedCalc}, runner.seen, "the before-run must see the baseline and the after-run the fix")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
	assert.Equal(t, []FileChange{{Path: "calc.go", Change: ChangeReverted}}, report.Files)
	require.Len(t, report.Restored, 1)
	assert.Equal(t, RestoreOK, report.Restored[0].Outcome)
	assert.Equal(t, 1, report.Before.ExitCode)
	assert.Equal(t, 0, report.After.ExitCode)
	assert.Empty(t, activeJournalEntries(t, dir), "a finished proof leaves no journal entry behind")
	assert.Equal(t, git(t, dir, "rev-parse", "HEAD"), report.BaselineCommit)
}

func TestProve_PassesWithoutFix(t *testing.T) {
	dir := gitRepo(t)
	report, err := prove(t, dir, func(context.Context) RunOutcome { return RunOutcome{Output: "ok"} }, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictPassesWithoutFix, report.Verdict)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_FailsWithFix(t *testing.T) {
	dir := gitRepo(t)
	report, err := prove(t, dir, func(context.Context) RunOutcome { return RunOutcome{ExitCode: 1, Output: "FAIL"} }, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictFailsWithFix, report.Verdict)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_BaselineCompileFailureIsNotProof(t *testing.T) {
	dir := gitRepo(t)
	run := func(context.Context) RunOutcome {
		if strings.Contains(readFile(t, dir, "calc.go"), "a - b") {
			return RunOutcome{ExitCode: 1, Output: "# example.com/calc\n./calc_test.go:6:9: undefined: Mul\nFAIL\texample.com/calc [build failed]"}
		}
		return RunOutcome{Output: "ok"}
	}
	report, err := prove(t, dir, run, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictBaselineDoesNotCompile, report.Verdict)
	require.NotNil(t, report.CompileFailure)
	assert.Equal(t, "Go build failure", report.CompileFailure.Kind)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_RestoresWhenTheRunnerTimesOut(t *testing.T) {
	dir := gitRepo(t)
	run := func(context.Context) RunOutcome {
		return RunOutcome{ExitCode: -1, TimedOut: true, Output: "still compiling..."}
	}
	report, err := prove(t, dir, run, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictInconclusive, report.Verdict)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
	assert.NotEmpty(t, report.Hints)
}

// A runner that ignores both its own timeout and cancellation must not hold
// the baseline in place forever: the backstop abandons it and restores.
func TestProve_RestoresWhenTheRunnerHangs(t *testing.T) {
	dir := gitRepo(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	run := func(context.Context) RunOutcome {
		<-release
		return RunOutcome{}
	}

	start := time.Now()
	report, err := Prove(context.Background(), Options{
		Workspace: dir, Files: []string{"calc.go"}, Run: run, RunTimeout: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, VerdictInconclusive, report.Verdict)
	assert.True(t, report.Before.TimedOut)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"), "the fix must be back while the hung command is still running")
	assert.Empty(t, activeJournalEntries(t, dir))
}

func TestProve_RestoresOnCancellation(t *testing.T) {
	dir := gitRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	var sawBaseline atomic.Bool
	run := func(ctx context.Context) RunOutcome {
		sawBaseline.Store(strings.Contains(readFile(t, dir, "calc.go"), "a - b"))
		close(started)
		<-ctx.Done()
		return RunOutcome{ExitCode: -1, Err: ctx.Err()}
	}
	go func() {
		<-started
		cancel()
	}()

	report, err := Prove(ctx, Options{Workspace: dir, Files: []string{"calc.go"}, Run: run})
	require.NoError(t, err)

	assert.True(t, sawBaseline.Load())
	assert.Equal(t, VerdictInconclusive, report.Verdict)
	assert.Nil(t, report.After)
	assert.Contains(t, report.AfterSkipped, "cancelled")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
	assert.Empty(t, activeJournalEntries(t, dir))
}

func TestProve_RestoresWhenTheRunnerPanics(t *testing.T) {
	dir := gitRepo(t)
	calls := 0
	run := func(context.Context) RunOutcome {
		calls++
		if calls == 1 {
			panic("runner exploded")
		}
		return RunOutcome{}
	}
	report, err := prove(t, dir, run, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictInconclusive, report.Verdict)
	require.Error(t, report.Before.Err)
	assert.Contains(t, report.Before.Err.Error(), "runner exploded")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_RestoresWhenProveItselfPanics(t *testing.T) {
	dir := gitRepo(t)
	testHookAfterSwap = func() { panic("boom after swap") }
	t.Cleanup(func() { testHookAfterSwap = nil })

	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom after swap")
	assert.Contains(t, err.Error(), "calc.go: restored")
	require.NotNil(t, report)
	require.Len(t, report.Restored, 1)
	assert.Equal(t, RestoreOK, report.Restored[0].Outcome)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
	assert.Empty(t, activeJournalEntries(t, dir))
}

// Another agent writes the file while it holds the baseline. Their write is
// kept, never clobbered; the fix is preserved where the result says.
func TestProve_ConcurrentModificationKeepsTheirContent(t *testing.T) {
	const theirs = "package calc\n\n// another agent's edit\nfunc Add(a, b int) int { return b + a }\n"
	dir := gitRepo(t)
	afterRuns := 0
	run := func(context.Context) RunOutcome {
		if strings.Contains(readFile(t, dir, "calc.go"), "a - b") {
			writeFile(t, dir, "calc.go", theirs)
			return RunOutcome{ExitCode: 1}
		}
		afterRuns++
		return RunOutcome{}
	}

	report, err := prove(t, dir, run, "calc.go")
	require.NoError(t, err)

	assert.Equal(t, VerdictConflict, report.Verdict)
	assert.Equal(t, theirs, readFile(t, dir, "calc.go"), "the other writer's content must survive")
	require.Len(t, report.Restored, 1)
	got := report.Restored[0]
	assert.Equal(t, RestoreConflict, got.Outcome)
	require.NotEmpty(t, got.Preserved)
	preserved, readErr := os.ReadFile(got.Preserved)
	require.NoError(t, readErr)
	assert.Equal(t, fixedCalc, string(preserved), "the fix must be recoverable from the reported path")
	assert.Zero(t, afterRuns, "the after-run would test their content, not the fix")
	assert.Nil(t, report.After)
	assert.NotEmpty(t, report.AfterSkipped)
	assert.Empty(t, activeJournalEntries(t, dir), "a conflict is moved out of the active journal so recovery never acts on it")
}

// Rewriting the file with the SAME baseline bytes is still someone else's
// write — a deliberate revert, say — and must not be overwritten either.
func TestProve_IdenticalRewriteIsStillAConflict(t *testing.T) {
	dir := gitRepo(t)
	run := func(context.Context) RunOutcome {
		path := filepath.Join(dir, "calc.go")
		if content := readFile(t, dir, "calc.go"); strings.Contains(content, "a - b") {
			require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			later := time.Now().Add(2 * time.Second)
			require.NoError(t, os.Chtimes(path, later, later))
			return RunOutcome{ExitCode: 1}
		}
		return RunOutcome{}
	}
	report, err := prove(t, dir, run, "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictConflict, report.Verdict)
	assert.Equal(t, buggyCalc, readFile(t, dir, "calc.go"))
}

// Every escape target here is a real, modified file in a git repository, so
// without the workspace check the proof would go ahead and rewrite it — the
// refusal is the only thing that can make these cases pass.
func TestProve_RefusesPathsOutsideTheWorkspace(t *testing.T) {
	// The workspace is a subdirectory of the repository (a project inside a
	// monorepo), so "../calc.go" is in the same repo yet outside the workspace.
	repoRoot := gitRepo(t)
	workspace := filepath.Join(repoRoot, "app")
	writeFile(t, repoRoot, "app/app.go", "package app\n")

	other := gitRepo(t) // a second repository, reachable only through links
	require.NoError(t, os.Symlink(other, filepath.Join(workspace, "linked")))
	require.NoError(t, os.Symlink(filepath.Join(repoRoot, "calc.go"), filepath.Join(workspace, "link.go")))

	cases := map[string]string{
		"dot-dot into the same repo":  "../calc.go",
		"absolute path":               filepath.Join(repoRoot, "calc.go"),
		"nested dot-dot":              "sub/../../calc.go",
		"symlinked directory":         "linked/calc.go",
		"symlinked file":              "link.go",
		"another repo, absolute path": filepath.Join(other, "calc.go"),
		"the workspace itself":        ".",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			_, err := prove(t, workspace, func(context.Context) RunOutcome { called = true; return RunOutcome{} }, path)
			require.Error(t, err)
			assert.False(t, called, "a refused call must not run anything")
		})
	}

	t.Run("inside .git", func(t *testing.T) {
		_, err := prove(t, repoRoot, func(context.Context) RunOutcome { return RunOutcome{} }, ".git/config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".git")
	})

	assert.Equal(t, fixedCalc, readFile(t, repoRoot, "calc.go"))
	assert.Equal(t, fixedCalc, readFile(t, other, "calc.go"))
}

func TestProve_SerializesConcurrentCallsOnTheSameFile(t *testing.T) {
	dir := gitRepo(t)
	var active, maxActive atomic.Int32
	run := func(context.Context) RunOutcome {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		content := readFile(t, dir, "calc.go")
		active.Add(-1)
		if strings.Contains(content, "a - b") {
			return RunOutcome{ExitCode: 1}
		}
		return RunOutcome{}
	}

	const callers = 4
	reports := make([]*Report, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = prove(t, dir, run, "calc.go")
		}()
	}
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		assert.Equal(t, VerdictProven, reports[i].Verdict, "call %d: a proof that overlapped another would snapshot its baseline", i)
	}
	assert.Equal(t, int32(1), maxActive.Load(), "proofs of the same file must not overlap")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

// orphanSwap leaves calc.go swapped to its baseline with a journal entry and
// no restore — exactly what a process killed mid-run leaves behind.
func orphanSwap(t *testing.T, dir, rel string) {
	t.Helper()
	ctx := context.Background()
	files, err := resolveFiles(dir, "", []string{rel})
	require.NoError(t, err)
	r, files, err := openRepo(ctx, files)
	require.NoError(t, err)
	commit, err := r.resolveCommit(ctx, "HEAD")
	require.NoError(t, err)
	files[0].baseline, err = r.baselineState(ctx, commit, files[0].rel, "HEAD")
	require.NoError(t, err)
	held, err := r.lockFiles(ctx, files, time.Second)
	require.NoError(t, err)
	defer held.release() // the OS drops a dead process's locks; this stands in for that

	original, err := readState(files[0].abs)
	require.NoError(t, err)
	p := &proof{repo: r, report: &Report{}, runID: "crashed", commit: commit}
	did, err := p.swap(&target{file: files[0], original: original})
	require.NoError(t, err)
	require.True(t, did)
}

func TestProve_RecoversASwapLeftByACrashedRun(t *testing.T) {
	dir := gitRepo(t)
	orphanSwap(t, dir, "calc.go")
	require.Equal(t, buggyCalc, readFile(t, dir, "calc.go"), "precondition: the crash left the baseline on disk")

	runner := &observingRunner{dir: dir}
	report, err := prove(t, dir, runner.run, "calc.go")
	require.NoError(t, err)

	require.Len(t, report.Recovered, 1)
	assert.Equal(t, RestoreOK, report.Recovered[0].Outcome)
	assert.Contains(t, report.Recovered[0].Detail, "interrupted")
	assert.Equal(t, VerdictProven, report.Verdict, "the proof must snapshot the recovered fix, not the leftover baseline")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
	assert.Empty(t, activeJournalEntries(t, dir))
}

// Recovery is not limited to the files a later call names: any orphan whose
// lock is free is put back.
func TestProve_RecoversOrphansOfOtherFiles(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "other.go", "package calc\n\nconst Version = 1\n")
	git(t, dir, "add", "other.go")
	git(t, dir, "commit", "-qm", "add other")
	writeFile(t, dir, "other.go", "package calc\n\nconst Version = 2\n")
	orphanSwap(t, dir, "other.go")
	require.Contains(t, readFile(t, dir, "other.go"), "Version = 1")

	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	assert.Equal(t, "other.go", report.Recovered[0].Path)
	assert.Contains(t, readFile(t, dir, "other.go"), "Version = 2")
}

// A crashed run that had put back a file the fix deleted — inside directories
// it created — leaves both behind; recovery removes them again.
func TestProve_RecoveryRemovesWhatACrashedRunRecreated(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "legacy/override/override.go", "package override\n")
	git(t, dir, "add", "legacy")
	git(t, dir, "commit", "-qm", "add override")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "legacy")))

	orphanSwap(t, dir, "legacy/override/override.go")
	require.FileExists(t, filepath.Join(dir, "legacy/override/override.go"), "precondition: the crash left the recreated file")

	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	assert.Equal(t, RestoreOK, report.Recovered[0].Outcome)
	assert.NoDirExists(t, filepath.Join(dir, "legacy"))
}

// A call that is refused after recovery ran must still say what it
// recovered: the agent has to learn its file was put back.
func TestProve_ReportsRecoveryEvenWhenTheCallFails(t *testing.T) {
	dir := gitRepo(t)
	orphanSwap(t, dir, "calc.go")

	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go", "missing.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.go does not exist")
	require.NotNil(t, report)
	require.Len(t, report.Recovered, 1)
	assert.Equal(t, RestoreOK, report.Recovered[0].Outcome)
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_RecoveryNeverClobbersALaterEdit(t *testing.T) {
	dir := gitRepo(t)
	orphanSwap(t, dir, "calc.go")
	const edited = "package calc\n\n// edited after the crash\nfunc Add(a, b int) int { return a + b }\n"
	writeFile(t, dir, "calc.go", edited)

	report, err := prove(t, dir, func(context.Context) RunOutcome { return RunOutcome{ExitCode: 1} }, "calc.go")
	require.NoError(t, err)
	require.Len(t, report.Recovered, 1)
	assert.Equal(t, RestoreConflict, report.Recovered[0].Outcome)
	preserved, readErr := os.ReadFile(report.Recovered[0].Preserved)
	require.NoError(t, readErr)
	assert.Equal(t, fixedCalc, string(preserved))
	assert.Equal(t, edited, readFile(t, dir, "calc.go"))
}

func TestProve_FileAddedByTheFixIsRemovedForTheBeforeRun(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "internal/helper/helper.go", "package helper\n")
	var existedBefore, existedAfter bool
	calls := 0
	run := func(context.Context) RunOutcome {
		_, err := os.Stat(filepath.Join(dir, "internal/helper/helper.go"))
		calls++
		if calls == 1 {
			existedBefore = err == nil
			return RunOutcome{ExitCode: 1}
		}
		existedAfter = err == nil
		return RunOutcome{}
	}

	report, err := prove(t, dir, run, "internal/helper/helper.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictProven, report.Verdict)
	assert.Equal(t, []FileChange{{Path: "internal/helper/helper.go", Change: ChangeRemoved}}, report.Files)
	assert.False(t, existedBefore)
	assert.True(t, existedAfter)
	assert.Equal(t, "package helper\n", readFile(t, dir, "internal/helper/helper.go"))
}

func TestProve_FileDeletedByTheFixIsRecreatedForTheBeforeRun(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "legacy/override/override.go", "package override\n")
	commitAll(t, dir, "add override")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "legacy")))

	var sawIt bool
	calls := 0
	run := func(context.Context) RunOutcome {
		calls++
		if calls == 1 {
			_, err := os.Stat(filepath.Join(dir, "legacy/override/override.go"))
			sawIt = err == nil
			return RunOutcome{ExitCode: 1}
		}
		return RunOutcome{}
	}

	report, err := prove(t, dir, run, "legacy/override/override.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictProven, report.Verdict)
	assert.Equal(t, ChangeRecreated, report.Files[0].Change)
	assert.True(t, sawIt)
	_, statErr := os.Stat(filepath.Join(dir, "legacy"))
	assert.True(t, os.IsNotExist(statErr), "directories created for the before-run are removed again")
}

func TestProve_ExplicitBaselineRevision(t *testing.T) {
	dir := gitRepo(t)
	commitAll(t, dir, "fix add") // HEAD now holds the fix; HEAD~1 the bug
	writeFile(t, dir, "calc.go", fixedCalc+"\n// tidy\n")

	runner := &observingRunner{dir: dir}
	report, err := Prove(context.Background(), Options{Workspace: dir, Files: []string{"calc.go"}, Baseline: "HEAD~1", Run: runner.run})
	require.NoError(t, err)
	assert.Equal(t, VerdictProven, report.Verdict)
	assert.Equal(t, buggyCalc, runner.seen[0])
	assert.Equal(t, fixedCalc+"\n// tidy\n", readFile(t, dir, "calc.go"))
}

func TestProve_PreservesTheFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX modes")
	}
	dir := gitRepo(t)
	path := filepath.Join(dir, "calc.go")
	require.NoError(t, os.Chmod(path, 0o750))

	_, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
}

func TestProve_SkipsFilesUnchangedFromBaseline(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "doc.go", "package calc\n")
	writeFile(t, dir, "calc.go", buggyCalc)
	commitAll(t, dir, "add doc")
	writeFile(t, dir, "calc.go", fixedCalc)

	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go", "doc.go")
	require.NoError(t, err)
	assert.Equal(t, []string{"doc.go"}, report.Skipped)
	assert.Equal(t, VerdictProven, report.Verdict)

	_, err = prove(t, dir, (&observingRunner{dir: dir}).run, "doc.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identical")
}

// On a case-insensitive filesystem a miscased path opens the real file, but
// git would find no such path at baseline and the proof would delete the file
// for the before-run. The path must be read back in the stored case.
func TestProve_MiscasedPathOnACaseInsensitiveFilesystem(t *testing.T) {
	dir := gitRepo(t)
	if _, err := os.Stat(filepath.Join(dir, "CALC.GO")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	runner := &observingRunner{dir: dir}

	// Both spellings in one call name ONE file: deduplicated, not locked twice.
	report, err := prove(t, dir, runner.run, "CALC.GO", "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictProven, report.Verdict)
	assert.Equal(t, []FileChange{{Path: "calc.go", Change: ChangeReverted}}, report.Files)
	assert.Equal(t, buggyCalc, runner.seen[0], "the before-run must see the baseline, not a deleted file")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, "calc.go", "the file keeps its stored name")
	assert.NotContains(t, names, "CALC.GO")
}

func TestProve_WarnsWhenTheTestFileIsNamed(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "calc_test.go", "package calc\n")
	report, err := prove(t, dir, (&observingRunner{dir: dir}).run, "calc.go", "calc_test.go")
	require.NoError(t, err)
	require.Len(t, report.Warnings, 1)
	assert.Contains(t, report.Warnings[0], "calc_test.go looks like a test file")
}

func TestProve_RefusesBadRequests(t *testing.T) {
	dir := gitRepo(t)
	noop := func(context.Context) RunOutcome { return RunOutcome{} }

	_, err := Prove(context.Background(), Options{Workspace: dir, Files: []string{"calc.go"}, Baseline: "no-such-rev", Run: noop})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a commit")

	_, err = prove(t, dir, noop, "missing.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")

	_, err = prove(t, dir, noop)
	require.Error(t, err)

	plain := t.TempDir()
	writeFile(t, plain, "x.go", "package x\n")
	_, err = prove(t, plain, noop, "x.go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not inside a git repository")
}

// End to end with a real `go test`: the failure the before-run reports is the
// test's own assertion, not a stand-in.
func TestProve_RealGoTest(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not installed")
	}
	dir := gitRepo(t)
	writeFile(t, dir, "go.mod", "module example.com/calc\n\ngo 1.21\n")
	git(t, dir, "add", "go.mod")
	git(t, dir, "commit", "-qm", "module")
	writeFile(t, dir, "calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(1, 2); got != 3 {\n\t\tt.Fatalf(\"Add(1, 2) = %d, want 3\", got)\n\t}\n}\n")

	report, err := prove(t, dir, goTestRunner(goBin, dir), "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictProven, report.Verdict, "before:\n%s\nafter:\n%s", report.Before.Output, report.After.Output)
	assert.Contains(t, report.Before.Output, "Add(1, 2) = -1, want 3")
	assert.Equal(t, fixedCalc, readFile(t, dir, "calc.go"))
}

func TestProve_RealGoTestBaselineDoesNotCompile(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not installed")
	}
	dir := gitRepo(t)
	writeFile(t, dir, "go.mod", "module example.com/calc\n\ngo 1.21\n")
	git(t, dir, "add", "go.mod")
	git(t, dir, "commit", "-qm", "module")
	// The fix ADDS Mul, and the test calls it: against the baseline the test
	// cannot even compile, which says nothing about whether it catches a bug.
	writeFile(t, dir, "calc.go", fixedCalc+"\nfunc Mul(a, b int) int { return a * b }\n")
	writeFile(t, dir, "calc_test.go", "package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n")

	report, err := prove(t, dir, goTestRunner(goBin, dir), "calc.go")
	require.NoError(t, err)
	assert.Equal(t, VerdictBaselineDoesNotCompile, report.Verdict, "before:\n%s", report.Before.Output)
	require.NotNil(t, report.CompileFailure)
}

func goTestRunner(goBin, dir string) Runner {
	return func(ctx context.Context) RunOutcome {
		cmd := exec.CommandContext(ctx, goBin, "test", "-count=1", "./...")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		start := time.Now()
		out, err := cmd.CombinedOutput()
		outcome := RunOutcome{Output: string(out), Duration: time.Since(start)}
		if exitErr, ok := err.(*exec.ExitError); ok {
			outcome.ExitCode = exitErr.ExitCode()
		} else if err != nil {
			outcome.Err = err
		}
		return outcome
	}
}
