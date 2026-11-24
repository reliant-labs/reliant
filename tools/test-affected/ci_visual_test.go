package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"gopkg.in/yaml.v3"
)

// The visual baseline as a gate (owner, 2026-11-24; reliant#3446). The merge
// queue's `visual` job runs scripts/visual-gate.sh, compare only, on the merge
// group's commit when scripts/ci-lanes.sh's `visual` lane says the group
// reaches it, and `CI result` needs the job (TestMergeQueueGolangciGates, via
// mqWorkers). visual-nightly.yml runs the same script on main and keeps the
// `visual:stale` issue. These tests pin each link: a job that stops gating,
// a lane rule that misses a renderer source, a gate that could re-capture or
// pass without comparing anything, and an issue that never closes.

const visualGated = "steps.lane.outputs.run == 'true'"

// TestMergeQueueVisualLane: the job always runs (a skip is its steps', with a
// notice), decides with scripts/ci-lanes.sh from the group's base, and runs
// scripts/visual-gate.sh fatally under that decision, with no Playwright args.
func TestMergeQueueVisualLane(t *testing.T) {
	wf := loadMergeQueue(t)
	v := wf.Jobs["visual"]
	if v == nil {
		t.Fatalf("merge-queue.yml has no visual job: %v", sortedKeys(wf.Jobs))
	}
	if v.If != "" {
		t.Errorf("visual if=%q: a group that reaches no visual input skips the job's steps, not the job", v.If)
	}
	if v.TimeoutMinutes <= 0 || v.TimeoutMinutes > 30 {
		t.Errorf("visual timeout-minutes = %d, want 1..30 (CI result runs after it, inside the ruleset's %d-minute check timeout)", v.TimeoutMinutes, rulesetCheckTimeoutMinutes)
	}
	if v.Env["BASE"] != "${{ github.event.merge_group.base_sha }}" {
		t.Errorf("visual BASE = %q, want the group's base sha", v.Env["BASE"])
	}
	if strings.Contains(v.Name, "CI result") {
		t.Errorf("visual is named %q: the required check would have more producers", v.Name)
	}
	decide, setup, gate := -1, -1, -1
	for i, s := range v.Steps {
		switch {
		case s.ID == "lane":
			decide = i
		case strings.HasSuffix(s.Uses, "/actions/setup"):
			setup = i
		case strings.Contains(s.Run, "scripts/visual-gate.sh"):
			gate = i
		}
	}
	if decide < 0 || setup < 0 || gate < 0 || decide > setup || setup > gate {
		t.Fatalf("visual needs the lane decision (%d), then setup (%d), then scripts/visual-gate.sh (%d)", decide, setup, gate)
	}
	if co := v.Steps[0]; !strings.HasPrefix(co.Uses, "actions/checkout@") || co.With["fetch-depth"] != 0 {
		t.Errorf("visual's first step must check out the whole history (fetch-depth 0), for the diff from the group's base: %+v", co)
	}
	d := v.Steps[decide]
	if d.If != "" || d.ContinueOnError != nil {
		t.Errorf("the decision step must run on every group and be fatal: if=%q continue-on-error=%v", d.If, d.ContinueOnError)
	}
	if !strings.Contains(d.Run, `scripts/ci-lanes.sh --base "${BASE}"`) || !strings.Contains(d.Run, "visual=") {
		t.Errorf("the decision step does not read scripts/ci-lanes.sh's visual lane from the group's base:\n%s", d.Run)
	}
	if s := v.Steps[setup]; s.If != visualGated || s.With["node"] != "true" {
		t.Errorf("visual's setup: if=%q node=%v, want if=%q and node", s.If, s.With["node"], visualGated)
	}
	g := v.Steps[gate]
	if g.If != visualGated || g.ContinueOnError != nil {
		t.Errorf("the gate step: if=%q continue-on-error=%v, want if=%q and fatal", g.If, g.ContinueOnError, visualGated)
	}
	if strings.TrimSpace(g.Run) != "scripts/visual-gate.sh" {
		t.Errorf("the gate step runs %q, want scripts/visual-gate.sh with no arguments (compare every surface, never re-capture)", g.Run)
	}
}

