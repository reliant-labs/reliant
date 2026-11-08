package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// golangci-lint before merge (owner, 2026-11-08): the merge queue's `golangci`
// job lints the changed packages beside the gate, and `CI result` (the one
// required check) fails unless that job passed. A job the ruleset does not
// require stops nothing, so these tests pin the wiring, not only the job.

const golangciJobName = "golangci (changed)"

type mqWorkflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]*struct {
		Name           string            `yaml:"name"`
		Needs          any               `yaml:"needs"`
		If             string            `yaml:"if"`
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Env            map[string]string `yaml:"env"`
		Steps          []struct {
			ciStep `yaml:",inline"`
			Env    map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadMergeQueue(t *testing.T) *mqWorkflow {
	t.Helper()
	var wf mqWorkflow
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/merge-queue.yml"), &wf); err != nil {
		t.Fatalf("parse merge-queue.yml: %v", err)
	}
	return &wf
}

// TestMergeQueueGolangciGates: the job exists, runs in parallel (neither job
// needs the other), lints from the group's base, and `CI result` ends by
// waiting for its verdict, unconditionally fatal, for longer than the job may
// run.
func TestMergeQueueGolangciGates(t *testing.T) {
	requireShellTools(t)
	wf := loadMergeQueue(t)
	if _, ok := wf.On["merge_group"]; !ok || len(wf.On) != 1 {
		t.Fatalf("merge-queue.yml runs on %v, want merge_group alone", wf.On)
	}
	gl, gate := wf.Jobs["golangci"], wf.Jobs["ci-result"]
	if gl == nil || gate == nil {
		t.Fatalf("merge-queue.yml needs jobs golangci and ci-result, has %v", sortedKeys(wf.Jobs))
	}
	if gl.Name != golangciJobName || gate.Name != "CI result" {
		t.Errorf("job names %q / %q, want %q / \"CI result\" (the ruleset's required check)", gl.Name, gate.Name, golangciJobName)
	}
	if gl.Needs != nil || gate.Needs != nil {
		t.Errorf("golangci needs %v, ci-result needs %v: a needs would serialize them (one more runner queue in front of the gate)", gl.Needs, gate.Needs)
	}
	if gl.Env["BASE"] != "${{ github.event.merge_group.base_sha }}" {
		t.Errorf("golangci BASE = %q, want the group's base sha", gl.Env["BASE"])
	}
	var lints bool
	for _, s := range gl.Steps {
		if strings.Contains(s.Run, `task ci:golangci -- --base "${BASE}"`) {
			lints = true
		}
		if strings.HasSuffix(s.Uses, "/actions/setup") && s.With["golangci"] != "true" {
			t.Errorf("golangci's setup step does not install golangci-lint: with %v", s.With)
		}
	}
	if !lints {
		t.Error("the golangci job never runs `task ci:golangci -- --base \"${BASE}\"`")
	}

	idx := -1
	for i, s := range gate.Steps {
		if s.Name == golangciJobName+" passed" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("CI result has no %q step: the golangci job would gate nothing", golangciJobName+" passed")
	}
	wait := gate.Steps[idx]
	if wait.If != "" || wait.ContinueOnError != nil {
		t.Errorf("the wait step must be unconditional and fatal, has if=%q continue-on-error=%v", wait.If, wait.ContinueOnError)
	}
	if wait.Env["JOB"] != gl.Name {
		t.Errorf("the wait step reads job %q, but the job is named %q", wait.Env["JOB"], gl.Name)
	}
	if w, _ := strconv.Atoi(wait.Env["WAIT_S"]); w <= gl.TimeoutMinutes*60 {
		t.Errorf("WAIT_S=%d is not above the job's %dm timeout: a slow lint would be read as unfinished, not failed", w, gl.TimeoutMinutes)
	}
	for _, s := range gate.Steps[:idx] {
		if s.Name == "Five-minute budget" {
			t.Error("the wait must come before the budget step, which reports the run's wall time")
		}
	}

	// The script against a fake gh that answers with one state per call.
	run := func(waitS string, states ...string) (bool, string) {
		dir := t.TempDir()
		var answers strings.Builder
		for _, s := range states {
			answers.WriteString(s + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "answers"), []byte(answers.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		// Each call pops the next line; a line "fail" is an API error, and
		// past the last line the last one repeats.
		writeExec(t, filepath.Join(dir, "gh"), `#!/usr/bin/env bash
case "$*" in *"/actions/runs/123/attempts/2/jobs"*) ;; *) echo "unexpected gh $*" >&2; exit 9 ;; esac
f="`+dir+`/answers"
line=$(head -1 "$f"); [ "$(wc -l <"$f")" -gt 1 ] && sed -i 1d "$f"
[ "$line" = fail ] && exit 1
printf '%s\n' "$line"
`)
		env := map[string]string{
			"PATH": dir + ":" + os.Getenv("PATH"), "GITHUB_REPOSITORY": "o/r", "GITHUB_RUN_ID": "123",
			"GITHUB_RUN_ATTEMPT": "2", "GITHUB_SERVER_URL": "https://github.com", "GH_TOKEN": "tok",
			"JOB": wait.Env["JOB"], "WAIT_S": waitS, "POLL_S": "0",
		}
		return ciShell(t, wait.Run, env)
	}
	if ok, out := run("0", "completed success"); !ok {
		t.Fatalf("harness: a passed job fails the step, so the cases below prove nothing:\n%s", out)
	}
	if ok, out := run("60", "queued ", "in_progress ", "fail", "completed success"); !ok {
		t.Errorf("a job that passes after queuing, running and one API error fails the step:\n%s", out)
	}
	for _, bad := range [][]string{
		{"completed failure"}, {"completed cancelled"}, {"completed skipped"}, {"completed timed_out"},
		{"in_progress "}, {"absent "}, {"fail"},
	} {
		// WAIT_S=0: one poll, then the deadline has passed.
		if ok, out := run("0", bad...); ok {
			t.Errorf("CI result passes with golangci at %q:\n%s", bad, out)
		}
	}
}

// TestCIRunGolangciGates: on a ci:run pull request ci.yml runs the same job
// beside the merge gate, and `CI result` there requires it.
func TestCIRunGolangciGates(t *testing.T) {
	requireShellTools(t)
	wf := loadCIWorkflow(t)
	if got, want := wf.job(t, "golangci").If, wf.job(t, "merge-gate").If; got != want {
		t.Errorf("golangci runs if %q, want the merge gate's %q", got, want)
	}
	needs := wf.job(t, "ci-result").needs()
	if !strings.Contains(strings.Join(needs, ","), "golangci") {
		t.Fatal("ci-result does not need golangci: a finding would not fail the check")
	}
	_, gate := wf.job(t, "ci-result").step(t, "ci-result", "Every job that ran passed")
	pr := func(golangci string) bool {
		r := map[string]string{}
		for _, n := range needs {
			r[n] = "skipped"
		}
		r["lanes"], r["merge-gate"], r["golangci"] = "success", "success", golangci
		ok, _ := ciShell(t, gate.Run, map[string]string{"RESULTS": needsJSON(r), "EVENT_NAME": "pull_request", "CI_RUN": "true"})
		return ok
	}
	if !pr("success") {
		t.Fatal("harness: CI result fails a ci:run PR with golangci green")
	}
	for _, bad := range []string{"failure", "cancelled", "skipped"} {
		if pr(bad) {
			t.Errorf("CI result passes a ci:run PR whose golangci result is %q", bad)
		}
	}
}

// TestSetupReadsForgesGolangciPin runs the setup action's pin step against a
// fake go whose forge module holds a toolversions.go: the version is forge's
// templates.GolangciLintVersion, and a forge without one fails the step.
func TestSetupReadsForgesGolangciPin(t *testing.T) {
	requireShellTools(t)
	var action struct {
		Runs struct {
			Steps []ciStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(readRepo(t, filepath.Join(setupDir, "action.yml")), &action); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, s := range action.Runs.Steps {
		if s.Name == "Read the golangci-lint pin" {
			script = s.Run
		}
	}
	if script == "" {
		t.Fatal(`setup action has no "Read the golangci-lint pin" step`)
	}
	run := func(src string) (bool, string) {
		dir := t.TempDir()
		mod := filepath.Join(dir, "mod")
		if err := os.MkdirAll(filepath.Join(mod, "internal", "templates"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(mod, "internal", "templates", "toolversions.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		writeExec(t, filepath.Join(dir, "go"), `#!/usr/bin/env bash
[ "$*" = "mod download -json github.com/reliant-labs/forge" ] || { echo "unexpected go $*" >&2; exit 9; }
echo '{"Path":"github.com/reliant-labs/forge","Dir":"`+mod+`"}'
`)
		out := filepath.Join(dir, "out")
		ok, log := ciShell(t, script, map[string]string{"PATH": dir + ":" + os.Getenv("PATH"), "GITHUB_OUTPUT": out})
		b, _ := os.ReadFile(out)
		return ok, log + "\nGITHUB_OUTPUT:\n" + string(b)
	}
	ok, out := run("package templates\n\n// GolangciLintVersion is ...\nconst GolangciLintVersion = \"v2.14.0\"\n\nconst PrettierVersion = \"3.6.2\"\n")
	if !ok || !strings.Contains(out, "version=2.14.0\n") {
		t.Errorf("pin step: ok=%v, want version=2.14.0 in GITHUB_OUTPUT:\n%s", ok, out)
	}
	if ok, out := run("package templates\n\nconst PrettierVersion = \"3.6.2\"\n"); ok {
		t.Errorf("pin step passes on a forge with no GolangciLintVersion:\n%s", out)
	}
}

// TestGolangciChangedPlan runs scripts/golangci-changed.sh --plan over a
// throwaway module: a changed package is linted; a deleted directory, the
// nested module, testdata, a directory the go tool ignores and a directory
// whose files are all behind a build tag are not; and a diff with no Go
// package is a quick no-op.
func TestGolangciChangedPlan(t *testing.T) {
	testkit.Slow(t, "runs scripts/golangci-changed.sh (git and go list subprocesses over a throwaway module)",
		testkit.Inputs("scripts/golangci-changed.sh", "scripts/lint-scope.sh"))
	for _, tool := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sh := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GOFLAGS=-mod=mod")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("scripts/golangci-changed.sh", string(readRepo(t, "scripts/golangci-changed.sh")))
	write("scripts/lint-scope.sh", string(readRepo(t, "scripts/lint-scope.sh")))
	write("gone/gone.go", "package gone\n")
	write("kept/kept.go", "package kept\n")
	write("sdk/go/go.mod", "module example.com/m/sdk\n\ngo 1.22\n")
	write("sdk/go/sdk.go", "package sdk\n")
	sh("chmod", "+x", "scripts/golangci-changed.sh", "scripts/lint-scope.sh")
	sh("git", "init", "-q", "-b", "main")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "base")
	base := strings.TrimSpace(sh("git", "rev-parse", "HEAD"))

	sh("git", "rm", "-rq", "gone")
	write("kept/kept.go", "package kept\n\nfunc F() {}\n")
	write("added/a/a_test.go", "package a\n")
	write("sdk/go/sdk.go", "package sdk\n\nfunc F() {}\n")
	write("kept/testdata/fix.go", "package fix\n")
	write("_ignored/x.go", "package x\n")
	write("tagged/t.go", "//go:build integration\n\npackage tagged\n")
	write("README.md", "docs\n")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "change")

	out := sh("bash", "scripts/golangci-changed.sh", "--base", base, "--plan")
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  ./") {
			got = append(got, strings.TrimSpace(l))
		}
	}
	if want := "./added/a ./kept"; strings.Join(got, " ") != want {
		t.Errorf("planned %q, want %q:\n%s", got, want, out)
	}

	write("README.md", "more docs\n")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "docs")
	head := strings.TrimSpace(sh("git", "rev-parse", "HEAD^"))
	// Without --plan: no package changed, so golangci-lint is never looked up.
	cmd := exec.Command("bash", "scripts/golangci-changed.sh", "--base", head)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "PATH=/usr/bin:/bin")
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "nothing to lint") {
		t.Errorf("a docs-only diff: err=%v, want a clean no-op:\n%s", err, b)
	}
}
