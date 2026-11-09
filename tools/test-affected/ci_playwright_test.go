// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// requiredPlaywrightProjects are the projects the responsive-UI work must run
// in CI (frontends/web/playwright.config.ts): desktop Chromium, and the touch
// Chromium / WebKit phone / WebKit tablet projects.
var requiredPlaywrightProjects = []string{
	"chromium", "responsive-chromium-touch", "responsive-webkit-phone", "responsive-webkit-tablet",
}

type ciWorkflow struct {
	Jobs map[string]*ciJob `yaml:"jobs"`
}

type ciJob struct {
	Needs           any    `yaml:"needs"`
	If              string `yaml:"if"`
	ContinueOnError any    `yaml:"continue-on-error"`
	Strategy        struct {
		FailFast any            `yaml:"fail-fast"`
		Matrix   map[string]any `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []ciStep `yaml:"steps"`
}

type ciStep struct {
	Name            string         `yaml:"name"`
	ID              string         `yaml:"id"`
	If              string         `yaml:"if"`
	Uses            string         `yaml:"uses"`
	Run             string         `yaml:"run"`
	ContinueOnError any            `yaml:"continue-on-error"`
	With            map[string]any `yaml:"with"`
}

func (j *ciJob) needs() []string {
	switch n := j.Needs.(type) {
	case string:
		return []string{n}
	case []any:
		var out []string
		for _, v := range n {
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	return nil
}

func loadCIWorkflow(t *testing.T) *ciWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf ciWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	return &wf
}

func (wf *ciWorkflow) job(t *testing.T, id string) *ciJob {
	t.Helper()
	j := wf.Jobs[id]
	if j == nil {
		t.Fatalf("ci.yml has no job %q", id)
	}
	return j
}

func (j *ciJob) step(t *testing.T, job, name string) (int, ciStep) {
	t.Helper()
	for i, s := range j.Steps {
		if s.Name == name {
			return i, s
		}
	}
	t.Fatalf("ci.yml job %q has no step named %q", job, name)
	return 0, ciStep{}
}

// matrixSuites reads strategy.matrix.suite and rejects every other matrix
// shape: an include/exclude or a second axis could add, drop or rename a leg
// without this test noticing.
func matrixSuites(t *testing.T, j *ciJob) []string {
	t.Helper()
	for k := range j.Strategy.Matrix {
		if k != "suite" {
			t.Errorf("playwright matrix has key %q; only `suite` is understood (include/exclude/extra axes can drop or add legs unseen)", k)
		}
	}
	raw, ok := j.Strategy.Matrix["suite"].([]any)
	if !ok {
		t.Fatalf("playwright strategy.matrix.suite must be a literal list, got %#v", j.Strategy.Matrix["suite"])
	}
	var suites []string
	for _, v := range raw {
		suites = append(suites, fmt.Sprint(v))
	}
	return suites
}

func expandSuite(s, suite string) string {
	return strings.ReplaceAll(s, "${{ matrix.suite }}", suite)
}

// e2eTask is a test:web:e2e* Taskfile task reduced to what it hands the shared
// web:e2e:run task.
type e2eTask struct {
	browsers []string
	projects []string
}

func readE2ETask(t *testing.T, tf *taskfile, name string) e2eTask {
	t.Helper()
	if _, ok := tf.Tasks[name]; !ok {
		t.Fatalf("Taskfile.yml has no task %q", name)
	}
	var out e2eTask
	calls := 0
	for _, c := range tf.Tasks[name].Cmds {
		m, ok := c.(map[string]any)
		if !ok || m["task"] != "web:e2e:run" {
			continue
		}
		calls++
		vars, _ := m["vars"].(map[string]any)
		out.browsers = strings.Fields(fmt.Sprint(vars["BROWSERS"]))
		for _, f := range strings.Fields(fmt.Sprint(vars["PROJECTS"])) {
			p, ok := strings.CutPrefix(f, "--project=")
			if !ok {
				t.Errorf("%s: PROJECTS holds %q, want only --project=<name> flags", name, f)
				continue
			}
			out.projects = append(out.projects, p)
		}
	}
	if calls != 1 {
		t.Fatalf("%s must call web:e2e:run exactly once, calls it %d times", name, calls)
	}
	slices.Sort(out.projects)
	return out
}

// TestPlaywrightPipelineSplitContract pins the `playwright` job's
// desktop/responsive matrix to the Taskfile tasks and Playwright projects it
// stands for: the split must not silently drop a required project, run one
// twice, or let two legs share a cache entry or artifact.
func TestPlaywrightPipelineSplitContract(t *testing.T) {
	wf := loadCIWorkflow(t)
	tf := parseTaskfile(t, filepath.Join(repoRoot, "Taskfile.yml"))
	pw := wf.job(t, "playwright")

	suites := matrixSuites(t, pw)
	if want := []string{"desktop", "responsive"}; !slices.Equal(suites, want) {
		t.Fatalf("playwright matrix suites = %v, want %v", suites, want)
	}

	// A leg that may fail without failing the job turns a red suite into a
	// green `needs.playwright.result`.
	if pw.ContinueOnError != nil {
		t.Errorf("playwright job sets continue-on-error=%v: a failing leg would not fail the job", pw.ContinueOnError)
	}
	for _, s := range pw.Steps {
		if s.ContinueOnError != nil {
			t.Errorf("playwright step %q sets continue-on-error=%v: a failing suite would pass", s.Name, s.ContinueOnError)
		}
	}
	// Every leg must run to completion so one suite's failure does not hide
	// the other's (the job result is still failure).
	if pw.Strategy.FailFast != false {
		t.Errorf("playwright strategy.fail-fast = %v, want false", pw.Strategy.FailFast)
	}

	// The step that runs the suite: one `task test:web:e2e:${{ matrix.suite }}`.
	var runs []ciStep
	for _, s := range pw.Steps {
		if strings.Contains(s.Run, "task test:web:e2e") {
			runs = append(runs, s)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("playwright must have exactly one step running a test:web:e2e task, has %d", len(runs))
	}
	runTmpl := regexp.MustCompile(`task (test:web:e2e:\$\{\{ matrix\.suite \}\})`).FindStringSubmatch(runs[0].Run)
	if runTmpl == nil {
		t.Fatalf("playwright must run `task test:web:e2e:${{ matrix.suite }}`, step runs %q", runs[0].Run)
	}

	// Every leg maps to an existing task, and together the legs cover the
	// required projects, each exactly once.
	var union []string
	byTask := map[string]e2eTask{}
	for _, suite := range suites {
		name := expandSuite(runTmpl[1], suite)
		task := readE2ETask(t, tf, name)
		byTask[suite] = task
		union = append(union, task.projects...)
	}
	slices.Sort(union)
	want := slices.Clone(requiredPlaywrightProjects)
	slices.Sort(want)
	if !slices.Equal(union, want) {
		t.Errorf("projects run by the matrix suites = %v, want exactly %v (each once)", union, want)
	}
	if !slices.Contains(byTask["desktop"].projects, "chromium") {
		t.Errorf("the desktop suite must run the chromium project, runs %v", byTask["desktop"].projects)
	}
	for _, p := range byTask["responsive"].projects {
		if !strings.HasPrefix(p, "responsive-") {
			t.Errorf("the responsive suite runs non-responsive project %q", p)
		}
	}

	// Each suite installs the browsers its projects use, so the responsive
	// suite is not green by skipping WebKit.
	if !slices.Contains(byTask["responsive"].browsers, "webkit") || !slices.Contains(byTask["responsive"].browsers, "chromium") {
		t.Errorf("test:web:e2e:responsive installs %v, want chromium and webkit", byTask["responsive"].browsers)
	}
	if !slices.Contains(byTask["desktop"].browsers, "chromium") {
		t.Errorf("test:web:e2e:desktop installs %v, want chromium", byTask["desktop"].browsers)
	}

	// The aggregate task (what a developer runs) is the same project set.
	all := readE2ETask(t, tf, "test:web:e2e")
	if !slices.Equal(all.projects, want) {
		t.Errorf("test:web:e2e projects = %v, want %v", all.projects, want)
	}
	if !slices.Contains(all.browsers, "webkit") || !slices.Contains(all.browsers, "chromium") {
		t.Errorf("test:web:e2e installs %v, want chromium and webkit", all.browsers)
	}

	// The shared task actually uses what the wrappers pass.
	var runCmds strings.Builder
	for _, c := range tf.commands("web:e2e:run") {
		runCmds.WriteString(c)
		runCmds.WriteByte('\n')
	}
	for _, frag := range []string{"playwright install {{.BROWSERS}}", "{{.PROJECTS}}"} {
		if !strings.Contains(runCmds.String(), frag) {
			t.Errorf("web:e2e:run no longer uses %s: the suites' browsers/projects would be ignored", frag)
		}
	}

	// The projects exist in the Playwright config.
	cfg, err := os.ReadFile(filepath.Join(repoRoot, "frontends", "web", "playwright.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range requiredPlaywrightProjects {
		if !regexp.MustCompile(`name:\s*'` + regexp.QuoteMeta(p) + `'`).Match(cfg) {
			t.Errorf("frontends/web/playwright.config.ts defines no project %q", p)
		}
	}

	// Cache keys and artifact names carry the suite, so no two legs share one,
	// and no other job uploads under a leg's artifact name.
	cacheKeys := map[string]string{}
	artifacts := map[string]string{}
	sawRestore, sawUpload := false, false
	for _, s := range pw.Steps {
		switch {
		case strings.HasPrefix(s.Uses, "actions/cache/restore@"):
			sawRestore = true
			key := fmt.Sprint(s.With["key"])
			if !strings.Contains(key, "${{ matrix.suite }}") {
				t.Errorf("Playwright browser cache key %q does not include matrix.suite: the legs would share (and clobber) one entry", key)
			}
			for _, suite := range suites {
				k := expandSuite(key, suite)
				if prev, dup := cacheKeys[k]; dup {
					t.Errorf("suites %s and %s share cache key %q", prev, suite, k)
				}
				cacheKeys[k] = suite
			}
		case strings.HasPrefix(s.Uses, "actions/cache/save@"):
			// Saves under the restore step's primary key; a literal key must
			// carry the suite too.
			if key, ok := s.With["key"].(string); ok && !strings.Contains(key, "steps.") && !strings.Contains(key, "${{ matrix.suite }}") {
				t.Errorf("Playwright cache save key %q does not include matrix.suite", key)
			}
		case strings.HasPrefix(s.Uses, "actions/upload-artifact@"):
			sawUpload = true
			name := fmt.Sprint(s.With["name"])
			if !strings.Contains(name, "${{ matrix.suite }}") {
				t.Errorf("Playwright artifact name %q does not include matrix.suite: upload-artifact@v4 fails on a duplicate name", name)
			}
			for _, suite := range suites {
				n := expandSuite(name, suite)
				if prev, dup := artifacts[n]; dup {
					t.Errorf("suites %s and %s share artifact name %q", prev, suite, n)
				}
				artifacts[n] = suite
			}
		}
	}
	if !sawRestore {
		t.Error("playwright job has no actions/cache/restore step for the browsers")
	}
	if !sawUpload {
		t.Error("playwright job has no actions/upload-artifact step for the report")
	}
	for id, j := range wf.Jobs {
		if id == "playwright" {
			continue
		}
		for _, s := range j.Steps {
			if !strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
				continue
			}
			if suite, clash := artifacts[fmt.Sprint(s.With["name"])]; clash {
				t.Errorf("job %q uploads artifact %q, which is the %s leg's report", id, s.With["name"], suite)
			}
		}
	}
}

// ciShell runs a workflow step's script exactly as the runner would (bash, with
// the step's env), and reports whether it exited zero. Skips without jq: the
// scripts need it, and the runner images have it.
func ciShell(t *testing.T, script string, env map[string]string) (bool, string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, string(out)
	}
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("run step script: %v", err)
	}
	return false, string(out)
}

func needsJSON(results map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(results) {
		parts = append(parts, fmt.Sprintf("%q:{\"result\":%q,\"outputs\":{}}", k, results[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func requireShellTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s not on PATH in CI: the aggregation contract would pass vacuously", bin)
			}
			t.Skipf("%s not on PATH", bin)
		}
	}
}

// TestPlaywrightFailsClosedInAggregation runs the real `release-record` and
// `ci-result` gate scripts from ci.yml against synthetic `needs` documents. A
// matrix job's `needs.playwright.result` is `failure` when any leg fails, and
// `cancelled` when one is cancelled, so the contract is on that one result:
//
//   - the release record is written only on `success` (a skipped Playwright on
//     a push is a broken `if:`, never a pass);
//   - `CI result` fails on `failure`/`cancelled`; `skipped` is accepted only
//     where a path-selected lane may legitimately not run (a pull request),
//     never on a push, where every lane runs.
func TestPlaywrightFailsClosedInAggregation(t *testing.T) {
	requireShellTools(t)
	wf := loadCIWorkflow(t)

	if !slices.Contains(wf.job(t, "release-record").needs(), "playwright") {
		t.Error("release-record does not `needs: playwright`: the record would not wait for the suites")
	}
	if !slices.Contains(wf.job(t, "ci-result").needs(), "playwright") {
		t.Error("ci-result does not `needs: playwright`: a red suite would not fail the required check")
	}
	if got := wf.job(t, "ci-result").If; got != "always()" {
		t.Errorf("ci-result `if` = %q, want always() so a failed/cancelled needs fails it rather than skipping it", got)
	}

	// Every case below is its own bash run over a synthetic document: they
	// run in parallel, which is what keeps this test under its -short budget.
	t.Run("release record", func(t *testing.T) {
		t.Parallel()
		rr := wf.job(t, "release-record")
		guardIdx, guard := rr.step(t, "release-record", "The path lanes passed")
		assembleIdx, _ := rr.step(t, "release-record", "Assemble the record")
		if guardIdx >= assembleIdx {
			t.Fatalf("the path-lanes guard (step %d) must run before the record is assembled (step %d)", guardIdx, assembleIdx)
		}
		if guard.If != "" || guard.ContinueOnError != nil {
			t.Errorf("the path-lanes guard must be unconditional and fatal, has if=%q continue-on-error=%v", guard.If, guard.ContinueOnError)
		}
		run := func(playwright string) bool {
			ok, out := ciShell(t, guard.Run, map[string]string{"RESULTS": needsJSON(map[string]string{
				"lanes": "success", "release-gate": "success", "web-bundle": "success",
				"playwright": playwright, "electron-package": "success",
			})})
			if !ok && !strings.Contains(out, "playwright: "+playwright) {
				t.Errorf("playwright=%s: guard failed without naming the lane:\n%s", playwright, out)
			}
			return ok
		}
		if !run("success") {
			t.Fatal("harness: the guard fails with every lane green, so the cases below prove nothing")
		}
		for _, bad := range []string{"failure", "cancelled", "skipped"} {
			if run(bad) {
				t.Errorf("a playwright result of %q does not stop the release record", bad)
			}
		}
	})

	t.Run("ci result", func(t *testing.T) {
		t.Parallel()
		_, gate := wf.job(t, "ci-result").step(t, "ci-result", "Every job that ran passed")
		needs := wf.job(t, "ci-result").needs()

		// push: every lane runs; only the merge-tier jobs and the contract
		// check may be skipped.
		push := func(t *testing.T, playwright string) bool {
			t.Helper()
			r := map[string]string{}
			for _, n := range needs {
				r[n] = "success"
			}
			r["merge-gate"], r["golangci"], r["cp-contract"] = "skipped", "skipped", "skipped"
			r["playwright"] = playwright
			ok, _ := ciShell(t, gate.Run, map[string]string{"RESULTS": needsJSON(r), "EVENT_NAME": "push", "CI_RUN": "false"})
			return ok
		}
		// pull_request: the merge gate runs, and a lane the change does not
		// reach is skipped.
		pr := func(t *testing.T, ciRun, playwright string) bool {
			t.Helper()
			r := map[string]string{}
			for _, n := range needs {
				r[n] = "skipped"
			}
			r["lanes"], r["merge-gate"], r["golangci"] = "success", "success", "success"
			r["playwright"] = playwright
			ok, _ := ciShell(t, gate.Run, map[string]string{"RESULTS": needsJSON(r), "EVENT_NAME": "pull_request", "CI_RUN": ciRun})
			return ok
		}

		if !push(t, "success") {
			t.Fatal("harness: ci-result fails on a push with every lane green, so the cases below prove nothing")
		}
		if !pr(t, "true", "success") {
			t.Fatal("harness: ci-result fails on a ci:run PR with Playwright green, so the cases below prove nothing")
		}
		for _, bad := range []string{"failure", "cancelled", "skipped"} {
			t.Run("push/"+bad, func(t *testing.T) {
				t.Parallel()
				if push(t, bad) {
					t.Errorf("CI result passes a push whose playwright result is %q", bad)
				}
			})
		}
		for _, bad := range []string{"failure", "cancelled"} {
			for _, ciRun := range []string{"true", "false"} {
				t.Run("pull_request/ci:run="+ciRun+"/"+bad, func(t *testing.T) {
					t.Parallel()
					if pr(t, ciRun, bad) {
						t.Errorf("CI result passes a pull request (ci:run=%s) whose playwright result is %q", ciRun, bad)
					}
				})
			}
		}
		// The intended semantics for a path-selected lane: skipped is fine on
		// a pull request, where the lanes job decides which lanes a change
		// reaches.
		t.Run("pull_request/skipped", func(t *testing.T) {
			t.Parallel()
			if !pr(t, "true", "skipped") {
				t.Error("CI result must accept a skipped (not path-selected) playwright lane on a pull request")
			}
		})
	})
}
