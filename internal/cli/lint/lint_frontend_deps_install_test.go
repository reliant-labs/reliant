package lint

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// TestMain keeps the pre-existing "deps missing" tests hermetic: the lanes now
// install missing node_modules, and those tests assert the not-installed
// classification, not a real `npm ci`. Tests of the install itself clear it.
func TestMain(m *testing.M) {
	os.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	// Nor does an in-process `forge lint` take or wait for one of the host's
	// heavy slots (pkg/hostslot).
	os.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "off")
	// The fix guard's type-check execs `go list`; the format and tracing
	// fixtures here are not what it is for (lint_fix_guard_test.go is).
	fixTypecheck = func(string, []string) map[string]string { return nil }
	os.Exit(m.Run())
}

// fakeNPM puts an `npm` stub on PATH that records its argv and, for install
// verbs, creates node_modules (or fails with installExit).
func fakeNPM(t *testing.T, installExit int) (calls string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	bin := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls.log")
	script := "#!/bin/sh\necho \"$*\" >> " + calls + "\n" +
		"case \"$1\" in ci|install)\n"
	if installExit == 0 {
		script += "mkdir -p node_modules\n"
	}
	script += "exit " + strconv.Itoa(installExit) + " ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func freshFrontend(t *testing.T, pkg string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"package.json": pkg, "package-lock.json": "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// A fresh worktree has no node_modules. The eslint lane must install and then
// run — not report "could not run", which reads as a green.
func TestLintFrontendDirInstallsMissingDepsThenRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	calls := fakeNPM(t, 0)
	dir := freshFrontend(t, `{"scripts":{"lint":"eslint ."}}`)

	if fs, gated := collectFrontendDirJSON(context.Background(), "web", dir, false, false); gated || hasGap(fs) {
		t.Fatalf("lane failed after a successful install: gated=%v %+v", gated, fs)
	}
	got := readLog(t, calls)
	if !strings.HasPrefix(got, "ci\n") || !strings.Contains(got, "run lint") {
		t.Fatalf("npm calls = %q, want `ci` then `run lint`", got)
	}
}

// If the install itself fails, eslint never ran: the lane could NOT run, which
// fails the run by default (lint_skip.go) and names the install command.
func TestLintFrontendDirFailsLoudlyWhenInstallFails(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	fakeNPM(t, 1)
	dir := freshFrontend(t, `{"scripts":{"lint":"eslint ."}}`)

	fs, _ := collectFrontendDirJSON(context.Background(), "web", dir, false, false)
	if len(fs) != 1 || fs[0].Rule != ruleFrontendLintUnavailable {
		t.Fatalf("install failure must be a lane that could NOT run, got %+v", fs)
	}
	if !strings.Contains(fs[0].Message, "did NOT run") || !strings.Contains(fs[0].FixHint, "npm ") {
		t.Errorf("want the reason and the install command, got %+v", fs[0])
	}
	rc := &lintRunCtx{}
	if v, _ := rc.judgeGap("frontend lint", true); v != gapFails {
		t.Errorf("a gating lane that could not run must fail the run, got verdict %v", v)
	}
}

func TestTypecheckFrontendInstallsMissingDeps(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	calls := fakeNPM(t, 0)
	dir := freshFrontend(t, `{"scripts":{"typecheck":"tsc --noEmit"}}`)

	res := typecheckFrontend(context.Background(), frontendTarget{name: "web", dir: dir}, dir)
	if res.status != typecheckPassed {
		t.Fatalf("status = %v (%s%s), want passed after install", res.status, res.reason, res.output)
	}
	if got := readLog(t, calls); !strings.HasPrefix(got, "ci\n") {
		t.Fatalf("npm calls = %q, want `ci` first", got)
	}
}

func TestTypecheckFrontendFailsWhenInstallFails(t *testing.T) {
	fakeNPM(t, 1)
	dir := freshFrontend(t, `{"scripts":{"typecheck":"tsc --noEmit"}}`)

	res := typecheckFrontend(context.Background(), frontendTarget{name: "web", dir: dir}, dir)
	// Could NOT run — which fails the gating lane (lint_skip.go) — not a
	// type error, and never green.
	if res.status != typecheckUnavailable {
		t.Fatalf("status = %v, want unavailable (the compiler never ran)", res.status)
	}
	if !strings.Contains(res.reason, "did NOT run") || res.fixHint == "" {
		t.Errorf("want the reason and the fix, got reason=%q fix=%q", res.reason, res.fixHint)
	}
}

// hasGap reports whether a lane's findings include its could-not-run headline.
func hasGap(fs []lintJSONFinding) bool {
	for _, f := range fs {
		if isGapRule(f.Rule) {
			return true
		}
	}
	return false
}
