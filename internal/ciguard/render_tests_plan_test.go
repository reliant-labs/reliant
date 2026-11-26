//go:build !windows

// scripts/ci-render-tests.sh is bash; this test drives it with real git over a
// throwaway repository and a fake `go` (POSIX shell), so the file builds for
// every OS but Windows.

package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderTestsPlan runs scripts/ci-render-tests.sh over a throwaway
// repository: a change to a KCL template or a composition root runs the
// listed tests in full mode (no -short, no -race), one anchored -run per
// package; any other change runs nothing; a listed test its package no
// longer declares fails the script; an unresolvable base fails it.
func TestRenderTestsPlan(t *testing.T) {
	if testing.Short() {
		t.Skip("runs scripts/ci-render-tests.sh with real git over a throwaway repository; runs in task test")
	}
	for _, tool := range []string{"bash", "git"} {
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
	for dst, src := range map[string]string{
		"scripts/ci-render-tests.sh": renderTestsScript,
		"scripts/ci-go-test.sh":      filepath.Join("..", "..", "scripts", "ci-go-test.sh"),
	} {
		write(dst, readText(t, src))
	}

	// The fake go: `go test -list RE ./pkg` prints $FAKE_LIST_<pkg base>
	// (default: every name in RE), and any other `go test` appends its
	// arguments to $dir/go.log.
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(`#!/usr/bin/env bash
set -eu
[[ $1 == test ]] || exit 1
shift
if [[ $1 == -list ]]; then
  var=FAKE_LIST_$(basename "$3")
  if [[ -n "${!var+x}" ]]; then printf '%s\n' "${!var}"; else re=${2#^(}; tr '|' '\n' <<<"${re%)\$}"; fi
  echo "ok  $3 0.01s"
  exit 0
fi
echo "$*" >>"$FAKE_GO_LOG"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(dir, "go.log")
	run := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
			"GIT_COMMITTER_EMAIL=t@t", "CI_BASE=", "FAKE_GO_LOG="+goLog, "PATH="+fakeBin+":"+os.Getenv("PATH"))
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

	write("internal/templates/project/config-roots.k.tmpl", "v1\n")
	write("internal/templates/deploy/kcl/dev/main.k.tmpl", "v1\n")
	write("internal/codegen/composition_roots_kcl.go", "package codegen\n")
	write("internal/codegen/other.go", "package codegen\n")
	write("docs/d.md", "v1\n")
	sh("git", "init", "-q", "-b", "main")
	commit("base")

	// Not a trigger: nothing runs, and the plan says so.
	write("docs/d.md", "v2\n")
	write("internal/codegen/other.go", "package codegen\n\nconst X = 1\n")
	commit("docs")
	if out := sh("bash", "scripts/ci-render-tests.sh", "HEAD^1"); !strings.Contains(out, "nothing to run") {
		t.Errorf("a docs and non-trigger codegen change: want nothing to run, got:\n%s", out)
	}
	if got := goRuns(); got != nil {
		t.Errorf("a change that touches no trigger ran go %q", got)
	}

	// A project KCL template (directly under project/: `**/` matches no
	// directory too) runs both packages' tests, full mode, anchored.
	write("internal/templates/project/config-roots.k.tmpl", "v2\n")
	commit("template")
	plan := sh("bash", "scripts/ci-render-tests.sh", "--plan", "HEAD^1")
	if !strings.Contains(plan, "internal/templates/project/config-roots.k.tmpl") ||
		!strings.Contains(plan, "./internal/templates: ^(TestFreshScaffoldRendersEveryEnvHosted|") {
		t.Errorf("--plan for a config-roots.k.tmpl change does not name the file and the templates tests:\n%s", plan)
	}
	if got := goRuns(); got != nil {
		t.Errorf("--plan ran go %q", got)
	}
	sh("bash", "scripts/ci-render-tests.sh", "HEAD^1")
	got := goRuns()
	if len(got) != 2 || !strings.HasSuffix(got[0], " ./internal/templates") || !strings.HasSuffix(got[1], " ./internal/codegen") {
		t.Fatalf("a template change ran go %q, want one run of ./internal/templates and one of ./internal/codegen", got)
	}
	for _, g := range got {
		if strings.Contains(g, "-short") || strings.Contains(g, "-race") || !strings.Contains(g, "-run ^(") {
			t.Errorf("render run %q: want full mode (no -short), no -race, an anchored -run", g)
		}
	}

	// An env template and a composition root trigger it too.
	for _, f := range []string{"internal/templates/deploy/kcl/dev/main.k.tmpl", "internal/codegen/composition_roots_kcl.go"} {
		write(f, "package codegen\n\n// changed\n")
		commit(f)
		sh("bash", "scripts/ci-render-tests.sh", "HEAD^1")
		if got := goRuns(); len(got) != 2 {
			t.Errorf("a change to %s ran go %q, want both packages' render tests", f, got)
		}
	}

	// A listed test the package no longer declares fails, naming it, and
	// nothing runs.
	out, err := run([]string{"FAKE_LIST_templates=TestSomethingElse"}, "bash", "scripts/ci-render-tests.sh", "HEAD^1")
	if err == nil || !strings.Contains(out, "declares no TestFreshScaffoldRendersEveryEnvHosted") {
		t.Errorf("a renamed render test: err=%v, want a failure naming it:\n%s", err, out)
	}
	if got := goRuns(); got != nil {
		t.Errorf("a renamed render test still ran go %q", got)
	}

	if out, err := run(nil, "bash", "scripts/ci-render-tests.sh", "no-such-rev"); err == nil || !strings.Contains(out, "cannot resolve base") {
		t.Errorf("an unresolvable base: err=%v, want a failure:\n%s", err, out)
	}
}