// TestMergeQueueVisualLaneDecision runs the decision step over the real
// scripts/ci-lanes.sh in a scratch repository: what the renderer's build and
// the visual suite read turns it on, tests, other suites, lint configs, docs
// and unrelated code leave it off with a notice, and a diff that cannot be
// computed turns it on.
func TestMergeQueueVisualLaneDecision(t *testing.T) {
	testkit.Slow(t, "runs the decision step in bash over scripts/ci-lanes.sh and git subprocesses",
		testkit.Inputs("scripts/ci-lanes.sh", ".github/workflows/merge-queue.yml"))
	requireShellTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wf := loadMergeQueue(t)
	var script string
	for _, s := range wf.Jobs["visual"].Steps {
		if s.ID == "lane" {
			script = s.Run
		}
	}
	if script == "" {
		t.Fatal("merge-queue.yml's visual job has no decision step (id: lane)")
	}
	lanes := readRepo(t, "scripts/ci-lanes.sh")

	run := func(t *testing.T, path, base string) (bool, string, string) {
		t.Helper()
		dir := t.TempDir()
		git := scratchGit(t, dir)
		scratchWrite(t, dir, "scripts/ci-lanes.sh", string(lanes))
		scratchWrite(t, dir, "README.md", "x\n")
		git("add", "-A")
		git("commit", "-qm", "base")
		if base == "" {
			base = git("rev-parse", "HEAD")
		}
		scratchWrite(t, dir, path, "changed\n")
		git("add", "-A")
		git("commit", "-qm", "change")
		outFile := filepath.Join(dir, "github_output")
		ok, out := ciShellIn(t, dir, script, map[string]string{"BASE": base, "GITHUB_OUTPUT": outFile})
		b, _ := os.ReadFile(outFile)
		return ok, strings.TrimSpace(string(b)), out
	}

	cases := []struct {
		path string
		want bool
	}{
		{"frontends/web/src/components/Button.tsx", true},
		{"frontends/web/src/index.css", true},
		{"frontends/web/src/gen/reliant/v1/chat_pb.ts", true},
		{"frontends/web/public/favicon.svg", true},
		{"frontends/web/index.html", true},
		{"frontends/web/vite.config.ts", true},
		{"frontends/web/tailwind.config.js", true},
		{"frontends/web/package.json", true},
		{"frontends/web/pnpm-lock.yaml", true},
		{"frontends/web/patches/@monaco-editor__loader@1.7.0.patch", true},
		{"frontends/web/playwright.visual.config.ts", true},
		{"frontends/web/scripts/visual-docker.sh", true},
		{"frontends/web/e2e/visual/__screenshots__/desktop-dark/chat.png", true},
		{"frontends/web/e2e/visual/fixtures/chat.ts", true},
		{"sdk/ts/src/client/index.ts", true},
		{"scripts/visual-gate.sh", true},
		{".github/workflows/merge-queue.yml", true},
		{".github/workflows/visual-nightly.yml", true},
		{"Taskfile.yml", true}, // test:web:visual lives there (and it is CI itself)
		{"frontends/web/src/components/__tests__/Button.test.tsx", false},
		{"frontends/web/src/lib/paths.test.ts", false},
		{"frontends/web/e2e/journeys/chat.spec.ts", false},
		{"frontends/web/playwright.config.ts", false},
		{"frontends/web/eslint.config.js", false},
		{"frontends/web/eslint-rules/no-raw-colors.js", false},
		{"frontends/web/README.md", false},
		{"sdk/ts/src/client/index.test.ts", false},
		{"sdk/ts/vitest.config.ts", false},
		{"internal/llm/client.go", false},
		{"docs/guide.md", false},
		{"electron/main.ts", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			ok, got, out := run(t, c.path, "")
			want := "run=false"
			if c.want {
				want = "run=true"
			}
			if !ok || got != want {
				t.Fatalf("ok=%v outputs=%q, want %q:\n%s", ok, got, want, out)
			}
			if !c.want && !strings.Contains(out, "::notice title=Visual baseline: skipped, a pass::") {
				t.Errorf("a skip must say why, as a notice:\n%s", out)
			}
		})
	}
	t.Run("a diff that cannot be computed runs the lane", func(t *testing.T) {
		t.Parallel()
		ok, got, out := run(t, "docs/guide.md", "0000000000000000000000000000000000000000")
		if !ok || got != "run=true" {
			t.Fatalf("ok=%v outputs=%q, want run=true:\n%s", ok, got, out)
		}
	})
}

