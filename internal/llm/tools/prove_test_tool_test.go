// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	proveBuggyCalc = "package calc\n\nfunc Add(a, b int) int { return a - b }\n"
	proveFixedCalc = "package calc\n\nfunc Add(a, b int) int { return a + b }\n"
	proveCalcTest  = "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(1, 2); got != 3 {\n\t\tt.Fatalf(\"Add(1, 2) = %d, want 3\", got)\n\t}\n}\n"
)

// proveTestRepo is a Go module whose HEAD holds a bug and whose working tree
// holds the fix plus a new, uncommitted regression test.
func proveTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
			"-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	gitCmd("init", "-q")
	write("go.mod", "module example.com/calc\n\ngo 1.21\n")
	write("calc.go", proveBuggyCalc)
	gitCmd("add", "-A")
	gitCmd("commit", "-qm", "buggy add")
	write("calc.go", proveFixedCalc)
	write("calc_test.go", proveCalcTest)
	return dir
}

func proveTestCtx(dir string) *rctx.ToolContext {
	return rctx.NewToolContext(context.Background(), "test-chat", "0", nil, &rctx.WorktreeInfo{Path: dir}).
		WithDaemon(daemon.NewLocalClient())
}

// logProveOutputOnFailure attaches the tool's report to a failing test only.
// The report quotes the sample module's own go test output, so a passing
// run's report put "--- FAIL: TestAdd" and "FAIL example.com/calc [build
// failed]" into CI's -v log, where they read as failures of this suite.
func logProveOutputOnFailure(t *testing.T, content string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("prove_test output:\n%s", content)
		}
	})
}

func requireGo(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("compiles and runs a real `go test` twice; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
}

// End to end: the real daemon executor runs a real `go test` against the
// reverted file and then the fixed one.
func TestProveTest_ProvesARealGoTest(t *testing.T) {
	requireGo(t)
	dir := proveTestRepo(t)

	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: "GOWORK=off GOFLAGS= go test -count=1 -run TestAdd ./...",
		Files:   []string{"calc.go"},
	})
	require.NoError(t, err)
	logProveOutputOnFailure(t, resp.Content)

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "VERDICT: proven")
	assert.Contains(t, resp.Content, "BEFORE (fix reverted) — exit 1")
	assert.Contains(t, resp.Content, "Add(1, 2) = -1, want 3")
	assert.Contains(t, resp.Content, "AFTER (fix in place) — exit 0")
	assert.Contains(t, resp.Content, "calc.go — reverted to baseline for the before-run; restored byte for byte")
	assert.Contains(t, resp.Metadata, `"verdict":"proven"`)

	got, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	require.NoError(t, err)
	assert.Equal(t, proveFixedCalc, string(got))
}

func TestProveTest_ReportsATestThatDoesNotTestTheFix(t *testing.T) {
	requireGo(t)
	dir := proveTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "calc_test.go"),
		[]byte("package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { _ = Add(1, 2) }\n"), 0o644))

	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: "GOWORK=off GOFLAGS= go test -count=1 ./...",
		Files:   []string{"calc.go"},
	})
	require.NoError(t, err)
	logProveOutputOnFailure(t, resp.Content)
	assert.Contains(t, resp.Content, "VERDICT: passes_without_fix")
	assert.Contains(t, resp.Content, "does not test your change")
}

func TestProveTest_ReportsABaselineThatDoesNotCompile(t *testing.T) {
	requireGo(t)
	dir := proveTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "calc.go"),
		[]byte(proveFixedCalc+"\nfunc Mul(a, b int) int { return a * b }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "calc_test.go"),
		[]byte("package calc\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal(\"Mul\")\n\t}\n}\n"), 0o644))

	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: "GOWORK=off GOFLAGS= go test -count=1 ./...",
		Files:   []string{"calc.go"},
	})
	require.NoError(t, err)
	logProveOutputOnFailure(t, resp.Content)
	assert.Contains(t, resp.Content, "VERDICT: baseline_does_not_compile")
	assert.Contains(t, resp.Content, "Build failure marker")
	assert.Contains(t, resp.Content, "undefined: Mul")
}

