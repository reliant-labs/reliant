package main

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowPermissions struct {
	On          map[string]any    `yaml:"on"`
	Permissions any               `yaml:"permissions"`
	Env         map[string]string `yaml:"env"`
	Jobs        map[string]struct {
		Permissions any               `yaml:"permissions"`
		Env         map[string]string `yaml:"env"`
		Steps       []mqStep          `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestMergeQueueHasNoWriteCredential(t *testing.T) {
	var workflow workflowPermissions
	path := ".github/workflows/merge-queue.yml"
	if err := yaml.Unmarshal(readRepo(t, path), &workflow); err != nil {
		t.Fatal(err)
	}
	if err := readOnly(workflow.Permissions); err != nil {
		t.Errorf("%s: %v", path, err)
	}
	if exposesGitHubToken(workflow.Env) {
		t.Errorf("%s exposes a GitHub token in workflow env", path)
	}
	ciSteps := 0
	for name, job := range workflow.Jobs {
		permissions := job.Permissions
		if permissions == nil {
			permissions = workflow.Permissions
		}
		if err := readOnly(permissions); err != nil {
			t.Errorf("%s job %s: %v", path, name, err)
		}
		if exposesGitHubToken(job.Env) {
			t.Errorf("%s job %s exposes a GitHub token in job env", path, name)
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "task ci:") || strings.HasPrefix(step.Name, "task ci:") {
				ciSteps++
			}
			if exposesGitHubToken(step.Env) || strings.Contains(step.Run, "github.token") || strings.Contains(step.Run, "secrets.GITHUB_TOKEN") {
				t.Errorf("%s job %s step %q exposes a GitHub token", path, name, step.Name)
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") && fmt.Sprint(step.With["persist-credentials"]) != "false" {
				t.Errorf("%s job %s checkout persists its credential", path, name)
			}
			if step.Uses == "./.github/actions/setup" {
				token, provided := step.With["github-read-token"]
				if !provided || token != "" {
					t.Errorf("%s job %s must opt out of setup's token", path, name)
				}
			}
		}
	}
	if ciSteps == 0 {
		t.Error("credential guard found no candidate task ci steps")
	}
}

func readOnly(value any) error {
	permissions, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("want an explicit permissions map, got %v", value)
	}
	for name, scope := range permissions {
		if name != "contents" || (scope != "read" && scope != "none") {
			return fmt.Errorf("candidate workflow must be contents read-only, found %s: %v", name, scope)
		}
	}
	return nil
}

func exposesGitHubToken(env map[string]string) bool {
	for name, value := range env {
		if name == "GH_TOKEN" || name == "GITHUB_TOKEN" || strings.Contains(value, "github.token") || strings.Contains(value, "secrets.GITHUB_TOKEN") {
			return true
		}
	}
	return false
}

func TestMergeQueueSLOWritesFromDefaultBranchOnly(t *testing.T) {
	var workflow workflowPermissions
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/merge-queue-slo.yml"), &workflow); err != nil {
		t.Fatal(err)
	}
	if _, ok := workflow.On["workflow_run"]; !ok || len(workflow.On) != 1 {
		t.Fatalf("SLO publisher must be a default-branch workflow_run listener: %v", workflow.On)
	}
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" {
				t.Errorf("SLO publisher %s executes an action rather than just consuming structured evidence: %s", name, step.Uses)
			}
		}
	}
}
