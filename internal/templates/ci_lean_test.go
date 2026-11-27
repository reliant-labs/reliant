package templates

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The scaffolded CI is a thin wrapper around a local gate, and it is lean.
//
// WHY. GitHub bills every job rounded UP to the whole minute, and a runner is
// a fresh machine: each job repeats checkout, toolchain setup, cache restore
// and the forge install before doing any work. The scaffold used to be nine
// jobs (lint, test, build, verify-generated, deployability, vuln-scan,
// license-check, docker-build, e2e) and cost control-plane — the one project
// generated from it — about $850/month at its October pace, until the org's
// Actions budget blocked its CI entirely. Most of those minutes were setup, not
// checking: the cost grew with the NUMBER of checks rather than with what they
// do.
//
// So the static and build checks share ONE job behind ONE setup, and every job
// calls the same `task` a developer runs locally (`task ci` runs them all), so
// "CI did not run" never has to mean "unverified". These tests hold that shape.

type leanStep struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
}

type leanJob struct {
	Name           string     `yaml:"name"`
	TimeoutMinutes int        `yaml:"timeout-minutes"`
	Needs          any        `yaml:"needs"`
	If             string     `yaml:"if"`
	Steps          []leanStep `yaml:"steps"`
}

func leanJobs(t *testing.T, doc []byte) map[string]leanJob {
	t.Helper()
	var wf struct {
		Jobs map[string]leanJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(doc, &wf); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, doc)
	}
	return wf.Jobs
}

func renderedCI(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for name, doc := range renderedWorkflows(t) {
		if strings.HasPrefix(name, "ci ") {
			out[name] = doc
		}
	}
	if len(out) == 0 {
		t.Fatal("renderedWorkflows produced no ci.yml shapes")
	}
	return out
}

// Every job has a timeout. A hung step otherwise burns the runner for GitHub's
// 6-hour default, billed by the minute: the cheapest insurance in the file.
func TestCIWorkflows_EveryJobHasATimeout(t *testing.T) {
	for name, doc := range renderedWorkflows(t) {
		for id, job := range leanJobs(t, doc) {
			// release.yml / deploy.yml jobs that only call a reusable workflow
			// have no steps; there is nothing to time out.
			if len(job.Steps) == 0 {
				continue
			}
			if job.TimeoutMinutes <= 0 {
				t.Errorf("%s: job %q has no timeout-minutes — a hung step bills the runner for up to 6 hours", name, id)
			}
		}
	}
}

// installsForge / checkout / setupGo are the per-job setup steps.
var (
	checkoutUses = regexp.MustCompile(`^actions/checkout@`)
	setupGoUses  = regexp.MustCompile(`^actions/setup-go@`)
)

func installsForge(s leanStep) bool { return s.Name == "Install forge" }

// A job does its setup once. Within one job, checkout, setup-go and the forge
// install each appear at most once; across the workflow, forge is installed
// only where the job runs forge. The cost the consolidation removed was seven
// copies of this block; this fails if they grow back.
func TestCIWorkflows_OneSetupPerJob(t *testing.T) {
	for name, doc := range renderedCI(t) {
		jobs := leanJobs(t, doc)
		for id, job := range jobs {
			var checkouts, goSetups, forgeInstalls int
			for _, s := range job.Steps {
				switch {
				case checkoutUses.MatchString(s.Uses):
					checkouts++
				case setupGoUses.MatchString(s.Uses):
					goSetups++
				case installsForge(s):
					forgeInstalls++
				}
			}
			if checkouts > 1 || goSetups > 1 || forgeInstalls > 1 {
				t.Errorf("%s: job %q repeats its setup (checkout x%d, setup-go x%d, install forge x%d) — one of each per job",
					name, id, checkouts, goSetups, forgeInstalls)
			}
		}

		// The static and build checks are ONE job. The job ids the old layout
		// used for them must not come back as separate runners.
		for _, folded := range []string{"lint", "build", "verify-generated", "deployability", "vuln-scan", "license-check"} {
			if _, ok := jobs[folded]; ok {
				t.Errorf("%s: job %q is a check; it belongs in the gate behind `checks`, not on its own runner", name, folded)
			}
		}

		// Forge is installed in the checks job and no more than once more (the
		// k3d e2e job, which drives a cluster through forge). A third copy is
		// a check that grew its own runner.
		installs := 0
		for _, job := range jobs {
			for _, s := range job.Steps {
				if installsForge(s) {
					installs++
				}
			}
		}
		if installs > 2 {
			t.Errorf("%s: forge is installed in %d jobs; want the checks job and at most the k3d e2e job", name, installs)
		}
	}
}