// TestVisualGateScript runs scripts/visual-gate.sh in a scratch repository
// against a stub `task` (which records its arguments and writes the scenario's
// Playwright JSON report to VISUAL_JSON) and a stub visual-docker.sh (--pull).
// It must ask for compare-only, pass only when captures ran and matched, name
// every capture that differs with the re-capture command, and never pass a
// lane that compared nothing.
func TestVisualGateScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/visual-gate.sh in bash with git, jq and stub tools",
		testkit.Inputs("scripts/visual-gate.sh"))
	requireShellTools(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	gateScript := readRepo(t, "scripts/visual-gate.sh")

	capture := func(title, project, status, msg string) map[string]any {
		res := map[string]any{"status": "passed"}
		if msg != "" {
			res = map[string]any{"status": "failed", "error": map[string]any{"message": msg}}
		}
		return map[string]any{"title": title, "tests": []any{map[string]any{"projectName": project, "status": status, "results": []any{res}}}}
	}
	report := func(stats map[string]int, errs []any, specs ...map[string]any) string {
		b, err := json.Marshal(map[string]any{
			"stats":  stats,
			"errors": errs,
			"suites": []any{map[string]any{"title": "surfaces.visual.ts", "specs": []any{}, "suites": []any{
				map[string]any{"title": "visual baseline", "specs": specs},
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	pass := capture("chat (desktop)", "desktop-dark", "expected", "")

	type outcome struct {
		rc               int
		out, log, report string
	}
	run := func(t *testing.T, args []string, taskRC int, rep string, installed bool) outcome {
		t.Helper()
		dir := t.TempDir()
		git := scratchGit(t, dir)
		scratchWrite(t, dir, "scripts/visual-gate.sh", string(gateScript))
		scratchWrite(t, dir, "frontends/web/scripts/visual-docker.sh",
			"#!/usr/bin/env bash\n[ \"$1\" = --pull ] || exit 9\necho \"docker $*\" >>\"$STUB_LOG\"\n")
		scratchWrite(t, dir, "frontends/web/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
		git("add", "-A")
		git("commit", "-qm", "base")
		if installed {
			scratchWrite(t, dir, "frontends/web/node_modules/.modules.yaml", "x\n")
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(dir, "frontends/web/node_modules/.modules.yaml"), later, later); err != nil {
				t.Fatal(err)
			}
		}
		stubs := filepath.Join(t.TempDir(), "bin")
		scratchWrite(t, stubs, "task", "#!/usr/bin/env bash\necho \"task $* VISUAL_JSON=$VISUAL_JSON\" >>\"$STUB_LOG\"\n"+
			"if [ -n \"$STUB_REPORT\" ]; then printf '%s' \"$STUB_REPORT\" >\"$VISUAL_JSON\"; fi\nexit \"$STUB_RC\"\n")
		scratchWrite(t, stubs, "pnpm", "#!/usr/bin/env bash\necho \"pnpm $*\" >>\"$STUB_LOG\"\n")
		logFile := filepath.Join(dir, "stub.log")
		summary := filepath.Join(dir, "summary.md")
		cmd := exec.Command("bash", append([]string{"scripts/visual-gate.sh", "--summary", summary}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
			"STUB_LOG="+logFile, "STUB_REPORT="+rep, "STUB_RC="+strconv.Itoa(taskRC), "GITHUB_STEP_SUMMARY=")
		out, err := cmd.CombinedOutput()
		o := outcome{out: string(out)}
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run visual-gate.sh: %v", err)
			}
			o.rc = ee.ExitCode()
		}
		b, _ := os.ReadFile(logFile)
		o.log = string(b)
		b, _ = os.ReadFile(summary)
		o.report = string(b)
		// Compare only, every surface, on the scratch checkout's report path.
		if o.rc != 2 {
			want := "task test:web:visual -- --update-snapshots=none VISUAL_JSON=" + filepath.Join(dir, "frontends/web/visual-results.json")
			if !strings.Contains(o.log, want+"\n") {
				t.Errorf("the gate did not run %q:\n%s", want, o.log)
			}
		}
		return o
	}

	t.Run("every capture matches: a pass", func(t *testing.T) {
		t.Parallel()
		o := run(t, nil, 0, report(map[string]int{"expected": 2}, nil, pass, capture("projects (mobile)", "mobile-dark", "expected", "")), true)
		if o.rc != 0 || !strings.Contains(o.report, "all 2 captures match the committed baselines") {
			t.Fatalf("rc=%d, want 0 and the pass report:\n%s\n%s", o.rc, o.out, o.report)
		}
		if strings.Contains(o.log, "pnpm ") {
			t.Errorf("node_modules is newer than the lockfile, yet the gate installed:\n%s", o.log)
		}
		if !strings.Contains(o.log, "docker --pull") {
			t.Errorf("the gate did not pull the image beside the build:\n%s", o.log)
		}
	})
	t.Run("a stale node_modules is installed frozen", func(t *testing.T) {
		t.Parallel()
		o := run(t, nil, 0, report(map[string]int{"expected": 1}, nil, pass), false)
		if o.rc != 0 || !strings.Contains(o.log, "pnpm install --dir frontends/web --frozen-lockfile") {
			t.Fatalf("rc=%d, want a pass after a frozen install:\n%s\n%s", o.rc, o.out, o.log)
		}
	})
	t.Run("captures that differ are named with the re-capture", func(t *testing.T) {
		t.Parallel()
		rep := report(map[string]int{"expected": 1, "unexpected": 3}, nil, pass,
			capture("shell-usage-billing (desktop)", "desktop-light", "unexpected",
				"\x1b[31mError: expect(page).toHaveScreenshot(expected) failed\x1b[39m\n\n  1234 pixels (ratio 0.01 of all image pixels) are different."),
			capture("chat (mobile)", "mobile-dark", "unexpected", "Error: expect(page).toHaveScreenshot(expected) failed\n\n  Expected an image 390px by 844px, received 390px by 1200px. 500 pixels..."),
			capture("projects (desktop)", "desktop-dark", "unexpected", "Test timeout of 90000ms exceeded."))
		o := run(t, nil, 1, rep, true)
		if o.rc != 1 {
			t.Fatalf("rc=%d, want 1 (a mismatch):\n%s", o.rc, o.out)
		}
		for _, want := range []string{
			"3 of 4 captures differ from the committed baselines",
			"| `shell-usage-billing (desktop)` | desktop-light | 1234 pixels differ (ratio 0.01) |",
			"| `chat (mobile)` | mobile-dark | Expected an image 390px by 844px, received 390px by 1200px. 500 pixels... |",
			"| `projects (desktop)` | desktop-dark | Test timeout of 90000ms exceeded. |",
			"git fetch origin && git rebase origin/main",
			"task test:web:visual -- --update-snapshots",
			"flake:removed",
			"visual-results",
		} {
			if !strings.Contains(o.report, want) || !strings.Contains(o.out, want) {
				t.Errorf("the report (and the log) must say %q:\n%s", want, o.report)
			}
		}
		if strings.Contains(o.report, "\x1b") {
			t.Errorf("the report carries terminal colour codes:\n%q", o.report)
		}
	})
	incomplete := []struct {
		name   string
		rc     int
		report string
		why    string
	}{
		{"no report (the build or docker failed)", 1, "", "wrote no Playwright report"},
		{"an unreadable report", 1, "not json", "is unreadable"},
		{"errors outside any test", 1, report(map[string]int{}, []any{map[string]any{"message": "Error: Timed out waiting 120000ms from config.webServer.\nmore"}}), "Playwright failed outside any test: Error: Timed out waiting 120000ms from config.webServer."},
		{"a report that ran no capture", 0, report(map[string]int{"skipped": 166}, nil), "ran no capture"},
		{"a red task with no failed capture", 1, report(map[string]int{"expected": 1}, nil, pass), "names no failed capture"},
	}
	for _, c := range incomplete {
		t.Run(c.name+" is never a pass", func(t *testing.T) {
			t.Parallel()
			o := run(t, nil, c.rc, c.report, true)
			if o.rc != 3 || !strings.Contains(o.out, c.why) || !strings.Contains(o.report, "the lane did not complete") {
				t.Fatalf("rc=%d, want 3 saying %q:\n%s\n%s", o.rc, c.why, o.out, o.report)
			}
		})
	}
	t.Run("Playwright arguments are refused", func(t *testing.T) {
		t.Parallel()
		o := run(t, []string{"--update-snapshots"}, 0, report(map[string]int{"expected": 1}, nil, pass), true)
		if o.rc != 2 || strings.Contains(o.log, "task ") {
			t.Fatalf("rc=%d, want 2 before running anything:\n%s\n%s", o.rc, o.out, o.log)
		}
	})
}

// TestVisualLaneWiring: the gate's JSON report reaches Playwright inside the
// image, the visual config writes it only when asked, and the task builds with
// the version the runner requires.
func TestVisualLaneWiring(t *testing.T) {
	docker := string(readRepo(t, "frontends/web/scripts/visual-docker.sh"))
	if !strings.Contains(docker, "-e VISUAL_JSON") {
		t.Error("visual-docker.sh does not pass VISUAL_JSON into the container: the gate would read no report")
	}
	if !strings.Contains(docker, `"${1:-}" = "--pull"`) {
		t.Error("visual-docker.sh lost --pull, which scripts/visual-gate.sh runs beside the build")
	}
	cfg := string(readRepo(t, "frontends/web/playwright.visual.config.ts"))
	if !strings.Contains(cfg, "process.env.VISUAL_JSON ? [['json', { outputFile: process.env.VISUAL_JSON }]") {
		t.Error("playwright.visual.config.ts no longer adds the json reporter at VISUAL_JSON")
	}
	if !strings.Contains(cfg, "retries: 0") {
		t.Error("playwright.visual.config.ts must not retry: a capture that passes on retry is a flake to delete")
	}
	task := string(readRepo(t, "Taskfile.yml"))
	if !strings.Contains(task, "RELEASE_VERSION=0.0.0-visual npm run build:alpha") || !strings.Contains(task, "./scripts/visual-docker.sh {{.CLI_ARGS}}") {
		t.Error("task test:web:visual no longer builds with the pinned version and hands its arguments to visual-docker.sh")
	}
}

// visualNightly is the part of visual-nightly.yml these tests read.
type visualNightly struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress *bool  `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]*struct {
		Permissions map[string]any `yaml:"permissions"`
		Steps       []mqStep       `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestVisualNightlyWorkflow: on a schedule (and dispatch) only, on main, one
// run at a time; the gate step's exit decides pass, mismatch or error; only a
// pass or a mismatch touches the issue, and with the only write permission;
// anything but a pass fails the run.
func TestVisualNightlyWorkflow(t *testing.T) {
	var wf visualNightly
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/visual-nightly.yml"), &wf); err != nil {
		t.Fatalf("parse visual-nightly.yml: %v", err)
	}
	if _, ok := wf.On["schedule"]; !ok || len(wf.On) != 2 {
		t.Errorf("visual-nightly.yml runs on %v, want schedule and workflow_dispatch", wf.On)
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Errorf("visual-nightly.yml has no workflow_dispatch: %v", wf.On)
	}
	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions %v, want contents: read alone", wf.Permissions)
	}
	if wf.Concurrency.Group == "" || wf.Concurrency.CancelInProgress == nil || *wf.Concurrency.CancelInProgress {
		t.Error("two runs must not both open an issue: one concurrency group, never cancelling a run mid-write")
	}
	j := wf.Jobs["visual"]
	if j == nil || len(wf.Jobs) != 1 {
		t.Fatalf("visual-nightly.yml needs the one job visual, has %v", sortedKeys(wf.Jobs))
	}
	if len(j.Permissions) != 2 || j.Permissions["contents"] != "read" || j.Permissions["issues"] != "write" {
		t.Errorf("job permissions %v, want contents: read and issues: write alone", j.Permissions)
	}
	var gate, issue, fail *mqStep
	for i := range j.Steps {
		s := &j.Steps[i]
		switch {
		case strings.HasPrefix(s.Uses, "actions/checkout@"):
			if s.With["ref"] != "main" {
				t.Errorf("checkout ref=%v, want main", s.With["ref"])
			}
		case s.ID == "gate":
			gate = s
		case strings.Contains(s.Run, ".github/scripts/visual-stale-issue.sh"):
			issue = s
		case strings.HasPrefix(s.Name, "Fail") && strings.Contains(s.Run, "exit 1"):
			fail = s
		}
	}
	if gate == nil || issue == nil || fail == nil {
		t.Fatalf("visual-nightly.yml needs the gate (%v), the issue (%v) and the failing (%v) steps", gate != nil, issue != nil, fail != nil)
	}
	if !strings.Contains(gate.Run, `scripts/visual-gate.sh --summary "${RUNNER_TEMP}/visual.md"`) {
		t.Errorf("the gate step must run scripts/visual-gate.sh and write its report for the issue:\n%s", gate.Run)
	}
	for _, s := range []string{"0) verdict=pass", "1) verdict=mismatch", "*) verdict=error"} {
		if !strings.Contains(gate.Run, s) {
			t.Errorf("the gate step does not map %q", s)
		}
	}
	if issue.If != "${{ !cancelled() && (steps.gate.outputs.verdict == 'pass' || steps.gate.outputs.verdict == 'mismatch') }}" {
		t.Errorf("the issue step if=%q: only a pass or a mismatch may touch the issue", issue.If)
	}
	if issue.Env["GH_TOKEN"] != "${{ github.token }}" || issue.Env["VERDICT"] != "${{ steps.gate.outputs.verdict }}" || issue.Env["REPORT"] != "${{ runner.temp }}/visual.md" {
		t.Errorf("the issue step's env %v", issue.Env)
	}
	if fail.If != "${{ !cancelled() && steps.gate.outputs.verdict != 'pass' }}" {
		t.Errorf("the failing step if=%q: a mismatch and an error must both fail the run", fail.If)
	}
}

// TestVisualStaleIssueScript runs .github/scripts/visual-stale-issue.sh
// against a stub gh: a mismatch opens the one issue, reopens and comments on
// a closed one, and says a run once; a pass closes an open issue and touches
// nothing else; any other verdict is refused before gh is called.
func TestVisualStaleIssueScript(t *testing.T) {
	testkit.Slow(t, "runs .github/scripts/visual-stale-issue.sh in bash with a stub gh",
		testkit.Inputs(".github/scripts/visual-stale-issue.sh"))
	requireShellTools(t)
	script := filepath.Join(repoRoot, ".github", "scripts", "visual-stale-issue.sh")
	const runURL = "https://github.com/reliant-labs/reliant/actions/runs/42"

	run := func(t *testing.T, verdict, latest, seen string) (int, []string) {
		t.Helper()
		dir := t.TempDir()
		logFile := filepath.Join(dir, "gh.log")
		scratchWrite(t, dir, "bin/gh", "#!/usr/bin/env bash\necho \"$*\" >>\"$GH_LOG\"\n"+
			"case \"$1 $2\" in\n  \"issue list\") [ -z \"$LATEST\" ] || echo \"$LATEST\" ;;\n  \"issue view\") echo \"$SEEN\" ;;\nesac\n")
		scratchWrite(t, dir, "report.md", "### visual: 1 of 166 captures differ\n")
		cmd := exec.Command("bash", script)
		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GH_LOG="+logFile, "LATEST="+latest, "SEEN="+seen, "VERDICT="+verdict, "RUN_URL="+runURL, "SHA=abc123", "REPORT="+filepath.Join(dir, "report.md"))
		out, err := cmd.CombinedOutput()
		rc := 0
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run: %v", err)
			}
			rc = ee.ExitCode()
		}
		b, _ := os.ReadFile(logFile)
		var calls []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" && !strings.HasPrefix(l, "label create") && !strings.HasPrefix(l, "issue list") && !strings.HasPrefix(l, "issue view") {
				calls = append(calls, strings.Fields(l)[0]+" "+strings.Fields(l)[1])
			}
		}
		if rc != 0 && verdict != "bogus" {
			t.Fatalf("rc=%d:\n%s", rc, out)
		}
		return rc, calls
	}
	cases := []struct {
		name, verdict, latest, seen string
		want                        []string
	}{
		{"a first mismatch opens the issue", "mismatch", "", "", []string{"issue create"}},
		{"a mismatch reopens a closed issue and reports there", "mismatch", "7 CLOSED", "old", []string{"issue reopen", "issue comment"}},
		{"a new mismatch comments on the open issue", "mismatch", "7 OPEN", "old run", []string{"issue comment"}},
		{"a reported run is said once", "mismatch", "7 OPEN", "Run: " + runURL, nil},
		{"a pass closes the open issue", "pass", "7 OPEN", "", []string{"issue close"}},
		{"a pass with the issue closed does nothing", "pass", "7 CLOSED", "", nil},
		{"a pass with no issue does nothing", "pass", "", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, calls := run(t, c.verdict, c.latest, c.seen)
			if !slices.Equal(calls, c.want) {
				t.Errorf("gh writes %v, want %v", calls, c.want)
			}
		})
	}
	t.Run("an unknown verdict is refused", func(t *testing.T) {
		t.Parallel()
		rc, calls := run(t, "bogus", "7 OPEN", "")
		if rc != 2 || len(calls) != 0 {
			t.Errorf("rc=%d writes=%v, want 2 and no gh call", rc, calls)
		}
	})
}

// scratchGit initialises a repository in dir and returns a git runner for it.
func scratchGit(t *testing.T, dir string) func(args ...string) string {
	t.Helper()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	return git
}

// scratchWrite writes an executable file at dir/rel.
func scratchWrite(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
