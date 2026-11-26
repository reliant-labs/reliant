// Copyright (c) 2025 Reliant Labs
package practions

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/pipeline/config"
)

// runCheck runs CheckScript(cfg) in a fresh git repository holding files,
// and returns what a run node would record.
func runCheck(t *testing.T, cfg config.Resolved, files map[string]string) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", CheckScript(cfg, "main"))
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		exit = ee.ExitCode()
	}
	return map[string]any{"exit_code": float64(exit), "stdout": stdout.String(), "stderr": stderr.String()}
}

func TestCheckScriptGoPackagePathsAreLiteralArguments(t *testing.T) {
	dir := t.TempDir()
	pkg := "pkg; touch injected; #"
	if err := os.Mkdir(filepath.Join(dir, pkg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkg, "x.go"), []byte("package example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Feed the changed-path reader directly so git's display quoting cannot
	// mask shell metacharacters. The fake go also verifies argument boundaries.
	script := checkPrelude + "printf '%s\\n' " + shellQuote(pkg+"/x.go") + ` >"$t/changed"
go() {
  [ "$#" = 3 ] && [ "$1" = test ] && [ "$2" = -short ] && [ "$3" = ` + shellQuote("./"+pkg) + ` ]
}
` + goPackages + `run_stage test run_go_tests
exit "$failed"
`
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if _, statErr := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(statErr) {
		t.Fatalf("changed path executed shell syntax: %v", statErr)
	}
	if err != nil {
		t.Fatalf("package path was not passed as one literal argument: %v\n%s", err, out)
	}
}

func TestCheckScriptReportsAFailingCheckWithItsOutput(t *testing.T) {
	cfg := config.Defaults()
	cfg.Check = &config.Check{Run: `printf 'a "quoted"\tline\\ with\nboom\n'; exit 3`}
	out := runCheck(t, cfg, map[string]string{"x.txt": "x"})
	stages, failures, passed := parseCheck(out)
	if passed || len(stages) != 1 || stages[0].Name != "check" || stages[0].Verdict != "fail" {
		t.Fatalf("want one failed check stage, got %+v (stdout %q)", stages, out["stdout"])
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Detail, `a "quoted" line\ with`) || !strings.Contains(failures[0].Detail, "boom") {
		t.Fatalf("the failure must carry the stage's output, got %+v", failures)
	}
	if out["exit_code"] != float64(1) {
		t.Fatalf("a failed stage exits 1, got %v", out["exit_code"])
	}
}

func TestCheckScriptStagesStopAfterAFailureAndOffIsNeverPassed(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gate.Stages = []config.ResolvedStage{
		{Name: config.StageLint, State: config.StageOn, Kind: config.StageCommand, Command: "echo lint-broke; false"},
		{Name: config.StageTest, State: config.StageOn, Kind: config.StageCommand, Command: "the full suite"},
		{Name: config.StageBuild, State: config.StageOn, Kind: config.StageCommand, Command: "echo built"},
	}
	stages, failures, passed := parseCheck(runCheck(t, cfg, nil))
	if passed || len(stages) != 3 {
		t.Fatalf("want three stages and a failure, got %+v", stages)
	}
	want := []string{"fail", "off", "off"} // the build never runs after lint failed
	for i, s := range stages {
		if s.Verdict != want[i] {
			t.Fatalf("stage %s: want %s, got %s", s.Name, want[i], s.Verdict)
		}
	}
	if len(failures) != 1 || failures[0].Check != "lint" || !strings.Contains(failures[0].Detail, "lint-broke") {
		t.Fatalf("failures: %+v", failures)
	}

	// Nothing to run passes with every stage off, and the gate's full test
	// suite is never the fixer's check.
	cfg.Gate.Stages[0].Command = "true"
	stages, _, passed = parseCheck(runCheck(t, cfg, nil))
	if !passed || stages[1].Verdict != "off" || stages[2].Verdict != "pass" {
		t.Fatalf("want lint pass, test off, build pass; got %+v", stages)
	}
}