// Each CI lane calls the same `task` that `task ci` runs locally, and spells no
// check of its own. This is what keeps the local gate and CI from drifting: a
// `go test` re-spelled in the workflow is how CI once skipped the frontend lane
// entirely, so a broken frontend test could merge green.
func TestCIWorkflows_EachLaneCallsTheTaskLocalCIRuns(t *testing.T) {
	lanes := map[string]string{
		"checks":           "task ci:checks",
		"test":             "task test",
		"integration-test": "task test:integration",
	}
	for name, doc := range renderedCI(t) {
		jobs := leanJobs(t, doc)
		for id, want := range lanes {
			job, ok := jobs[id]
			if !ok {
				t.Errorf("%s: no %q job", name, id)
				continue
			}
			var runs []string
			for _, s := range job.Steps {
				if s.Run != "" {
					runs = append(runs, strings.TrimSpace(s.Run))
				}
			}
			found := false
			for _, r := range runs {
				// `task test GOTESTRACE=` is the same task with ci.test.race
				// carried through; match on the task name.
				if r == want || strings.HasPrefix(r, want+" ") {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: job %q never runs %q (its runs: %q) — a lane must call the task `task ci` runs, so CI and the local gate are one definition", name, id, want, runs)
			}
			// No lane re-spells a check the gate owns.
			for _, r := range runs {
				for _, own := range []string{"go test", "go build", "golangci-lint run", "buf lint", "govulncheck", "go-licenses check", "forge ci vuln-scan", "forge ci validate-kcl", "forge ci verify-generated", "forge ci migration-safety", "forge doctor"} {
					if strings.Contains(r, own) {
						t.Errorf("%s: job %q runs %q itself; that check is the gate's (`task ci` runs it too), so CI would drift from the local verdict", name, id, own)
					}
				}
			}
		}
	}
}

// The task every lane calls must exist, with the names the local gate is
// documented to have, in every Taskfile kind.
func TestTaskfiles_DeclareTheCILanes(t *testing.T) {
	for _, tmpl := range []string{"Taskfile.yml.tmpl", "Taskfile.cli.yml.tmpl", "Taskfile.library.yml.tmpl"} {
		rendered := renderProject(t, tmpl, projectData())
		var tf struct {
			Tasks map[string]struct {
				Cmds []any `yaml:"cmds"`
			} `yaml:"tasks"`
		}
		if err := yaml.Unmarshal(rendered, &tf); err != nil {
			t.Fatalf("%s: %v", tmpl, err)
		}
		for _, task := range []string{"ci", "ci:checks", "test", "test:integration"} {
			if _, ok := tf.Tasks[task]; !ok {
				t.Errorf("%s declares no %q task — the CI workflow and `task ci` both call it", tmpl, task)
			}
		}
		var all strings.Builder
		for _, name := range []string{"ci", "ci:checks"} {
			for _, c := range tf.Tasks[name].Cmds {
				switch v := c.(type) {
				case string:
					all.WriteString(v)
				case map[string]any:
					if s, ok := v["cmd"].(string); ok {
						all.WriteString(s)
					}
				}
			}
		}
		// {{.FORGE}}: the pinned forge (taskfile_pinned_forge_test.go).
		for _, want := range []string{"{{.FORGE}} ci gate all", "{{.FORGE}} ci gate checks"} {
			if !strings.Contains(all.String(), want) {
				t.Errorf("%s: ci / ci:checks never run %q", tmpl, want)
			}
		}
	}
}

// The gate installs the tools CI installs at the versions CI installs them, so
// `task ci` and the workflow judge the same code with the same linters. The
// workflow's pins and the gate's come from one constant; this fails if the
// workflow stops using it.
func TestGateToolPinsMatchTheScaffoldedWorkflow(t *testing.T) {
	data := fullCIData()
	data.RunsBufGenerate = true
	data.LintBuf = true
	doc, err := CITemplates("github").Render("ci.yml.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	for _, want := range []string{
		"version: " + GolangciLintVersion,
		`version: "` + BufVersion + `"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered ci.yml does not pin %q — the gate pins that version, so CI and `task ci` would lint with different tools", want)
		}
	}
	for name, v := range map[string]string{"GolangciLintVersion": GolangciLintVersion, "GovulncheckVersion": GovulncheckVersion, "GoLicensesVersion": GoLicensesVersion} {
		if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(v) {
			t.Errorf("%s = %q, want an explicit vX.Y.Z — `latest` makes the gate non-reproducible", name, v)
		}
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(BufVersion) {
		t.Errorf("BufVersion = %q, want an explicit X.Y.Z", BufVersion)
	}
}

// Main pushes share one group that never cancels; PRs cancel per ref. The
// per-ref group cancels a superseded PR run (saving the runner) and cannot
// starve main, because main's group never cancels. TestCIWorkflows_
// CancelInProgressOnlyOnPullRequests holds the expression; this holds that the
// group is the shared one.
func TestCIWorkflows_ConcurrencyIsBoundedOnMain(t *testing.T) {
	for name, doc := range renderedCI(t) {
		got := workflowConcurrency(t, name, doc)
		group, _ := got["group"].(string)
		if !strings.Contains(group, "github.ref") {
			t.Errorf("%s: concurrency group %q is not keyed on github.ref — PRs must cancel per ref, and main must share one group", name, group)
		}
		if got["cancel-in-progress"] != prOnlyCancel {
			t.Errorf("%s: cancel-in-progress = %v, want %s", name, got["cancel-in-progress"], prOnlyCancel)
		}
	}
}

// Heavy jobs hang off `changes`, so a docs-only change pays for one tiny job.
func TestCIWorkflows_HeavyJobsAreGatedOnChanges(t *testing.T) {
	for name, doc := range renderedCI(t) {
		jobs := leanJobs(t, doc)
		if _, ok := jobs["changes"]; !ok {
			t.Errorf("%s: no `changes` job", name)
		}
		var ungated []string
		for _, id := range []string{"checks", "test", "integration-test"} {
			job, ok := jobs[id]
			if !ok {
				continue
			}
			if !strings.Contains(job.If, "needs.changes.outputs") {
				ungated = append(ungated, id)
			}
		}
		sort.Strings(ungated)
		if len(ungated) > 0 {
			t.Errorf("%s: jobs %v do not read needs.changes.outputs in their `if:` — a docs-only change would still pay for them", name, ungated)
		}
	}
}
