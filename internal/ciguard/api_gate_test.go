package ciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// D2 (research/pinning/OPTIONS.md): a forge PR that breaks pkg/ must declare it
// and ship its codemod (the API gate), and the consumers must keep building
// (the canary). These tests pin the wiring, not the logic — the logic has its
// own tests in internal/apigate and internal/consumercanary.

var canaryWorkflow = filepath.Join("..", "..", ".github", "workflows", "consumer-canary.yml")

func TestAPIGateRunsOnPullRequestsInThePRChecksJob(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf stepsWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	checks, ok := wf.Jobs["pr-checks"]
	if !ok {
		t.Fatal("ci.yml has no pr-checks job")
	}
	found := false
	for _, s := range checks.Steps {
		if !strings.Contains(s.Run, "internal/apigate") {
			continue
		}
		found = true
		if strings.TrimSpace(s.If) != "${{ github.event_name == 'pull_request' }}" {
			t.Errorf("the API gate step has if: %q; it must run on pull_request only (it reads the pull request's labels and body; a push is already merged)", s.If)
		}
		for _, want := range []string{"-base HEAD^1", "-pr"} {
			if !strings.Contains(s.Run, want) {
				t.Errorf("the API gate step runs %q; it must pass %q (the merge commit's first parent, and the PR's live labels and body)", s.Run, want)
			}
		}
	}
	if !found {
		t.Error("ci.yml's pr-checks job does not run internal/apigate: an undeclared break of forge/pkg could merge")
	}
}

func TestAPIGateHasNoJobOfItsOwn(t *testing.T) {
	// Every job a pull request adds is another runner to queue for (ci.yml,
	// "FEWER JOBS"); the gate shares pr-checks with the CHANGELOG guard.
	wf := readWorkflow(t)
	for name := range wf.Jobs {
		if strings.Contains(name, "apigate") || strings.Contains(name, "api-gate") {
			t.Errorf("job %q: the API gate is a step of `pr-checks`, not a job", name)
		}
	}
}

type canaryJob struct {
	If    string `yaml:"if"`
	Steps []struct {
		Name string `yaml:"name"`
		Run  string `yaml:"run"`
		With map[string]any
		Uses string `yaml:"uses"`
		Env  map[string]string
	} `yaml:"steps"`
}

type canaryWorkflowDoc struct {
	On   map[string]any       `yaml:"on"`
	Jobs map[string]canaryJob `yaml:"jobs"`
}

func readCanary(t *testing.T) (canaryWorkflowDoc, string) {
	t.Helper()
	raw, err := os.ReadFile(canaryWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf canaryWorkflowDoc
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("%s does not parse: %v", canaryWorkflow, err)
	}
	return wf, string(raw)
}

func TestCanaryPullRequestsNeedTheLabel(t *testing.T) {
	wf, _ := readCanary(t)
	job, ok := wf.Jobs["canary"]
	if !ok {
		t.Fatal("consumer-canary.yml has no canary job")
	}
	if !strings.Contains(job.If, "contains(github.event.pull_request.labels.*.name, 'ci:canary')") ||
		!strings.Contains(job.If, "github.event_name != 'pull_request'") {
		t.Errorf("the canary job's if is %q; on a pull request it must require the ci:canary label, and run otherwise (push, nightly, dispatch)", job.If)
	}
}

func TestCanaryWorkflowIsNotPartOfTheRequiredGate(t *testing.T) {
	wf := readWorkflow(t)
	if _, ok := wf.Jobs["canary"]; ok {
		t.Error("ci.yml carries a canary job: the canary is informational (consumer-canary.yml), `CI gate` must not wait on a whole-module build of two consumers")
	}
}

func TestCanaryDoesNotLeakTheDeployKeyToBuildSteps(t *testing.T) {
	wf, raw := readCanary(t)
	if !strings.Contains(raw, "CONTROL_PLANE_DEPLOY_KEY") {
		t.Fatal("consumer-canary.yml no longer names CONTROL_PLANE_DEPLOY_KEY: control-plane would silently stop being checked")
	}
	for _, s := range wf.Jobs["canary"].Steps {
		usesKey := strings.Contains(s.Run, "CONTROL_PLANE_DEPLOY_KEY")
		for _, v := range s.Env {
			usesKey = usesKey || strings.Contains(v, "CONTROL_PLANE_DEPLOY_KEY")
		}
		for _, v := range s.With {
			if str, ok := v.(string); ok && strings.Contains(str, "CONTROL_PLANE_DEPLOY_KEY") {
				usesKey = true
				if !strings.HasPrefix(s.Uses, "actions/checkout@") {
					t.Errorf("step %q hands the deploy key to %q; only actions/checkout may hold it", s.Name, s.Uses)
				}
				if s.With["persist-credentials"] != false {
					t.Errorf("step %q checks out the private repository without persist-credentials: false; the key would stay on disk for the steps that build code under review", s.Name)
				}
			}
		}
		if usesKey && strings.Contains(s.Run, "ci-consumer-canary.sh") {
			t.Errorf("step %q runs the canary with the deploy key in its environment", s.Name)
		}
	}
}

func TestNightlyRunsTheCanaryAndReportsIt(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "nightly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Uses  string   `yaml:"uses"`
			Needs []string `yaml:"needs"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	j, ok := wf.Jobs["consumer-canary"]
	if !ok || j.Uses != "./.github/workflows/consumer-canary.yml" {
		t.Fatalf("nightly.yml must call consumer-canary.yml; got %+v", j)
	}
	reported := false
	for _, n := range wf.Jobs["report"].Needs {
		reported = reported || n == "consumer-canary"
	}
	if !reported {
		t.Error("nightly's report job does not need consumer-canary: a red canary would not open the \"Nightly CI is red\" issue")
	}
}
