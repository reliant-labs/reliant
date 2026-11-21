package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// golangci-lint before merge (owner, 2026-11-08): the merge queue's `golangci`
// job lints the changed packages beside the gate, and `CI result` (the one
// required check) fails unless that job passed. A job the ruleset does not
// require stops nothing, so these tests pin the wiring, not only the job.
//
// Since reliant #3095 `CI result` is a native aggregate: it `needs:` the
// workers and reads their results from `needs`, never from the Actions API
// (an anonymous poll that answered 403 turned green groups red). The third
// worker, `vitest` (the affected vitest files in four shards), is pinned by
// TestMergeQueueVitestShards.

const golangciJobName = "golangci (changed)"

type mqWorkflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]*struct {
		Name           string `yaml:"name"`
		Needs          any    `yaml:"needs"`
		If             string `yaml:"if"`
		TimeoutMinutes int    `yaml:"timeout-minutes"`
		Permissions    any    `yaml:"permissions"`
		Strategy       struct {
			FailFast *bool          `yaml:"fail-fast"`
			Matrix   map[string]any `yaml:"matrix"`
		} `yaml:"strategy"`
		Env   map[string]string `yaml:"env"`
		Steps []mqStep          `yaml:"steps"`
	} `yaml:"jobs"`
}

// mqStep is one step of a merge-queue.yml job.
type mqStep struct {
	ciStep `yaml:",inline"`
	Env    map[string]string `yaml:"env"`
}

func loadMergeQueue(t *testing.T) *mqWorkflow {
	t.Helper()
	var wf mqWorkflow
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/merge-queue.yml"), &wf); err != nil {
		t.Fatalf("parse merge-queue.yml: %v", err)
	}
	return &wf
}

