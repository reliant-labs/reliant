package ciguard

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const forgeQueueSLOWorkflow = "../../.github/workflows/ci-queue-slo.yml"

func readQueueReporter(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(forgeQueueSLOWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]map[string][]string `yaml:"on"`
		Permissions map[string]string              `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses, Run string
			}
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	trigger := workflow.On["workflow_run"]
	job := workflow.Jobs["report"]
	if len(workflow.On) != 1 || strings.Join(trigger["workflows"], ",") != "CI" || strings.Join(trigger["types"], ",") != "completed" ||
		workflow.Permissions == nil || len(workflow.Permissions) != 0 || len(workflow.Jobs) != 1 ||
		len(job.Permissions) != 2 || job.Permissions["actions"] != "read" || job.Permissions["issues"] != "write" ||
		len(job.Steps) != 1 || job.Steps[0].Uses != "" || job.Steps[0].Run == "" {
		t.Fatal("queue reporting must be one default-branch workflow_run job with no checkout or external action")
	}
	return job.Steps[0].Run
}

func TestQueueReporterRunsOnlyFromDefaultBranchWorkflow(t *testing.T) { _ = readQueueReporter(t) }

func TestQueueReporterAuthenticatesRunAndUsesActionsClock(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the workflow Bash/Python against mock GitHub responses; runs in task test")
	}
	requireCICommands(t, "bash", "jq", "python3")
	script := readQueueReporter(t)
	for _, tc := range []struct {
		name, mutation, member string
		publish, success       bool
	}{
		{"valid breach", "", "forge-ci-queue-slo.json", true, true},
		{"valid within budget", "fast", "forge-ci-queue-slo.json", false, true},
		{"wrong workflow ID", "wrong-id", "forge-ci-queue-slo.json", false, false},
		{"wrong workflow path", "wrong-path", "forge-ci-queue-slo.json", false, false},
		{"wrong head repository", "fork", "forge-ci-queue-slo.json", false, false},
		{"wrong queue branch", "wrong-branch", "forge-ci-queue-slo.json", false, false},
		{"wrong event", "wrong-event", "forge-ci-queue-slo.json", false, false},
		{"wrong attempt", "wrong-attempt", "forge-ci-queue-slo.json", false, false},
		{"missing receipt", "missing", "forge-ci-queue-slo.json", false, true},
		{"ambiguous receipt", "duplicate", "forge-ci-queue-slo.json", false, false},
		{"oversized compressed response", "large-response", "forge-ci-queue-slo.json", false, false},
		{"receipt contains a clock", "untrusted-clock", "forge-ci-queue-slo.json", false, false},
		{"wrong receipt head", "wrong-head", "forge-ci-queue-slo.json", false, false},
		{"ZIP traversal", "", "../forge-ci-queue-slo.json", false, false},
		{"symlink member", "symlink", "forge-ci-queue-slo.json", false, false},
		{"jobs for another run", "wrong-job", "forge-ci-queue-slo.json", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			head := strings.Repeat("a", 40)
			fact := map[string]any{"schema": "forge-ci-queue-slo/v1", "repo": "reliant-labs/forge", "head": head, "run_id": 123, "attempt": 2}
			switch tc.mutation {
			case "wrong-head":
				fact["head"] = strings.Repeat("b", 40)
			case "untrusted-clock":
				fact["wall"] = 500
			}
			var zipBytes bytes.Buffer
			zw := zip.NewWriter(&zipBytes)
			zh := &zip.FileHeader{Name: tc.member}
			if tc.mutation == "symlink" {
				zh.SetMode(os.ModeSymlink | 0o777)
			}
			member, err := zw.CreateHeader(zh)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(member).Encode(fact); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			archive := zipBytes.Bytes()
			if tc.mutation == "large-response" {
				archive = make([]byte, 4097)
			}
			writeCIFixture(t, dir, "timing.zip", archive, 0o600)
			writeCIFixture(t, dir, "event.json", []byte(`{"workflow_run":{"id":123,"run_attempt":2,"head_sha":"`+head+`"}}`), 0o600)
			writeCIFixture(t, dir, "gh", []byte(fakeQueueGitHub), 0o700)
			summary := filepath.Join(dir, "summary")
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_EVENT_PATH="+filepath.Join(dir, "event.json"),
				"ARCHIVE="+filepath.Join(dir, "timing.zip"), "HEAD="+head, "MUTATION="+tc.mutation, "GITHUB_STEP_SUMMARY="+summary)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.success || strings.Contains(string(out), "published") != tc.publish {
				t.Fatalf("want success=%v publish=%v, got %v\n%s", tc.success, tc.publish, err, out)
			}
			if tc.publish {
				body, err := os.ReadFile(summary)
				if err != nil || !strings.Contains(string(body), "Critical path: **360s**") || !strings.Contains(string(body), "wall 400s (longest runner wait 40s") {
					t.Fatalf("summary must report Actions timestamps: %v\n%s", err, body)
				}
			}
		})
	}
}

func requireCICommands(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("needs %s: %v", name, err)
		}
	}
}

func writeCIFixture(t *testing.T, dir, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
		t.Fatal(err)
	}
}

const fakeQueueGitHub = `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *'/actions/workflows/ci.yml'*) echo 44 ;;
  *'/actions/runs/123/attempts/2/jobs'*)
    done='2026-11-20T17:06:40Z'; job=123
    [[ $MUTATION != fast ]] || done='2026-11-20T17:04:00Z'
    [[ $MUTATION != wrong-job ]] || job=124
    jq -n --arg end "$done" --argjson id "$job" '{name:"CI gate",conclusion:"success",run_id:$id,created_at:"2026-11-20T17:00:00Z",started_at:"2026-11-20T17:00:40Z",completed_at:$end}' ;;
  *'/actions/runs/123/attempts/2')
    path=.github/workflows/ci.yml; wf=44; attempt=2; ev=merge_group; branch=gh-readonly-queue/main/pr-4-aaaaaaaa; head_repo=reliant-labs/forge
    case "$MUTATION" in
      wrong-id) wf=45;; wrong-path) path=.github/workflows/wrong.yml;;
      wrong-branch) branch=gh-readonly-queue/other/pr-4-aaaaaaaa;; wrong-attempt) attempt=1;;
      wrong-event) ev=push;; fork) head_repo=other/forge;;
    esac
    jq -n --arg head "$HEAD" --arg path "$path" --arg branch "$branch" --arg event "$ev" --arg head_repo "$head_repo" --argjson wf "$wf" --argjson attempt "$attempt" '{id:123,workflow_id:$wf,run_attempt:$attempt,event:$event,status:"completed",repository:{full_name:"reliant-labs/forge"},head_repository:{full_name:$head_repo},head_sha:$head,head_branch:$branch,path:$path,created_at:"2026-11-20T17:00:00Z",run_started_at:"2026-11-20T17:00:40Z"}' ;;
  *'/actions/runs/123/artifacts?'*)
    jq -n --arg head "$HEAD" --arg mutation "$MUTATION" '{name:"forge-ci-queue-slo-2",id:99,expired:false,size_in_bytes:256,workflow_run:{id:123,head_sha:$head}} as $a | {artifacts:(if $mutation == "missing" then [] elif $mutation == "duplicate" then [$a,$a] else [$a] end)}' ;;
  *'/actions/artifacts/99/zip'*) cat "$ARCHIVE" ;;
  'issue list '*) echo '[]' ;;
  'label create '*) : ;;
  'issue create '*) [[ $* == *'wall 400s (longest runner wait 40s'* ]]; echo published ;;
  *) exit 12 ;;
esac
`
