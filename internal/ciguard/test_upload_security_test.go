package ciguard

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// Check the entire authorization expression, not substrings: an added || true
// must not make fork, PR, or cancelled runs eligible for credentialed execution.
// Together with workflow_run's main-only filter, these are GitHub's denial
// controls. Pin their syntax rather than implement a second Actions evaluator.
func testUploadSecurity(raw []byte) error {
	var wf struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
				Branches  []string `yaml:"branches"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			If          string            `yaml:"if"`
			Environment string            `yaml:"environment"`
			Env         map[string]string `yaml:"env"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return err
	}
	wr := wf.On.WorkflowRun
	if !slices.Equal(wr.Workflows, []string{"CI"}) || !slices.Equal(wr.Types, []string{"completed"}) || !slices.Equal(wr.Branches, []string{"main"}) {
		return fmt.Errorf("only completed main CI runs may trigger upload")
	}
	j, ok := wf.Jobs["upload"]
	if !ok || len(wf.Jobs) != 1 {
		return fmt.Errorf("expected only the guarded upload job")
	}
	const condition = "github.repository == 'reliant-labs/forge' && github.event.workflow_run.event == 'push' && github.event.workflow_run.conclusion != 'cancelled'"
	if strings.Join(strings.Fields(j.If), " ") != condition {
		return fmt.Errorf("upload must deny foreign repositories, non-push events and cancelled runs: %q", j.If)
	}
	if j.Environment != "tests-upload" || len(wf.Env) != 0 || len(j.Env) != 0 {
		return fmt.Errorf("upload requires tests-upload environment and no workflow/job env credentials")
	}
	checkout, upload := 0, 0
	for i, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") {
			checkout++
			if i != 0 || s.With["ref"] != "${{ github.event.workflow_run.head_sha }}" || s.With["persist-credentials"] != "false" {
				return fmt.Errorf("first step must check out triggering SHA without persisting credentials")
			}
		}
		if s.Run == `scripts/ci-test-upload.sh "$HEAD_SHA"` {
			upload++
			if s.Env["FORGE_CONTROL_PLANE_TOKEN"] != "${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}" || s.Env["HEAD_SHA"] != "${{ github.event.workflow_run.head_sha }}" || len(s.Env) != 2 {
				return fmt.Errorf("only upload step may receive token and triggering SHA")
			}
			continue
		}
		// No other step may receive a secret, even under a different env key
		// or in with/run (not just the conventional token variable).
		other, err := yaml.Marshal(s)
		if err != nil {
			return err
		}
		if strings.Contains(string(other), "secrets.") || len(s.Env) != 0 {
			return fmt.Errorf("non-upload step exposes credentials")
		}
	}
	if checkout != 1 || upload != 1 || strings.Count(string(raw), "secrets.") != 1 {
		return fmt.Errorf("expected one checkout and one step-local secret exposure")
	}
	return nil
}

func TestTestUploadTrustBoundary(t *testing.T) {
	raw, err := os.ReadFile(testUploadWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := testUploadSecurity(raw); err != nil {
		t.Fatal(err)
	}

	// Negative fixtures prove each denial/isolation control is independently
	// enforced by the guard, including attempts to retain a matching substring
	// while bypassing it. All fixtures start from the actual workflow on disk.
	for _, tc := range []struct{ name, from, to string }{
		{"foreign repository", "github.repository == 'reliant-labs/forge' &&", ""},
		{"PR run", "github.event.workflow_run.event == 'push' &&", ""},
		{"cancelled run", "github.event.workflow_run.conclusion != 'cancelled'", "true"},
		{"condition bypass", "github.event.workflow_run.conclusion != 'cancelled'", "github.event.workflow_run.conclusion != 'cancelled' || true"},
		{"non-main branch", "branches: [main]", "branches: ['*']"},
		{"wrong workflow", `workflows: ["CI"]`, `workflows: ["Untrusted"]`},
		{"uncompleted run", "types: [completed]", "types: [requested]"},
		{"wrong checkout SHA", "ref: ${{ github.event.workflow_run.head_sha }}", "ref: main"},
		{"persisted credentials", "persist-credentials: false", "persist-credentials: true"},
		{"wrong environment", "environment: tests-upload", "environment: other"},
		{"job secret exposure", "    environment: tests-upload", "    environment: tests-upload\n    env:\n      TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}"},
		{"setup secret exposure", "      - uses: actions/setup-go@v7", "      - uses: actions/setup-go@v7\n        env:\n          TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}"},
		{"missing upload secret", "FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}", "FORGE_CONTROL_PLANE_TOKEN: ''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(raw), tc.from) != 1 {
				t.Fatalf("fixture must replace exactly one occurrence of %q", tc.from)
			}
			mutated := strings.Replace(string(raw), tc.from, tc.to, 1)
			if err := testUploadSecurity([]byte(mutated)); err == nil {
				t.Fatal("unsafe workflow passed the trust-boundary guard")
			}
		})
	}
}