// mqNeeds is a job's `needs:`, as a list whichever YAML form it takes.
func mqNeeds(v any) []string {
	switch n := v.(type) {
	case string:
		return []string{n}
	case []any:
		out := make([]string, 0, len(n))
		for _, x := range n {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

// mqWorkers are the merge queue's workers: the jobs `CI result` needs, each
// of which must succeed. Every one runs from the start of the group.
var mqWorkers = []string{"golangci", "merge-gate", "vitest"}

// TestMergeQueueGolangciGates: the workers (the merge gate, the lint and the
// vitest shards) run in parallel, none needing another; the lint lints from
// the group's base; and `CI result` needs exactly the workers, runs always,
// and passes only when every one succeeded.
func TestMergeQueueGolangciGates(t *testing.T) {
	requireShellTools(t)
	wf := loadMergeQueue(t)
	if _, ok := wf.On["merge_group"]; !ok || len(wf.On) != 1 {
		t.Fatalf("merge-queue.yml runs on %v, want merge_group alone", wf.On)
	}
	gl, gate, result := wf.Jobs["golangci"], wf.Jobs["merge-gate"], wf.Jobs["ci-result"]
	if gl == nil || gate == nil || result == nil {
		t.Fatalf("merge-queue.yml needs jobs golangci, merge-gate and ci-result, has %v", sortedKeys(wf.Jobs))
	}
	if gl.Name != golangciJobName || result.Name != "CI result" {
		t.Errorf("job names %q / %q, want %q / \"CI result\" (the ruleset's required check)", gl.Name, result.Name, golangciJobName)
	}
	for id, j := range wf.Jobs {
		if id != "ci-result" && j.Name == "CI result" {
			t.Errorf("job %s is also named \"CI result\": the required check would have two producers", id)
		}
	}
	for _, id := range mqWorkers {
		j := wf.Jobs[id]
		if j == nil {
			t.Errorf("merge-queue.yml has no worker job %s", id)
			continue
		}
		if j.Needs != nil {
			t.Errorf("%s needs %v: a needs between workers would serialize them (one more runner queue in front of the gate)", id, j.Needs)
		}
	}
	if gl.If != "" {
		t.Errorf("golangci if=%q: a skipped lint job is not a success, so a no-change group must skip its steps, not the job", gl.If)
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

	// The aggregate: exactly the workers, always run, and nothing but
	// their results — no checkout, no action, no token, no API.
	needs := mqNeeds(result.Needs)
	slices.Sort(needs)
	if !slices.Equal(needs, mqWorkers) {
		t.Errorf("ci-result needs %v, want exactly %v", result.Needs, mqWorkers)
	}
	for id := range wf.Jobs {
		if id != "ci-result" && !slices.Contains(mqWorkers, id) {
			t.Errorf("job %s is neither a worker CI result needs nor CI result: a job the required check does not read stops nothing", id)
		}
	}
	if result.If != "always()" {
		t.Errorf("ci-result if=%q, want always(): a failed or cancelled worker must fail the required check, not skip it", result.If)
	}
	if p, ok := result.Permissions.(map[string]any); !ok || len(p) != 0 {
		t.Errorf("ci-result permissions %v, want {}: it reads only `needs`", result.Permissions)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("ci-result has %d steps, want the one aggregation step", len(result.Steps))
	}
	agg := result.Steps[0]
	if agg.Uses != "" || agg.If != "" || agg.ContinueOnError != nil {
		t.Errorf("the aggregation step must be a plain, unconditional, fatal run step: uses=%q if=%q continue-on-error=%v", agg.Uses, agg.If, agg.ContinueOnError)
	}
	if len(agg.Env) != 1 || agg.Env["RESULTS"] != "${{ toJSON(needs) }}" {
		t.Errorf("the aggregation step's env is %v, want RESULTS=${{ toJSON(needs) }} alone", agg.Env)
	}
	for _, banned := range []string{"curl", "gh ", "api.github.com", "github.token", "GITHUB_TOKEN", "GH_TOKEN", "secrets."} {
		if strings.Contains(agg.Run, banned) {
			t.Errorf("the aggregation step mentions %q: the verdict must come from `needs` alone", banned)
		}
	}

	// The acceptance matrix, against the real script. "missing" omits the
	// job from `needs` altogether.
	run := func(t *testing.T, results map[string]string) (bool, string) {
		t.Helper()
		r := map[string]string{}
		for job, res := range results {
			if res != "missing" {
				r[job] = res
			}
		}
		return ciShell(t, agg.Run, map[string]string{"RESULTS": needsJSON(r), "GH_TOKEN": "", "GITHUB_TOKEN": ""})
	}
	green := func() map[string]string {
		r := map[string]string{}
		for _, w := range mqWorkers {
			r[w] = "success"
		}
		return r
	}
	if ok, out := run(t, green()); !ok {
		t.Fatalf("harness: CI result fails with every worker green, so the cases below prove nothing:\n%s", out)
	}
	bad := []string{"failure", "cancelled", "skipped", "missing"}
	for _, b := range bad {
		// Each worker alone not green, and all of them at once.
		cases := []map[string]string{}
		for _, w := range mqWorkers {
			c := green()
			c[w] = b
			cases = append(cases, c)
		}
		all := green()
		for w := range all {
			all[w] = b
		}
		cases = append(cases, all)
		for _, c := range cases {
			name := ""
			for _, w := range mqWorkers {
				name += w + "=" + c[w] + "/"
			}
			t.Run(strings.TrimSuffix(name, "/"), func(t *testing.T) {
				t.Parallel()
				ok, out := run(t, c)
				if ok {
					t.Fatalf("CI result passes with %v:\n%s", c, out)
				}
				for job, res := range c {
					if res != "success" && !strings.Contains(out, job+": "+res) {
						t.Errorf("the failure does not name %s: %s:\n%s", job, res, out)
					}
				}
			})
		}
	}
	t.Run("unparseable needs", func(t *testing.T) {
		t.Parallel()
		if ok, out := ciShell(t, agg.Run, map[string]string{"RESULTS": "not json"}); ok {
			t.Errorf("CI result passes on an unreadable needs document:\n%s", out)
		}
	})
}

// TestMergeQueueBudgetStaysOnTheGate: the five-minute budget measurement and
// its merge-queue-slo-<attempt> artifact belong to the merge gate, whose
// bindings the default-branch Merge queue SLO listener verifies.
func TestMergeQueueBudgetStaysOnTheGate(t *testing.T) {
	wf := loadMergeQueue(t)
	var budget, upload bool
	for _, s := range wf.Jobs["merge-gate"].Steps {
		switch {
		case s.Name == "Five-minute budget":
			budget = s.If == "always()" && s.ContinueOnError == true &&
				strings.Contains(s.Run, `schema:"merge-queue-slo/v1"`) && strings.Contains(s.Run, "${RUNNER_TEMP}/merge-queue-slo.json")
		case strings.HasPrefix(s.Uses, "actions/upload-artifact@") && s.With["name"] == "merge-queue-slo-${{ github.run_attempt }}":
			upload = s.If == "always()" && s.ContinueOnError == true && s.With["path"] == "${{ runner.temp }}/merge-queue-slo.json"
		}
	}
	if !budget || !upload {
		t.Errorf("merge-gate must measure the budget (%v) and upload merge-queue-slo-<attempt> (%v), both non-gating", budget, upload)
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
	pr := func(t *testing.T, golangci string) bool {
		t.Helper()
		r := map[string]string{}
		for _, n := range needs {
			r[n] = "skipped"
		}
		r["lanes"], r["merge-gate"], r["golangci"] = "success", "success", golangci
		ok, _ := ciShell(t, gate.Run, map[string]string{"RESULTS": needsJSON(r), "EVENT_NAME": "pull_request", "CI_RUN": "true"})
		return ok
	}
	if !pr(t, "success") {
		t.Fatal("harness: CI result fails a ci:run PR with golangci green")
	}
	for _, bad := range []string{"failure", "cancelled", "skipped"} {
		// Independent bash runs: in parallel, not one after another.
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			if pr(t, bad) {
				t.Errorf("CI result passes a ci:run PR whose golangci result is %q", bad)
			}
		})
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

	// The same diff with a go that cannot list packages: discovery failed, so
	// the script fails rather than reading the failure as an empty scope.
	fake := t.TempDir()
	writeExec(t, filepath.Join(fake, "go"), "#!/usr/bin/env bash\necho 'go: toolchain download failed' >&2\nexit 1\n")
	cmd := exec.Command("bash", "scripts/golangci-changed.sh", "--base", base, "--plan")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "PATH="+fake+":"+os.Getenv("PATH"))
	if b, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(b), "nothing to lint") {
		t.Errorf("a failing go list: err=%v, want a non-zero exit, not an empty scope:\n%s", err, b)
	}

	// Without --plan, over the same diff: golangci-lint is invoked on exactly
	// the planned packages, findings limited to the diff's whole files from
	// base, under -trimpath, waiting for golangci-lint's machine lock (or,
	// --in-slot, dropping it); and a finding (golangci-lint's exit 1) fails
	// the script with that status.
	lint := t.TempDir()
	argsFile := filepath.Join(lint, "args")
	writeExec(t, filepath.Join(lint, "golangci-lint"), "#!/usr/bin/env bash\n"+
		"[ \"$1\" = version ] && { echo 'golangci-lint has version 2.14.0 built with go1.27.0 from fake'; exit 0; }\n"+
		"{ printf '%s\\n' \"$*\"; printf 'GOFLAGS=%s\\n' \"$GOFLAGS\"; } > \""+argsFile+"\"\n"+
		"[ \"$FAKE_FINDING\" = 1 ] && { echo 'kept/kept.go:3:1: finding (fake)'; exit 1; }\n"+
		"exit 0\n")
	for _, tc := range []struct {
		finding, inSlot bool
		lock            string
	}{{false, false, "--allow-serial-runners"}, {true, false, "--allow-serial-runners"}, {false, true, "--allow-parallel-runners"}} {
		finding := tc.finding
		_ = os.Remove(argsFile)
		args := []string{"scripts/golangci-changed.sh", "--base", base}
		if tc.inSlot {
			args = append(args, "--in-slot")
		}
		cmd = exec.Command("bash", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+lint+":"+os.Getenv("PATH"), "GOFLAGS=-mod=mod", "GOLANGCI_LINT_VERSION=v2.14.0")
		if finding {
			cmd.Env = append(cmd.Env, "FAKE_FINDING=1")
		}
		b, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		switch {
		case !finding && err != nil:
			t.Errorf("a clean lint: err=%v, want exit 0:\n%s", err, b)
		case finding && (!errors.As(err, &exit) || exit.ExitCode() != 1):
			t.Errorf("a lint finding: err=%v, want golangci-lint's exit 1 passed through:\n%s", err, b)
		}
		got, rerr := os.ReadFile(argsFile)
		if rerr != nil {
			t.Fatalf("finding=%v: golangci-lint run was never invoked: %v\n%s", finding, rerr, b)
		}
		want := "run " + tc.lock + " --new-from-rev=" + base + " --whole-files ./added/a ./kept\nGOFLAGS=-mod=mod -trimpath\n"
		if string(got) != want {
			t.Errorf("finding=%v: golangci-lint invoked as\n%s\nwant\n%s", finding, got, want)
		}
	}

	// A golangci-lint that is not the pinned release fails before linting
	// anything, naming the install command: another release reports other
	// findings, so its pass would not predict the merge queue's.
	_ = os.Remove(argsFile)
	cmd = exec.Command("bash", "scripts/golangci-changed.sh", "--base", base)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+lint+":"+os.Getenv("PATH"), "GOFLAGS=-mod=mod", "GOLANGCI_LINT_VERSION=2.15.0")
	b, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(b), "golangci-lint@v2.15.0") {
		t.Errorf("an unpinned golangci-lint: err=%v, want exit 2 naming the pinned install:\n%s", err, b)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Error("an unpinned golangci-lint was run")
	}

	// No golangci-lint on PATH at all (a runner whose setup did not install
	// it): exit 2 with the pinned install command, before any lint.
	goBin, _ := exec.LookPath("go")
	bare := filepath.Dir(goBin) + ":/usr/bin:/bin"
	hidden := goBin != ""
	for _, d := range filepath.SplitList(bare) {
		if _, serr := os.Stat(filepath.Join(d, "golangci-lint")); serr == nil {
			hidden = false // beside go or in a system dir: cannot be hidden here
		}
	}
	if hidden {
		cmd = exec.Command("bash", "scripts/golangci-changed.sh", "--base", base)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+bare, "GOFLAGS=-mod=mod", "GOLANGCI_LINT_VERSION=2.14.0")
		b, err := cmd.CombinedOutput()
		if !errors.As(err, &exit) || exit.ExitCode() != 2 ||
			!strings.Contains(string(b), "golangci-lint is not on PATH; install the pinned v2.14.0: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0") {
			t.Errorf("no golangci-lint on PATH: err=%v, want exit 2 with the pinned install command:\n%s", err, b)
		}
	}

	// An untracked .go file is in the diff (golangci-lint's --new-from-rev
	// reads the working tree); on a clean checkout, as in CI, nothing changes.
	write("loose/l.go", "package loose\n")
	if out := sh("bash", "scripts/golangci-changed.sh", "--base", base, "--plan"); !strings.Contains(out, "  ./loose\n") {
		t.Errorf("an untracked package is not planned:\n%s", out)
	}
	sh("rm", "-r", "loose")

	write("README.md", "more docs\n")
	sh("git", "add", "-A")
	sh("git", "commit", "-qm", "docs")
	head := strings.TrimSpace(sh("git", "rev-parse", "HEAD^"))
	// Without --plan: no package changed, so golangci-lint is never looked up.
	cmd = exec.Command("bash", "scripts/golangci-changed.sh", "--base", head)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "PATH=/usr/bin:/bin")
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "nothing to lint") {
		t.Errorf("a docs-only diff: err=%v, want a clean no-op:\n%s", err, b)
	}
}

