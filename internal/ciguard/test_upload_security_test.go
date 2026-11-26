package ciguard

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// The two upload jobs of test-upload.yml and the whole authorization
// expression each must carry.
const (
	pushUploadJob       = "upload"
	pushUploadIf        = "github.repository == 'reliant-labs/forge' && github.event.workflow_run.event == 'push' && github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.conclusion != 'cancelled'"
	mergeQueueUploadJob = "upload-merge-queue"
	mergeQueueUploadIf  = "github.repository == 'reliant-labs/forge' && github.event.workflow_run.event == 'merge_group' && github.event.workflow_run.conclusion != 'cancelled'"
)

// uploadJobPermissions is what each upload job may hold: read the CI run's
// artifacts and the repository, and mint the job's OIDC token, which forge
// exchanges for a tests:write credential.
var uploadJobPermissions = map[string]string{"actions": "read", "contents": "read", "id-token": "write"}

// Check the entire authorization expression, not substrings: an added || true
// must not make fork, PR, or cancelled runs eligible for credentialed execution.
// Together with workflow_run's branch filter, these are GitHub's denial
// controls. Pin their syntax rather than implement a second Actions evaluator.
//
// The trust boundary: no job runs code that is not on main. The push job
// checks out the pushed commit (main's own, merged) and alone reaches the
// tests-upload environment's secret. The merge-queue job checks out main's
// tip (github.sha, the default branch in a workflow_run) and treats the
// entry, which may never merge, as data: it gets no environment and no
// secret, only the job's OIDC identity, as the push job also has.
func testUploadSecurity(raw []byte) error {
	type step struct {
		Uses string            `yaml:"uses"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
		With map[string]string `yaml:"with"`
	}
	var wf struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
				Branches  []string `yaml:"branches"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Env         map[string]string `yaml:"env"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Environment string            `yaml:"environment"`
			Env         map[string]string `yaml:"env"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []step            `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		return err
	}
	wr := wf.On.WorkflowRun
	if !slices.Equal(wr.Workflows, []string{"CI"}) || !slices.Equal(wr.Types, []string{"completed"}) ||
		!slices.Equal(wr.Branches, []string{"main", "gh-readonly-queue/main/**"}) {
		return fmt.Errorf("only completed CI runs of main and of its merge queue may trigger upload")
	}
	if len(wf.Env) != 0 || !maps.Equal(wf.Permissions, map[string]string{"contents": "read"}) {
		return fmt.Errorf("the workflow level grants contents: read alone and no env")
	}
	if len(wf.Jobs) != 2 {
		return fmt.Errorf("expected only the two guarded upload jobs")
	}
	for _, name := range []string{pushUploadJob, mergeQueueUploadJob} {
		j, ok := wf.Jobs[name]
		if !ok {
			return fmt.Errorf("no %s job", name)
		}
		push := name == pushUploadJob
		cond, ref := mergeQueueUploadIf, "${{ github.sha }}"
		if push {
			cond, ref = pushUploadIf, "${{ github.event.workflow_run.head_sha }}"
		}
		if strings.Join(strings.Fields(j.If), " ") != cond {
			return fmt.Errorf("%s must deny foreign repositories, other events and branches, and cancelled runs: %q", name, j.If)
		}
		if push && j.Environment != "tests-upload" {
			return fmt.Errorf("%s requires the tests-upload environment", name)
		}
		if !push && j.Environment != "" {
			return fmt.Errorf("%s runs over a merge-queue entry's artifacts and must reach no environment", name)
		}
		if len(j.Env) != 0 {
			return fmt.Errorf("%s must carry no job env credentials", name)
		}
		if !maps.Equal(j.Permissions, uploadJobPermissions) {
			return fmt.Errorf("%s permissions %v, want exactly %v", name, j.Permissions, uploadJobPermissions)
		}
		checkout, upload := 0, 0
		for i, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout@") {
				checkout++
				if i != 0 || s.With["ref"] != ref || s.With["persist-credentials"] != "false" {
					return fmt.Errorf("%s's first step must check out %s without persisting credentials", name, ref)
				}
			}
			if s.Run == `scripts/ci-test-upload.sh "$HEAD_SHA"` {
				upload++
				want := map[string]string{"HEAD_SHA": "${{ github.event.workflow_run.head_sha }}"}
				if push {
					want["FORGE_CONTROL_PLANE_TOKEN"] = "${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}"
				}
				if !maps.Equal(s.Env, want) {
					return fmt.Errorf("%s's upload step env %v, want exactly %v", name, s.Env, want)
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
				return fmt.Errorf("%s: non-upload step exposes credentials", name)
			}
		}
		if checkout != 1 || upload != 1 {
			return fmt.Errorf("%s: expected one checkout and one upload step", name)
		}
	}
	if strings.Count(string(raw), "secrets.") != 1 {
		return fmt.Errorf("expected one step-local secret exposure")
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
	// while bypassing it. All fixtures start from the actual workflow on disk;
	// each replaces the nth occurrence of from (the push job comes first).
	for _, tc := range []struct {
		name, from, to string
		nth            int
	}{
		{"foreign repository", "github.repository == 'reliant-labs/forge' &&", "", 1},
		{"foreign repository (merge queue)", "github.repository == 'reliant-labs/forge' &&", "", 2},
		{"PR run", "github.event.workflow_run.event == 'push' &&", "", 1},
		{"PR run (merge queue)", "github.event.workflow_run.event == 'merge_group'", "github.event.workflow_run.event != 'push'", 1},
		{"push of a queue branch", "github.event.workflow_run.head_branch == 'main' &&", "", 1},
		{"cancelled run", "github.event.workflow_run.conclusion != 'cancelled'", "true", 1},
		{"cancelled run (merge queue)", "github.event.workflow_run.conclusion != 'cancelled'", "true", 2},
		{"condition bypass", "github.event.workflow_run.conclusion != 'cancelled'", "github.event.workflow_run.conclusion != 'cancelled' || true", 1},
		{"condition bypass (merge queue)", "github.event.workflow_run.conclusion != 'cancelled'", "github.event.workflow_run.conclusion != 'cancelled' || true", 2},
		{"any branch", `branches: [main, "gh-readonly-queue/main/**"]`, "branches: ['*']", 1},
		{"any queue", `"gh-readonly-queue/main/**"`, `"gh-readonly-queue/**"`, 1},
		{"wrong workflow", `workflows: ["CI"]`, `workflows: ["Untrusted"]`, 1},
		{"uncompleted run", "types: [completed]", "types: [requested]", 1},
		{"wrong checkout SHA", "ref: ${{ github.event.workflow_run.head_sha }}", "ref: main", 1},
		{"merge queue runs the entry's code", "ref: ${{ github.sha }}", "ref: ${{ github.event.workflow_run.head_sha }}", 1},
		{"persisted credentials", "persist-credentials: false", "persist-credentials: true", 1},
		{"persisted credentials (merge queue)", "persist-credentials: false", "persist-credentials: true", 2},
		{"wrong environment", "environment: tests-upload", "environment: other", 1},
		{"merge queue reaches the environment", "    continue-on-error: true\n    permissions:", "    continue-on-error: true\n    environment: tests-upload\n    permissions:", 1},
		{"job secret exposure", "    environment: tests-upload", "    environment: tests-upload\n    env:\n      TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}", 1},
		{"setup secret exposure", "      - uses: actions/setup-go@v7", "      - uses: actions/setup-go@v7\n        env:\n          TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}", 1},
		{"merge queue secret exposure", "          HEAD_SHA: ${{ github.event.workflow_run.head_sha }}", "          HEAD_SHA: ${{ github.event.workflow_run.head_sha }}\n          FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}", 2},
		{"missing upload secret", "FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}", "FORGE_CONTROL_PLANE_TOKEN: ''", 1},
		{"write permission", "      id-token: write", "      id-token: write\n      contents: write", 1},
		{"write permission (merge queue)", "      id-token: write", "      id-token: write\n      pull-requests: write", 2},
		{"workflow-level grant", "permissions:\n  contents: read", "permissions:\n  contents: read\n  id-token: write", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(raw), tc.from) < tc.nth {
				t.Fatalf("fixture needs %d occurrence(s) of %q", tc.nth, tc.from)
			}
			mutated := replaceNth(string(raw), tc.from, tc.to, tc.nth)
			if err := testUploadSecurity([]byte(mutated)); err == nil {
				t.Fatal("unsafe workflow passed the trust-boundary guard")
			}
		})
	}
}

// replaceNth replaces the nth (1-based) occurrence of old in s.
func replaceNth(s, old, repl string, n int) string {
	at := 0
	for i := 1; ; i++ {
		j := strings.Index(s[at:], old)
		if j < 0 {
			return s
		}
		if i == n {
			return s[:at+j] + repl + s[at+j+len(old):]
		}
		at += j + len(old)
	}
}
