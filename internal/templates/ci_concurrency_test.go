package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// prOnlyCancel is the only cancel-in-progress a workflow forge writes may
// carry besides `false`: cancel a superseded run on a pull request, and never
// on a push to the default branch, a tag, a schedule or a dispatch.
//
// A literal `true` keyed on github.ref starves main of any verdict when
// runners are scarce. Every push to main shares the group ci-refs/heads/main,
// so each merge cancels the run before it — usually while that run's jobs are
// still queued. On 2026-10-11 control-plane's 24 consecutive main runs from
// 19:13 to 21:51 UTC were all cancelled without running a step, and a
// cross-repo drift reached main unnoticed (control-plane#831). With this
// expression the main run in progress always finishes; GitHub keeps at most
// one PENDING run per group and a newer push replaces it, so a burst of merges
// costs one extra run, not one per merge.
const prOnlyCancel = "${{ github.event_name == 'pull_request' }}"

// concurrencyCancelViolations returns every concurrency block in a workflow
// — the workflow-level one and each job's — whose cancel-in-progress is
// neither false nor prOnlyCancel. An absent cancel-in-progress is GitHub's
// default, false, and is fine.
func concurrencyCancelViolations(t *testing.T, name string, content []byte) []string {
	t.Helper()
	var doc struct {
		Concurrency any `yaml:"concurrency"`
		Jobs        map[string]struct {
			Concurrency any `yaml:"concurrency"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", name, err)
	}
	var bad []string
	check := func(where string, block any) {
		m, ok := block.(map[string]any)
		if !ok {
			// Absent, or the string shorthand (`concurrency: group`), which
			// never cancels.
			return
		}
		switch v := m["cancel-in-progress"].(type) {
		case nil:
		case bool:
			if v {
				bad = append(bad, fmt.Sprintf("%s: %s cancel-in-progress: true", name, where))
			}
		case string:
			if v != prOnlyCancel {
				bad = append(bad, fmt.Sprintf("%s: %s cancel-in-progress: %q", name, where, v))
			}
		default:
			bad = append(bad, fmt.Sprintf("%s: %s cancel-in-progress: %v", name, where, v))
		}
	}
	check("workflow", doc.Concurrency)
	jobs := make([]string, 0, len(doc.Jobs))
	for id := range doc.Jobs {
		jobs = append(jobs, id)
	}
	sort.Strings(jobs)
	for _, id := range jobs {
		check("job "+id, doc.Jobs[id].Concurrency)
	}
	return bad
}

// workflowConcurrency returns a workflow's top-level concurrency block.
func workflowConcurrency(t *testing.T, name string, content []byte) map[string]any {
	t.Helper()
	var doc struct {
		Concurrency map[string]any `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", name, err)
	}
	return doc.Concurrency
}

// TestCIWorkflows_CancelInProgressOnlyOnPullRequests renders every GitHub
// Actions workflow forge scaffolds and pins the concurrency policy: a run is
// cancelled by a newer one only on a pull request. The workflows that group on
// github.ref and run on pushes to main carry exactly prOnlyCancel; none
// anywhere carries a literal true.
func TestCIWorkflows_CancelInProgressOnlyOnPullRequests(t *testing.T) {
	minimal := CIWorkflowData{ProjectName: "demo", LintGolangci: true, PermContents: "read", HasDocker: true, VerifyGenerated: true}
	workflows := []struct {
		name string
		tmpl string
		data any
		// refGrouped: the workflow-level group is keyed on github.ref, so
		// its cancel-in-progress must be the pull_request expression.
		refGrouped bool
	}{
		{"ci.yml (full)", "ci.yml.tmpl", fullCIData(), true},
		{"ci.yml (minimal)", "ci.yml.tmpl", minimal, true},
		{"e2e.yml (docker-compose)", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "docker-compose"}, true},
		{"e2e.yml (k3d)", "e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "k3d"}, true},
		{"build-images.yml", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", VulnDocker: true}, true},
		{"build-images.yml (hosted)", "build-images.yml.tmpl", BuildImagesWorkflowData{ProjectName: "demo", BuildEnv: "staging", Hosted: true}, true},
		{"proto-breaking.yml", "proto-breaking.yml.tmpl", CIWorkflowData{ProjectName: "demo", PermContents: "read"}, true},
		{"deploy.yml", "deploy.yml.tmpl", DeployWorkflowData{
			ProjectName:  "demo",
			Environments: []DeployEnv{{Name: "staging", Auto: true}, {Name: "prod", Protection: true}},
			Concurrency:  true,
		}, false},
		{"release.yml", "release.yml.tmpl", releaseFixture(), false},
	}
	for _, wf := range workflows {
		out, err := CITemplates("github").Render(wf.tmpl, wf.data)
		if err != nil {
			t.Fatalf("render %s: %v", wf.name, err)
		}
		for _, v := range concurrencyCancelViolations(t, wf.name, out) {
			t.Errorf("%s — cancels runs on main/tags; want false or %s", v, prOnlyCancel)
		}
		if !wf.refGrouped {
			continue
		}
		got := workflowConcurrency(t, wf.name, out)
		if got["cancel-in-progress"] != prOnlyCancel {
			t.Errorf("%s: workflow concurrency = %v, want cancel-in-progress: %s", wf.name, got, prOnlyCancel)
		}
	}
}

// TestForgeOwnWorkflows_CancelInProgressOnlyOnPullRequests holds forge's own
// CI to the policy it scaffolds. Every one of these runs on push to main, and
// the files are read by this process, so the test cache sees an edit to them.
func TestForgeOwnWorkflows_CancelInProgressOnlyOnPullRequests(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("found no workflows under ../../.github/workflows — has the repo layout moved?")
	}
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range concurrencyCancelViolations(t, filepath.Base(p), content) {
			t.Errorf("%s — cancels runs on main/tags; want false or %s", v, prOnlyCancel)
		}
	}
}