// TestGolangciChangedFailsOnAG101 runs scripts/golangci-changed.sh, the merge
// gate's golangci phase and the merge queue's golangci (changed) job, with the
// real pinned golangci-lint and this repository's .golangci.yml over a
// throwaway module whose diff adds a hard-coded credential: gosec's G101, the
// finding #2368 reached main with and the queue's job ejects a PR for. It
// fails with the finding named; the same module without it passes.
func TestGolangciChangedFailsOnAG101(t *testing.T) {
	testkit.Slow(t, "runs the real golangci-lint (go list and type-checking) over a throwaway module",
		testkit.Inputs("scripts/golangci-changed.sh", "scripts/lint-scope.sh", ".golangci.yml"))
	for _, tool := range []string{"bash", "git", "go", "jq", "golangci-lint"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	// The pin, read as the script reads it in this repository.
	mod, err := exec.Command("go", "mod", "download", "-json", "github.com/reliant-labs/forge").Output()
	if err != nil {
		t.Skipf("cannot download the pinned forge module: %v", err)
	}
	var fm struct{ Dir string }
	if err := json.Unmarshal(mod, &fm); err != nil {
		t.Fatal(err)
	}
	tv, err := os.ReadFile(filepath.Join(fm.Dir, "internal", "templates", "toolversions.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^const GolangciLintVersion = "v([0-9.]+)"$`).FindSubmatch(tv)
	if m == nil {
		t.Fatal("no GolangciLintVersion in the pinned forge")
	}
	pin := string(m[1])
	if v, _ := exec.Command("golangci-lint", "version").CombinedOutput(); !strings.Contains(string(v), "has version "+pin+" ") {
		t.Skipf("golangci-lint on PATH is not the pinned %s: %s", pin, v)
	}

	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		putFixture(t, dir, rel, body, 0o644)
	}
	sh := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GOFLAGS=-mod=mod"), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := sh(nil, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write(".golangci.yml", string(readRepo(t, ".golangci.yml")))
	write("scripts/golangci-changed.sh", string(readRepo(t, "scripts/golangci-changed.sh")))
	write("scripts/lint-scope.sh", string(readRepo(t, "scripts/lint-scope.sh")))
	write("kept/kept.go", "// Package kept is a fixture.\npackage kept\n")
	must("chmod", "+x", "scripts/golangci-changed.sh", "scripts/lint-scope.sh")
	must("git", "init", "-q", "-b", "main")
	must("git", "add", "-A")
	must("git", "commit", "-qm", "base")
	base := strings.TrimSpace(must("git", "rev-parse", "HEAD"))

	lint := func() (string, error) {
		return sh([]string{"GOLANGCI_LINT_VERSION=" + pin}, "bash", "scripts/golangci-changed.sh", "--base", base)
	}
	write("kept/kept.go", "// Package kept is a fixture.\npackage kept\n\n// Answer is not a secret.\nconst Answer = 42\n")
	if out, err := lint(); err != nil {
		t.Fatalf("a clean change fails the lint: %v\n%s", err, out)
	}
	// The planted value is derived at run time, so no credential-shaped
	// literal sits in this file for a secret scanner to flag: 40 mixed-case
	// base64url characters of a fixed hash, high-entropy enough for G101.
	sum := sha256.Sum256([]byte("reliant merge-gate G101 fixture"))
	planted := base64.RawURLEncoding.EncodeToString(sum[:])[:40]
	write("kept/kept.go", "// Package kept is a fixture.\npackage kept\n\n// Token is a hard-coded credential.\nconst Token = \""+planted+"\"\n")
	must("git", "add", "-A")
	must("git", "commit", "-qm", "credential")
	out, err := lint()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, "G101") {
		t.Errorf("a G101 finding: err=%v, want golangci-lint's exit 1 naming G101:\n%s", err, out)
	}
}

// TestEveryMergeGateCallerProvisionsGolangci: every workflow step that runs the
// merge gate (task ci:merge) either installs the pinned golangci-lint in its
// job (the setup action's golangci input), or hands golangci to a golangci
// (changed) job beside it (--queue, --skip-golangci) that the workflow runs
// under the same condition. Otherwise a Go change fails the gate's golangci
// phase on a clean runner ("golangci-lint is not on PATH") instead of being
// linted.
func TestEveryMergeGateCallerProvisionsGolangci(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var wf ciWorkflow
		if err := yaml.Unmarshal(b, &wf); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		name := filepath.Base(f)
		for id, job := range wf.Jobs {
			installs := false
			for _, s := range job.Steps {
				if strings.HasSuffix(s.Uses, "/actions/setup") && fmt.Sprint(s.With["golangci"]) == "true" {
					installs = true
				}
			}
			for _, s := range job.Steps {
				if !strings.Contains(s.Run, "task ci:merge") && !strings.Contains(s.Run, "merge-gate.sh") {
					continue
				}
				callers++
				delegates := strings.Contains(s.Run, "--queue") || strings.Contains(s.Run, "--skip-golangci")
				if !installs && !delegates {
					t.Errorf("%s job %s step %q runs the merge gate without golangci-lint installed: set the setup action's golangci: \"true\", or pass --skip-golangci beside a golangci (changed) job", name, id, s.Name)
					continue
				}
				if !delegates {
					continue
				}
				beside := false
				for gid, g := range wf.Jobs {
					for _, gs := range g.Steps {
						if gid != id && strings.Contains(gs.Run, "task ci:golangci") && g.If == job.If {
							beside = true
						}
					}
				}
				if !beside {
					t.Errorf("%s job %s hands golangci elsewhere, but no job of %s runs task ci:golangci under its condition %q", name, id, name, job.If)
				}
			}
		}
	}
	if callers < 2 {
		t.Fatalf("found %d merge-gate caller(s) in .github/workflows; want ci.yml's and merge-queue.yml's", callers)
	}
}
