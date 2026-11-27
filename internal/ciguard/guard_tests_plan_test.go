//go:build !windows

// scripts/ci-guard-tests.sh is bash; this test drives it with real git over a
// throwaway repository and a fake `go` (bash), so the file builds for every
// OS but Windows.

package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuardTestsPlan runs scripts/ci-guard-tests.sh over a throwaway
// repository: a Go or internal/ change runs every listed test in one go test,
// full mode (no -short, no -race), -count=1; a docs-only change runs nothing;
// a listed test that did not pass in its own package (renamed away, skipped,
// or passing only in another package) fails the script; an unresolvable base
// fails it.
//
// It runs under -short: about half a second, and cache-sound, because this
// process reads both scripts it copies (git, bash and the fake go read only
// the temp repository).
func TestGuardTestsPlan(t *testing.T) {
	for _, tool := range []string{"bash", "git", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s not on PATH", tool)
		}
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := readText(t, guardTestsScript)
	write("scripts/ci-guard-tests.sh", script)
	write("scripts/ci-go-test.sh", readText(t, filepath.Join("..", "..", "scripts", "ci-go-test.sh")))

	// The fake go: `go list -m` prints the module; `go test` logs its
	// arguments to $FAKE_GO_LOG and prints, for each package argument, a
	// `--- PASS: <name>` line for every listed test of that package (minus
	// the <pkg>.<name> entry $FAKE_DROP; $FAKE_SKIP's is reported skipped),
	// then `ok  <import path>`.
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(`#!/usr/bin/env bash
set -eu
if [[ $1 == list ]]; then echo example.com/m; exit 0; fi
[[ $1 == test ]] || exit 1
shift
echo "$*" >>"$FAKE_GO_LOG"
for arg in "$@"; do
  [[ $arg == ./* ]] || continue
  pkg=${arg#./}
  while read -r entry; do
    [[ ${entry%.*} == "$pkg" ]] || continue
    name=${entry##*.}
    [[ $entry == "${FAKE_DROP:-}" ]] && continue
    if [[ $entry == "${FAKE_SKIP:-}" ]]; then echo "--- SKIP: $name (0.00s)"; else echo "--- PASS: $name (0.01s)"; fi
  done <"$FAKE_TESTS"
  printf 'ok  \texample.com/m/%s\t0.01s\n' "$pkg"
done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	listed := bashArray(t, guardTestsScript, script, "tests")
	testsFile := filepath.Join(t.TempDir(), "tests")
	if err := os.WriteFile(testsFile, []byte(strings.Join(listed, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	goLog := filepath.Join(dir, "go.log")
	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t", "CI_BASE=", "FAKE_GO_LOG="+goLog, "FAKE_TESTS="+testsFile,
			"PATH="+fakeBin+":"+os.Getenv("PATH"))
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sh := func(args ...string) string {
		t.Helper()
		out, err := run(nil, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	commit := func(msg string) {
		t.Helper()
		sh("git", "add", "-A")
		sh("git", "commit", "-qm", msg)
	}
	goRuns := func() []string {
		t.Helper()
		raw, err := os.ReadFile(goLog)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(goLog)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}

	write("docs/d.md", "v1\n")
	write("internal/x/x.go", "package x\n")
	write("internal/templates/project/skills/a/SKILL.md", "v1\n")
	sh("git", "init", "-q", "-b", "main")
	commit("base")

	// Docs and scripts only: nothing runs, and the plan says so.
	write("docs/d.md", "v2\n")
	write("scripts/other.sh", "true\n")
	commit("docs")
	if out := sh("bash", "scripts/ci-guard-tests.sh", "HEAD^1"); !strings.Contains(out, "nothing to run") {
		t.Errorf("a docs and scripts change: want nothing to run, got:\n%s", out)
	}
	if got := goRuns(); got != nil {
		t.Errorf("a change that touches no trigger ran go %q", got)
	}

	// A skill (internal/, not Go) runs every listed test in ONE go test:
	// full mode, -count=1, an anchored -run, every package.
	write("internal/templates/project/skills/a/SKILL.md", "v2\n")
	commit("skill")
	plan := sh("bash", "scripts/ci-guard-tests.sh", "--plan", "HEAD^1")
	if !strings.Contains(plan, "internal/templates/project/skills/a/SKILL.md") || !strings.Contains(plan, "-run ^(TestNoDeadClaims|") {
		t.Errorf("--plan for a skill change does not name the file and the tests:\n%s", plan)
	}
	if got := goRuns(); got != nil {
		t.Errorf("--plan ran go %q", got)
	}
	sh("bash", "scripts/ci-guard-tests.sh", "HEAD^1")
	got := goRuns()
	if len(got) != 1 {
		t.Fatalf("a skill change ran go %d times (%q), want one go test over every package", len(got), got)
	}
	for _, want := range []string{"-count=1", "-run ^(", "./internal/deadcodeguard", "./internal/templates"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("guard run %q lacks %q", got[0], want)
		}
	}
	for _, banned := range []string{"-short", "-race"} {
		if strings.Contains(got[0], banned) {
			t.Errorf("guard run %q has %s: want full mode, no -race", got[0], banned)
		}
	}

	// A Go file anywhere triggers it too.
	write("pkg/y/y.go", "package y\n")
	commit("go")
	sh("bash", "scripts/ci-guard-tests.sh", "HEAD^1")
	if got := goRuns(); len(got) != 1 {
		t.Errorf("a Go change ran go %q, want one go test", got)
	}

	// A listed test that did not run, or skipped, fails the script, naming it.
	for _, env := range []string{"FAKE_DROP=internal/deadcodeguard.TestNoDeadClaims", "FAKE_SKIP=internal/deadcodeguard.TestNoDeadClaims"} {
		out, err := run([]string{env}, "bash", "scripts/ci-guard-tests.sh", "HEAD^1")
		if err == nil || !strings.Contains(out, "internal/deadcodeguard.TestNoDeadClaims did not run and pass") {
			t.Errorf("%s: err=%v, want a failure naming internal/deadcodeguard.TestNoDeadClaims:\n%s", env, err, out)
		}
		goRuns()
	}
	// A name two packages share must pass in each: a pass in vacuousguard
	// does not stand in for deadcodeguard's TestQuarantineIsTight.
	out, err := run([]string{"FAKE_DROP=internal/deadcodeguard.TestQuarantineIsTight"}, "bash", "scripts/ci-guard-tests.sh", "HEAD^1")
	if err == nil || !strings.Contains(out, "internal/deadcodeguard.TestQuarantineIsTight did not run and pass") {
		t.Errorf("a dropped shared name: err=%v, want a failure naming it:\n%s", err, out)
	}
	goRuns()

	if out, err := run(nil, "bash", "scripts/ci-guard-tests.sh", "no-such-rev"); err == nil || !strings.Contains(out, "cannot resolve base") {
		t.Errorf("an unresolvable base: err=%v, want a failure:\n%s", err, out)
	}
}
