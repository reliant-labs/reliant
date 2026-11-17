package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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
// one PENDING run per group and a newer push supersedes it, so a burst of
// merges costs one extra run, not one per merge (supersedeViolations).
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

// pullRequestEvents are the triggers whose runs belong to one pull request or
// one merge group, never to main, a tag, a schedule or a dispatch.
var pullRequestEvents = map[string]bool{
	"pull_request":                true,
	"pull_request_target":         true,
	"pull_request_review":         true,
	"pull_request_review_comment": true,
	"merge_group":                 true,
}

// pullRequestOnly reports whether a workflow triggers on nothing but
// pullRequestEvents. Such a workflow has no main or tag run for a
// cancel-in-progress to starve, so it may cancel on every event it has. (The
// review gate, reliant-review.yml, deliberately has no group at all: GitHub
// holds a required check red while any suite's newest run of it is
// cancelled.)
func pullRequestOnly(t *testing.T, name string, content []byte) bool {
	t.Helper()
	var doc struct {
		On any `yaml:"on"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", name, err)
	}
	var events []string
	switch on := doc.On.(type) {
	case string:
		events = []string{on}
	case []any:
		for _, e := range on {
			s, _ := e.(string)
			events = append(events, s)
		}
	case map[string]any:
		for e := range on {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return false
	}
	for _, e := range events {
		if !pullRequestEvents[e] {
			return false
		}
	}
	return true
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
// Actions workflow forge scaffolds and pins the concurrency policy: the run in
// progress is cancelled by a newer one only on a pull request, and on main a
// newer push supersedes only the pending run (supersedeViolations). The
// workflows that group on github.ref carry exactly prOnlyCancel and never key
// their group on github.sha; none anywhere carries a literal true.
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
		bad, _ := supersedeViolations(t, wf.name, out)
		for _, v := range bad {
			t.Error(v)
		}
		if g, _ := got["group"].(string); strings.Contains(g, "github.sha") {
			t.Errorf("%s: group %q is keyed on github.sha; a run's group must be shared per ref so a newer run supersedes the pending one", wf.name, g)
		}
	}
}

// workflowEvents returns a workflow's trigger events and, for `push`, the
// branches it filters on (nil when it has no branches filter).
func workflowEvents(t *testing.T, name string, content []byte) (events map[string]bool, pushBranches []string, pushTagsOnly bool) {
	t.Helper()
	var doc struct {
		On any `yaml:"on"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", name, err)
	}
	events = map[string]bool{}
	switch on := doc.On.(type) {
	case string:
		events[on] = true
	case []any:
		for _, e := range on {
			s, _ := e.(string)
			events[s] = true
		}
	case map[string]any:
		for e, v := range on {
			events[e] = true
			if e != "push" {
				continue
			}
			push, _ := v.(map[string]any)
			if push["branches"] != nil {
				pushBranches = asStrings(push["branches"])
			}
			_, hasTags := push["tags"]
			pushTagsOnly = hasTags && push["branches"] == nil
		}
	}
	return events, pushBranches, pushTagsOnly
}

func asStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// supersedeViolations holds a workflow that runs on push to main to the rule
// of 2026-11-01 (owner): every push to main shares one concurrency group per
// workflow and ref, so a newer push SUPERSEDES the pending run (GitHub keeps at
// most one pending run per group and cancels the older one when a newer one
// queues) while the run in progress finishes (cancel-in-progress false off a
// pull request). A group keyed on github.sha gives every commit a group of its
// own and queues a run per merge. A workflow that also runs on a schedule, a
// dispatch or a workflow_call keys its group on github.event_name too, so
// those runs neither supersede nor are superseded by a push run. A merge_group
// run stays alone in its group because its ref names the group's own head.
//
// pushesToMain reports whether the rule applied at all.
func supersedeViolations(t *testing.T, name string, content []byte) (bad []string, pushesToMain bool) {
	t.Helper()
	events, branches, tagsOnly := workflowEvents(t, name, content)
	if !events["push"] || tagsOnly {
		return nil, false
	}
	if branches != nil && !slices.Contains(branches, "main") {
		return nil, false
	}
	conc := workflowConcurrency(t, name, content)
	group, _ := conc["group"].(string)
	if group == "" {
		return []string{fmt.Sprintf("%s runs on push to main but has no workflow concurrency group; want one group per ref so a newer push supersedes the pending run", name)}, true
	}
	if !strings.Contains(group, "github.ref") {
		bad = append(bad, fmt.Sprintf("%s: group %q is not keyed on github.ref", name, group))
	}
	if strings.Contains(group, "github.sha") {
		bad = append(bad, fmt.Sprintf("%s: group %q is keyed on github.sha, so pushes to main never share a group and a newer push cannot supersede a pending run", name, group))
	}
	if (events["schedule"] || events["workflow_call"] || events["workflow_dispatch"]) && !strings.Contains(group, "github.event_name") {
		bad = append(bad, fmt.Sprintf("%s: group %q is shared by push and scheduled/dispatched/called runs; key it on github.event_name so they stay out of the main push group", name, group))
	}
	if c := conc["cancel-in-progress"]; c != prOnlyCancel && c != false && c != nil {
		bad = append(bad, fmt.Sprintf("%s: cancel-in-progress = %v; a main run in progress must finish (want false or %s)", name, c, prOnlyCancel))
	}
	return bad, true
}

// TestForgeOwnWorkflows_MainPushesSupersedePendingRuns holds forge's own CI
// to supersedeViolations' rule, the one it scaffolds
// (TestCIWorkflows_CancelInProgressOnlyOnPullRequests).
func TestForgeOwnWorkflows_MainPushesSupersedePendingRuns(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		bad, applies := supersedeViolations(t, filepath.Base(p), content)
		if applies {
			checked++
		}
		for _, v := range bad {
			t.Error(v)
		}
	}
	if checked == 0 {
		t.Fatal("found no workflow that runs on push to main — has the repo layout moved?")
	}
}

// TestForgeOwnWorkflows_CancelInProgressOnlyOnPullRequests holds forge's own
// CI to the policy it scaffolds: the run in progress is cancelled by a newer
// one only on a pull request, never on main (where a newer push supersedes
// only the PENDING run — TestForgeOwnWorkflows_MainPushesSupersedePendingRuns).
// The files are read by this process, so the test cache sees an edit to them.
// A workflow that runs only for pull requests and merge groups is exempt
// (pullRequestOnly): it has no main run to cancel.
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
		if pullRequestOnly(t, filepath.Base(p), content) {
			continue
		}
		for _, v := range concurrencyCancelViolations(t, filepath.Base(p), content) {
			t.Errorf("%s — cancels runs on main/tags; want false or %s", v, prOnlyCancel)
		}
	}
}
