package testcapture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetectCIGitHubPullRequest(t *testing.T) {
	t.Parallel()
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"base":{"sha":"77e0aa"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := detectCI(envOf(map[string]string{
		"GITHUB_ACTIONS":     "true",
		"GITHUB_RUN_ID":      "123",
		"GITHUB_RUN_ATTEMPT": "2",
		"GITHUB_REPOSITORY":  "acme/shop",
		"GITHUB_EVENT_NAME":  "pull_request",
		"GITHUB_REF":         "refs/pull/5/merge",
		"GITHUB_HEAD_REF":    "feat/x",
		"GITHUB_REF_NAME":    "5/merge",
		"GITHUB_EVENT_PATH":  event,
	}))
	want := testreport.ManifestCI{
		Provider: "github", RunID: "123", Attempt: 2, Event: "pull_request", Ref: "refs/pull/5/merge",
		RunURL: "https://github.com/acme/shop/actions/runs/123",
	}
	if f.ci == nil || *f.ci != want {
		t.Fatalf("ci = %+v, want %+v", f.ci, want)
	}
	if f.branch != "feat/x" || f.base != "77e0aa" {
		t.Fatalf("branch, base = %q, %q; want the pull request's head branch and base", f.branch, f.base)
	}
}

func TestDetectCIGitHubPushAndMergeGroup(t *testing.T) {
	t.Parallel()
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"merge_group":{"base_sha":"b45e"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := detectCI(envOf(map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "9",
		"GITHUB_SERVER_URL": "https://ghe.example/",
		"GITHUB_REPOSITORY": "acme/shop",
		"GITHUB_REF_TYPE":   "branch",
		"GITHUB_REF_NAME":   "main",
		"GITHUB_EVENT_PATH": event,
	}))
	if f.ci.Attempt != 1 || f.ci.RunURL != "https://ghe.example/acme/shop/actions/runs/9" {
		t.Fatalf("ci = %+v: an absent attempt is the first, and the server URL is the runner's", f.ci)
	}
	if f.branch != "main" || f.base != "b45e" {
		t.Fatalf("branch, base = %q, %q", f.branch, f.base)
	}
}

func TestDetectCIOtherProviders(t *testing.T) {
	t.Parallel()
	gl := detectCI(envOf(map[string]string{
		"GITLAB_CI": "true", "CI_PIPELINE_ID": "77", "CI_PIPELINE_URL": "https://gitlab.example/p/-/pipelines/77",
		"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": "feat/y", "CI_MERGE_REQUEST_DIFF_BASE_SHA": "abc",
	}))
	if gl.ci == nil || gl.ci.Provider != "gitlab" || gl.ci.RunID != "77" || gl.branch != "feat/y" || gl.base != "abc" {
		t.Fatalf("gitlab = %+v %+v", gl, gl.ci)
	}
	bk := detectCI(envOf(map[string]string{"BUILDKITE": "true", "BUILDKITE_BUILD_ID": "u-1", "BUILDKITE_RETRY_COUNT": "1", "BUILDKITE_BRANCH": "main"}))
	if bk.ci == nil || bk.ci.Provider != "buildkite" || bk.ci.Attempt != 2 || bk.branch != "main" {
		t.Fatalf("buildkite = %+v %+v", bk, bk.ci)
	}
	if none := detectCI(envOf(nil)); none.ci != nil {
		t.Fatalf("no CI environment detected %+v", none.ci)
	}
}
