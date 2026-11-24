package testflakes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// fakeGit answers the three git reads FindChangedTests makes.
func fakeGit(root, names string, diffs map[string]string) Git {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		switch {
		case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return root, nil
		case len(args) >= 2 && args[0] == "diff" && args[1] == "--name-only":
			return names, nil
		case len(args) >= 2 && args[0] == "diff" && args[1] == "-U0":
			rel, _ := filepath.Rel(root, args[len(args)-1])
			return diffs[filepath.ToSlash(rel)], nil
		}
		return "", errors.New("unexpected git " + strings.Join(args, " "))
	}
}

func listOf(root string, dirs ...string) ListPackages {
	return func(context.Context, string) ([]string, error) {
		var out []string
		for _, d := range dirs {
			out = append(out, filepath.Join(root, filepath.FromSlash(d)))
		}
		return out, nil
	}
}

// Ported from ci-changed-tests.sh: +func lines and hunk headers name tests;
// tagged files, files outside the module's packages and deleted files are
// skipped; a helper edit names nothing.
func TestFindChangedTests(t *testing.T) {
	root := newModule(t, map[string]string{
		"internal/a/a_test.go":          "package a\n",
		"internal/a/more_test.go":       "package a\n",
		"internal/b/b_test.go":          "package b\n",
		"internal/e2e/e2e_test.go":      "//go:build e2e\n\npackage e2e\n",
		"internal/a/testdata/x_test.go": "package x\n",
		"root_test.go":                  "package m\n",
	})
	names := strings.Join([]string{
		"internal/a/a_test.go", "internal/a/more_test.go", "internal/b/b_test.go",
		"internal/e2e/e2e_test.go", "internal/a/testdata/x_test.go", "internal/gone/gone_test.go", "root_test.go",
	}, "\n")
	diffs := map[string]string{
		"internal/a/a_test.go":    "@@ -1,0 +2,3 @@\n+func TestNew(t *testing.T) {\n+}\n@@ -10 +13 @@ func TestEdited(t *testing.T) {\n-\told\n+\tnew\n",
		"internal/a/more_test.go": "@@ -3 +3 @@ func helper() {\n-x\n+y\n@@ -9 +9 @@ func FuzzParse(f *testing.F) {\n",
		"internal/b/b_test.go":    "@@ -1 +1 @@ func TestNew(t *testing.T) {\n",
		"root_test.go":            "+func TestRoot(t *testing.T) {\n",
	}
	got, err := FindChangedTests(context.Background(), ChangedOptions{
		Root: root, Base: "origin/main", Git: fakeGit(root, names, diffs),
		List: listOf(root, ".", "internal/a", "internal/b", "internal/e2e"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []string
	for _, p := range got.Packages {
		pkgs = append(pkgs, p.Rel+"="+strings.Join(p.Names, ","))
	}
	if want := ".=TestRoot internal/a=FuzzParse,TestEdited,TestNew internal/b=TestNew"; strings.Join(pkgs, " ") != want {
		t.Errorf("packages = %v, want %s", pkgs, want)
	}
	if re := got.RunRegex(); re != "^(FuzzParse|TestEdited|TestNew|TestRoot)$" {
		t.Errorf("run regex = %s", re)
	}
	skipped := strings.Join(got.Skipped, "\n")
	for _, want := range []string{"skip (tagged): internal/e2e/e2e_test.go", "skip (not a package of this module): internal/a/testdata/x_test.go"} {
		if !strings.Contains(skipped, want) {
			t.Errorf("skipped lacks %q:\n%s", want, skipped)
		}
	}
}

// Each changed line range is mapped onto the Test/Fuzz declaration it falls
// in, from the parsed file: an edit inside a subtest whose hunk header names
// something else (here a raw string's unindented line), and a pure deletion
// inside a test body, still select that test. A helper edit selects nothing,
// and an edit to TestMain selects every test of the package.
func TestFindChangedTestsMapsLinesOntoDeclarations(t *testing.T) {
	a := "package a\n\nimport \"testing\"\n\n" + // 1-4
		"func helper() int { return 1 }\n\n" + // 5-6
		"func TestOuter(t *testing.T) {\n" + // 7
		"\tconst golden = `\n" + // 8
		"unindented: yes\n" + // 9
		"`\n" + // 10
		"\tt.Run(\"sub\", func(t *testing.T) {\n" + // 11
		"\t\tif helper() != 1 {\n" + // 12
		"\t\t\tt.Fatal(golden)\n" + // 13
		"\t\t}\n" + // 14
		"\t})\n" + // 15
		"}\n\n" + // 16-17
		"func TestOther(t *testing.T) {\n" + // 18
		"\t_ = helper()\n" + // 19
		"}\n" // 20
	b := "package b\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n" + // 1-7
		"func TestMain(m *testing.M) {\n" + // 8
		"\tos.Exit(m.Run())\n" + // 9
		"}\n\n" + // 10-11
		"func TestB1(t *testing.T) {}\n\n" + // 12-13
		"func FuzzB2(f *testing.F) {}\n" // 14
	c := "package c\n\nimport \"testing\"\n\nfunc helper() {}\n\nfunc TestC(t *testing.T) {}\n"
	root := newModule(t, map[string]string{"internal/a/a_test.go": a, "internal/b/b_test.go": b, "internal/c/c_test.go": c})
	diffs := map[string]string{
		// line 13, in TestOuter's subtest; git labels it with line 9.
		"internal/a/a_test.go": "@@ -13 +13 @@ unindented: yes\n-\t\t\tt.Fatal(\"x\")\n+\t\t\tt.Fatal(golden)\n" +
			// a deletion after line 19, inside TestOther.
			"@@ -21 +19,0 @@ func TestOther(t *testing.T) {\n-\t_ = 2\n",
		"internal/b/b_test.go": "@@ -9 +9 @@ func TestMain(m *testing.M) {\n-\tm.Run()\n+\tos.Exit(m.Run())\n",
		"internal/c/c_test.go": "@@ -5 +5 @@ import \"testing\"\n-func helper() { _ = 1 }\n+func helper() {}\n",
	}
	got, err := FindChangedTests(context.Background(), ChangedOptions{
		Root: root, Base: "origin/main",
		Git:  fakeGit(root, "internal/a/a_test.go\ninternal/b/b_test.go\ninternal/c/c_test.go", diffs),
		List: listOf(root, "internal/a", "internal/b", "internal/c"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var pkgs []string
	for _, p := range got.Packages {
		pkgs = append(pkgs, p.Rel+"="+strings.Join(p.Names, ","))
	}
	if want := "internal/a=TestOther,TestOuter internal/b=FuzzB2,TestB1"; strings.Join(pkgs, " ") != want {
		t.Errorf("packages = %v, want %s", pkgs, want)
	}
}

func gateOpts(t *testing.T, root string, r *fakeRunner, git Git, list ListPackages) (GateOptions, *bytes.Buffer) {
	out := &bytes.Buffer{}
	clock := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	return GateOptions{
		Root: root, Base: "base", Stdout: out, Stderr: out, Environ: []string{"PATH=/bin"}, Getenv: noEnv,
		Now: func() time.Time { return clock }, Git: git, List: list, Runner: r.run,
		Begin: begin(root, "c1", "t1", clock),
	}, out
}

func TestGateRunsChangedTestsThreeTimesAndRecordsTheRun(t *testing.T) {
	root := newModule(t, map[string]string{"internal/a/a_test.go": "package a\n\nfunc TestNew(t *testing.T) {}\n"})
	git := fakeGit(root, "internal/a/a_test.go", map[string]string{"internal/a/a_test.go": "+func TestNew(t *testing.T) {\n"})
	pkg := testModule + "/internal/a"
	r := &fakeRunner{script: func([]string) (string, int) {
		// Failed the second of three runs: a flake.
		return passRun(pkg, "TestNew") + failRun(pkg, "TestNew", "    a_test.go:9: boom\n") + passRun(pkg, "TestNew") + ev("fail", pkg, "", ""), 1
	}}
	o, out := gateOpts(t, root, r, git, listOf(root, "internal/a"))
	o.Command = []string{"scripts/ci-go-test.sh"}
	res := Gate(context.Background(), o)
	if res.ExitCode != GateFailed {
		t.Fatalf("exit = %d (%s)\n%s", res.ExitCode, res.Message, out)
	}
	want := "scripts/ci-go-test.sh -json -short -count=3 -timeout 10m -run ^(TestNew)$ ./internal/a"
	if got := strings.Join(r.calls[0], " "); got != want {
		t.Errorf("argv = %s\nwant   %s", got, want)
	}
	if !strings.Contains(res.Message, "reproduce with: scripts/ci-go-test.sh -short -count=3 -timeout 10m -run '^(TestNew)$' ./internal/a") {
		t.Errorf("message = %s", res.Message)
	}
	if len(res.RetryPassed) != 1 || res.RetryPassed[0].Name != "TestNew" {
		t.Errorf("retry passed = %v", res.RetryPassed)
	}
	// The run is recorded as lane flake-gate, and the child saw the run.
	if res.LaneDir == "" || filepath.Base(res.LaneDir) != LaneFlakeGate {
		t.Fatalf("lane dir = %q", res.LaneDir)
	}
	m, err := testreport.ReadLaneManifest(filepath.Dir(res.LaneDir), LaneFlakeGate)
	if err != nil || m.ExitCode != 1 || m.Results != testreport.LaneResultsParsed {
		t.Fatalf("lane manifest = %+v, %v", m, err)
	}
	if !strings.Contains(strings.Join(r.envs[0], "\n"), testcapture.EnvLane+"="+LaneFlakeGate) {
		t.Error("the command must see FORGE_TEST_LANE")
	}
	// The record shows a retry-pass flake to `forge test flakes`.
	doc, _ := ComputeFlakes(FlakesOptions{Root: root, Now: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)})
	if len(doc.Tests) != 1 || doc.Tests[0].State != testreport.StateSuspected || doc.Tests[0].Events[0].Signal != testreport.SignalRetryPass {
		t.Errorf("flakes = %+v", doc.Tests)
	}
}

func TestGateFlagsFullAndCPU(t *testing.T) {
	if got := strings.Join(GateFlags(true, ""), " "); got != "-short -count=3 -timeout 10m -race" {
		t.Errorf("full = %s", got)
	}
	if got := strings.Join(GateFlags(false, "1,2,4"), " "); got != "-short -cpu=1,2,4 -timeout 10m" {
		t.Errorf("cpu = %s", got)
	}
}

func TestGateExitCodes(t *testing.T) {
	root := newModule(t, map[string]string{"internal/a/a_test.go": "package a\n"})
	pkg := testModule + "/internal/a"

	// No changed test: 2, and nothing runs.
	r := &fakeRunner{script: func([]string) (string, int) { return "", 0 }}
	o, _ := gateOpts(t, root, r, fakeGit(root, "", nil), listOf(root, "internal/a"))
	if res := Gate(context.Background(), o); res.ExitCode != GateUndetermined || len(r.calls) != 0 || !strings.Contains(res.Message, "no new or edited tests") {
		t.Errorf("no tests: %+v", res)
	}

	// An unreadable diff: 2.
	bad := func(context.Context, string, ...string) (string, error) {
		return "", errors.New("not a git repository")
	}
	o, _ = gateOpts(t, root, r, bad, listOf(root))
	if res := Gate(context.Background(), o); res.ExitCode != GateUndetermined {
		t.Errorf("bad git: %+v", res)
	}

	// Every run passed: 0.
	git := fakeGit(root, "internal/a/a_test.go", map[string]string{"internal/a/a_test.go": "+func TestNew(t *testing.T) {\n"})
	r = &fakeRunner{script: func([]string) (string, int) {
		return passRun(pkg, "TestNew") + passRun(pkg, "TestNew") + passRun(pkg, "TestNew") + ev("pass", pkg, "", ""), 0
	}}
	o, _ = gateOpts(t, root, r, git, listOf(root, "internal/a"))
	o.Full = true
	res := Gate(context.Background(), o)
	if res.ExitCode != GatePassed || !strings.Contains(res.Message, "passed 3/3") || !hasArg(r.calls[0], "-race") {
		t.Errorf("pass: %+v %v", res, r.calls)
	}
	if r.calls[0][0] != "go" || r.calls[0][1] != "test" {
		t.Errorf("default command = %v", r.calls[0])
	}

	// The selected tests' command could not start: 2, as before, but marked
	// so the flake-check step fails it; "no tests" above is not marked.
	o, _ = gateOpts(t, root, r, git, listOf(root, "internal/a"))
	o.Runner = func(context.Context, string, []string, []string, io.Writer, io.Writer) (int, error) {
		return 127, exec.ErrNotFound
	}
	if res := Gate(context.Background(), o); res.ExitCode != GateUndetermined || !res.CommandFailed || !strings.Contains(res.Message, "could not run go") {
		t.Errorf("command could not start: %+v", res)
	}
	o, _ = gateOpts(t, root, r, fakeGit(root, "", nil), listOf(root, "internal/a"))
	if res := Gate(context.Background(), o); res.CommandFailed {
		t.Errorf("no tests is not a failed command: %+v", res)
	}
}

// FORGE_TESTS=off: the gate still runs and judges, and records nothing.
func TestGateRecordsNothingWhenTestsOff(t *testing.T) {
	root := newModule(t, map[string]string{"internal/a/a_test.go": "package a\n"})
	pkg := testModule + "/internal/a"
	git := fakeGit(root, "internal/a/a_test.go", map[string]string{"internal/a/a_test.go": "+func TestNew(t *testing.T) {\n"})
	r := &fakeRunner{script: func([]string) (string, int) { return passRun(pkg, "TestNew") + ev("pass", pkg, "", ""), 0 }}
	o, _ := gateOpts(t, root, r, git, listOf(root, "internal/a"))
	off := func(k string) string {
		if k == testcapture.EnvTests {
			return "off"
		}
		return ""
	}
	o.Getenv, o.Begin.Getenv = off, off
	if res := Gate(context.Background(), o); res.ExitCode != GatePassed || res.LaneDir != "" {
		t.Errorf("%+v", res)
	}
	if _, err := os.Stat(testreport.RunsRoot(root)); !os.IsNotExist(err) {
		t.Errorf("runs dir exists: %v", err)
	}
}