// Another writer changes the file while the fix is reverted. Their bytes
// stay; the fix is preserved at the path the result names.
func TestProveTest_KeepsAConcurrentWritersContent(t *testing.T) {
	dir := proveTestRepo(t)
	const theirs = "package calc\n// another agent's edit\n"

	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: `if grep -q 'a - b' calc.go; then printf 'package calc\n// another agent'"'"'s edit\n' > calc.go; exit 1; fi; exit 0`,
		Files:   []string{"calc.go"},
	})
	require.NoError(t, err)
	logProveOutputOnFailure(t, resp.Content)

	assert.Contains(t, resp.Content, "VERDICT: conflict")
	assert.Contains(t, resp.Content, "AFTER (fix in place) — skipped")
	got, readErr := os.ReadFile(filepath.Join(dir, "calc.go"))
	require.NoError(t, readErr)
	assert.Equal(t, theirs, string(got), "their write must not be overwritten")

	_, preservedPath, found := strings.Cut(resp.Content, "your version is preserved at ")
	require.True(t, found)
	preservedPath, _, _ = strings.Cut(preservedPath, "\n")
	preserved, readErr := os.ReadFile(strings.TrimSpace(preservedPath))
	require.NoError(t, readErr)
	assert.Equal(t, proveFixedCalc, string(preserved))
}

// The daemon's own timeout fires on the before-run; the fix must be back on
// disk when the tool returns.
func TestProveTest_RestoresWhenTheCommandTimesOut(t *testing.T) {
	dir := proveTestRepo(t)

	start := time.Now()
	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: "sleep 30",
		Files:   []string{"calc.go"},
		Timeout: 300,
	})
	require.NoError(t, err)
	logProveOutputOnFailure(t, resp.Content)

	assert.Less(t, time.Since(start), 25*time.Second)
	assert.Contains(t, resp.Content, "VERDICT: inconclusive")
	assert.Contains(t, resp.Content, "TIMED OUT")
	got, readErr := os.ReadFile(filepath.Join(dir, "calc.go"))
	require.NoError(t, readErr)
	assert.Equal(t, proveFixedCalc, string(got))
}

func TestProveTest_RefusesAPathOutsideTheWorkspace(t *testing.T) {
	dir := proveTestRepo(t)
	resp, err := (&proveTestTool{}).Execute(proveTestCtx(dir), ProveTestParams{
		Command: "true",
		Files:   []string{"../escape.go"},
	})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "outside the workspace")
}

func TestProveTest_RequiresCommandAndFiles(t *testing.T) {
	dir := proveTestRepo(t)
	tool := &proveTestTool{}

	resp, err := tool.Execute(proveTestCtx(dir), ProveTestParams{Files: []string{"calc.go"}})
	require.NoError(t, err)
	assert.True(t, resp.IsError)

	resp, err = tool.Execute(proveTestCtx(dir), ProveTestParams{Command: "true"})
	require.NoError(t, err)
	assert.True(t, resp.IsError)

	needs, err := tool.RequiresPermission(ProveTestParams{Command: "go test ./..."})
	require.NoError(t, err)
	assert.True(t, needs, "it runs a command and rewrites files, so it is always gated")
}

func TestProveTest_RegisteredOnTheDaemonOutsideTheDefaultBundle(t *testing.T) {
	placement, err := PlacementOf(ToolProveTest)
	require.NoError(t, err)
	assert.Equal(t, PlacementDaemon, placement, "the swap and the restore must run where the files are")

	assert.NotContains(t, ExpandToolFilter([]string{"tag:coding:default"}, nil), ToolProveTest)
	assert.Contains(t, ExpandToolFilter([]string{"tag:execution"}, nil), ToolProveTest)
	assert.NotContains(t, ExpandToolFilter([]string{"tag:coding:default", "prove_test", "!tag:execution"}, nil), ToolProveTest)

	assert.True(t, slices.ContainsFunc(GetToolRegistry(), func(d ToolDefinition) bool { return d.Name == ToolProveTest }))
	assert.True(t, strings.Contains(proveTestDescription, "SHARED CHECKOUTS"), "the concurrency caveat is part of the contract")
}
