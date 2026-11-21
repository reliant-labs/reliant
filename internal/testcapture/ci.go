package testcapture

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// ciFacts is what the CI environment says about a run: its identity, and the
// branch and base a detached CI checkout cannot tell git.
type ciFacts struct {
	ci     *testreport.ManifestCI
	branch string
	base   string
}

// detectCI reads the CI environment. It is pure apart from GitHub's event
// payload, a file the runner wrote, read only when GITHUB_EVENT_PATH names it.
func detectCI(getenv func(string) string) ciFacts {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	switch {
	case env("GITHUB_ACTIONS") == "true" && env("GITHUB_RUN_ID") != "":
		server := env("GITHUB_SERVER_URL")
		if server == "" {
			server = "https://github.com"
		}
		ci := &testreport.ManifestCI{
			Provider: "github",
			RunID:    env("GITHUB_RUN_ID"),
			Attempt:  attempt(env("GITHUB_RUN_ATTEMPT"), 0),
			Event:    env("GITHUB_EVENT_NAME"),
			Ref:      env("GITHUB_REF"),
		}
		if repo := env("GITHUB_REPOSITORY"); repo != "" {
			ci.RunURL = strings.TrimRight(server, "/") + "/" + repo + "/actions/runs/" + ci.RunID
		}
		f := ciFacts{ci: ci, branch: env("GITHUB_HEAD_REF")}
		if f.branch == "" && env("GITHUB_REF_TYPE") == "branch" {
			f.branch = env("GITHUB_REF_NAME")
		}
		f.base = gitHubEventBase(env("GITHUB_EVENT_PATH"))
		return f
	case env("GITLAB_CI") == "true" && env("CI_PIPELINE_ID") != "":
		f := ciFacts{
			ci: &testreport.ManifestCI{
				Provider: "gitlab",
				RunID:    env("CI_PIPELINE_ID"),
				RunURL:   env("CI_PIPELINE_URL"),
				Event:    env("CI_PIPELINE_SOURCE"),
				Ref:      env("CI_COMMIT_REF_NAME"),
			},
			branch: env("CI_COMMIT_BRANCH"),
			base:   env("CI_MERGE_REQUEST_DIFF_BASE_SHA"),
		}
		if f.branch == "" {
			f.branch = env("CI_MERGE_REQUEST_SOURCE_BRANCH_NAME")
		}
		return f
	case env("BUILDKITE") == "true" && env("BUILDKITE_BUILD_ID") != "":
		return ciFacts{
			ci: &testreport.ManifestCI{
				Provider: "buildkite",
				RunID:    env("BUILDKITE_BUILD_ID"),
				RunURL:   env("BUILDKITE_BUILD_URL"),
				// BUILDKITE_RETRY_COUNT counts retries of the job: the
				// first attempt is 0.
				Attempt: attempt(env("BUILDKITE_RETRY_COUNT"), 1),
				Event:   env("BUILDKITE_SOURCE"),
			},
			branch: env("BUILDKITE_BRANCH"),
		}
	}
	return ciFacts{}
}

// attempt parses a provider's attempt counter, offset to count from 1. An
// absent or unreadable value is the first attempt.
func attempt(v string, offset int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n+offset < 1 {
		return 1
	}
	return n + offset
}

// gitHubEventBase is the base commit in a GitHub event payload: a pull
// request's base, or a merge group's. Any other event, or a payload that
// cannot be read, has none.
func gitHubEventBase(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var ev struct {
		PullRequest *struct {
			Base struct {
				SHA string `json:"sha"`
			} `json:"base"`
		} `json:"pull_request"`
		MergeGroup *struct {
			BaseSHA string `json:"base_sha"`
		} `json:"merge_group"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return ""
	}
	switch {
	case ev.PullRequest != nil:
		return ev.PullRequest.Base.SHA
	case ev.MergeGroup != nil:
		return ev.MergeGroup.BaseSHA
	}
	return ""
}
