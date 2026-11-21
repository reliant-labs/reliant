// Copyright (c) 2025 Reliant Labs

package github_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// What a ship step reads and writes: the rules on the branch it ships from,
// what changed since the last release, the commit statuses of the head, and
// the release it publishes. Same harness as github_test.go.

// The reads are agent tools; a release is the pipeline's record, so its writes
// are workflow steps only, as pr.merge and check_run.create are.
func TestShipActionsMutatesAndExposure(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/compare@1")
	require.NoError(t, err)
	got := map[string][2]bool{}
	for _, a := range resolved.Manifest.GetActions() {
		switch a.GetId() {
		case "branch.rules", "compare", "commit.statuses", "release.create", "release.update":
			got[a.GetId()] = [2]bool{a.GetMutates(), a.GetTool().GetExpose()}
		}
	}
	assert.Equal(t, map[string][2]bool{
		"branch.rules":    {false, true},
		"compare":         {false, true},
		"commit.statuses": {false, true},
		"release.create":  {true, false},
		"release.update":  {true, false},
	}, got, "[mutates, exposed as a tool]")
}

func TestBranchRulesListsEveryRuleAcrossPages(t *testing.T) {
	f := newFake(t)
	path := "/repos/acme/app/rules/branches/main"
	f.on("GET", path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"type": "deletion", "ruleset_source_type": "Organization",
			  "ruleset_source": "acme", "ruleset_id": 7}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+path+`?per_page=100&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[
		  {"type": "required_status_checks", "ruleset_source_type": "Repository", "ruleset_source": "acme/app", "ruleset_id": 42,
		   "parameters": {"strict_required_status_checks_policy": false,
		                  "required_status_checks": [{"context": "CI result", "integration_id": 15368}]}},
		  {"type": "pull_request", "ruleset_source_type": "Repository", "ruleset_source": "acme/app", "ruleset_id": 42,
		   "parameters": {"required_approving_review_count": 1}}]`))
	})
	out := data(t, f.run("branch.rules", map[string]any{"owner": "acme", "repo": "app", "branch": "main"}))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, path, reqs[0].Path)
	assert.Equal(t, "per_page=100", reqs[0].Query)
	assertGitHubHeaders(t, reqs[0])
	assert.Nil(t, reqs[0].Body)

	items := out["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, map[string]any{
		"type": "required_status_checks", "ruleset_id": 42.0, "ruleset_source_type": "Repository", "ruleset_source": "acme/app",
		"parameters": map[string]any{"strict_required_status_checks_policy": false,
			"required_status_checks": []any{map[string]any{"context": "CI result", "integration_id": 15368.0}}},
	}, items[0])
	assert.Equal(t, map[string]any{
		"type": "deletion", "ruleset_id": 7.0, "ruleset_source_type": "Organization", "ruleset_source": "acme", "parameters": nil,
	}, items[2], "a rule with no settings has null parameters")
}

// A branch with a slash is one escaped path segment; a dot segment never
// reaches GitHub.
func TestBranchRulesEscapesTheBranch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/rules/branches/release/1.x", jsonReply(200, `[]`))
	out := data(t, f.run("branch.rules", map[string]any{"owner": "acme", "repo": "app", "branch": "release/1.x"}))
	assert.Equal(t, "/repos/acme/app/rules/branches/release%2F1.x", f.only().Path)
	assert.Equal(t, []any{}, out["items"], "a branch no ruleset covers has no rules")

	for _, bad := range []string{"..", "release/..", "a//b", "", "/main"} {
		_, err := f.try("branch.rules", map[string]any{"owner": "acme", "repo": "app", "branch": bad})
		assert.Error(t, err, "%q", bad)
	}
	assert.Len(t, f.requests(), 1, "nothing is sent for a refused branch")
}

const compareJSON = `{
  "url": "https://api.github.com/repos/acme/app/compare/v1.4.0...main",
  "html_url": "https://github.com/acme/app/compare/v1.4.0...main",
  "status": "ahead", "ahead_by": 3, "behind_by": 0, "total_commits": 3,
  "merge_base_commit": {"sha": "bbb222"},
  "commits": [
    {"sha": "aaa111", "author": {"login": "octocat"},
     "commit": {"message": "Add retries (#41)\n\nLong body that is not returned.", "author": {"name": "Octo"}}},
    {"sha": "aaa222", "author": null, "commit": {"message": "Fix the save crash\r\n\r\nWindows line endings."}},
    {"sha": "aaa333", "author": {"login": "hubot"}, "commit": {"message": "` + "x" + `"}}
  ],
  "files": [{"filename": "a.go", "patch": "@@ -1 +1 @@"}]
}`

func TestCompareTrimsTheComparison(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/compare/v1.4.0...main", jsonReply(200, compareJSON))
	out := data(t, f.run("compare", map[string]any{"owner": "acme", "repo": "app", "base": "v1.4.0", "head": "main"}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app/compare/v1.4.0...main", req.Path)
	assert.Equal(t, "page=1&per_page=100", sortedQuery(req.Query))
	assertGitHubHeaders(t, req)

	assert.Equal(t, map[string]any{
		"status": "ahead", "ahead_by": 3.0, "behind_by": 0.0, "total_commits": 3.0,
		"merge_base_sha": "bbb222", "html_url": "https://github.com/acme/app/compare/v1.4.0...main",
		"commits": []any{
			map[string]any{"sha": "aaa111", "author": "octocat", "message": "Add retries (#41)"},
			map[string]any{"sha": "aaa222", "author": nil, "message": "Fix the save crash"},
			map[string]any{"sha": "aaa333", "author": "hubot", "message": "x"},
		},
		"has_more": false,
	}, out, "no files, no message bodies")
}

// One page at a time: has_more says a later page holds more, and a long
// first line is capped.
func TestComparePagesAndCaps(t *testing.T) {
	f := newFake(t)
	long := strings.Repeat("é", 400)
	f.on("GET", "/repos/acme/app/compare/release/1.x...octocat:main", jsonReply(200, `{
	  "status": "diverged", "ahead_by": 120, "behind_by": 2, "total_commits": 120,
	  "commits": [{"sha": "c1", "commit": {"message": "`+long+`"}}]}`))
	out := data(t, f.run("compare", map[string]any{
		"owner": "acme", "repo": "app", "base": "release/1.x", "head": "octocat:main", "limit": 50, "page": 2,
	}))

	req := f.only()
	assert.Equal(t, "/repos/acme/app/compare/release%2F1.x...octocat:main", req.Path, "each ref is escaped; the ... is not")
	assert.Equal(t, "page=2&per_page=50", sortedQuery(req.Query))
	assert.Equal(t, true, out["has_more"], "2 pages of 50 cover 100 of 120 commits")
	assert.Nil(t, out["merge_base_sha"])
	commits := out["commits"].([]any)
	require.Len(t, commits, 1)
	assert.Equal(t, strings.Repeat("é", 300), commits[0].(map[string]any)["message"], "capped at 300 characters, not bytes")
}

func TestCompareRefusesBadInput(t *testing.T) {
	f := newFake(t)
	base := map[string]any{"owner": "acme", "repo": "app", "base": "v1", "head": "main"}
	with := func(k string, v any) map[string]any {
		p := map[string]any{}
		for kk, vv := range base {
			p[kk] = vv
		}
		p[k] = v
		return p
	}
	for name, p := range map[string]map[string]any{
		"dot-dot base":  with("base", ".."),
		"dot-dot head":  with("head", "main/.."),
		"empty head":    with("head", ""),
		"limit too big": with("limit", 101),
		"limit zero":    with("limit", 0),
		"page zero":     with("page", 0),
		"no head":       {"owner": "acme", "repo": "app", "base": "v1"},
	} {
		_, err := f.try("compare", p)
		assert.Error(t, err, name)
	}
	assert.Empty(t, f.requests())
}

func TestCommitStatusesCombinedAndPerContext(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/commits/"+headSHA+"/status", jsonReply(200, `{
	  "state": "failure", "sha": "`+headSHA+`", "total_count": 2,
	  "statuses": [
	    {"context": "ci/circleci: build", "state": "failure", "description": "Tests failed",
	     "target_url": "https://circleci.com/1", "creator": {"login": "circleci"},
	     "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:05:00Z"},
	    {"context": "deploy/preview", "state": "success", "description": null, "target_url": null, "creator": null,
	     "created_at": "c", "updated_at": "u"}
	  ],
	  "repository": {"full_name": "acme/app"}, "commit_url": "x", "url": "y"}`))
	out := data(t, f.run("commit.statuses", map[string]any{"owner": "acme", "repo": "app", "ref": headSHA}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app/commits/"+headSHA+"/status", req.Path)
	assert.Equal(t, "per_page=100", req.Query)
	assertGitHubHeaders(t, req)

	assert.Equal(t, map[string]any{
		"state": "failure", "sha": headSHA, "total_count": 2.0, "truncated": false,
		"statuses": []any{
			map[string]any{"context": "ci/circleci: build", "state": "failure", "description": "Tests failed",
				"target_url": "https://circleci.com/1", "creator": "circleci",
				"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:05:00Z"},
			map[string]any{"context": "deploy/preview", "state": "success", "description": nil, "target_url": nil,
				"creator": nil, "created_at": "c", "updated_at": "u"},
		},
	}, out)
}

// A ref with no statuses is pending with an empty list; more contexts than the
// one page holds is said, not hidden. A branch is one escaped segment.
func TestCommitStatusesEmptyAndTruncated(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/commits/feat/x/status", jsonReply(200,
		`{"state": "pending", "sha": "abc", "total_count": 0, "statuses": []}`))
	out := data(t, f.run("commit.statuses", map[string]any{"owner": "acme", "repo": "app", "ref": "feat/x"}))
	assert.Equal(t, "/repos/acme/app/commits/feat%2Fx/status", f.only().Path)
	assert.Equal(t, "pending", out["state"])
	assert.Equal(t, []any{}, out["statuses"])
	assert.Equal(t, false, out["truncated"])

	g := newFake(t)
	g.on("GET", "/repos/acme/app/commits/main/status", jsonReply(200,
		`{"state": "success", "sha": "abc", "total_count": 101, "statuses": [{"context": "a", "state": "success"}]}`))
	out = data(t, g.run("commit.statuses", map[string]any{"owner": "acme", "repo": "app", "ref": "main"}))
	assert.Equal(t, true, out["truncated"])

	_, err := g.try("commit.statuses", map[string]any{"owner": "acme", "repo": "app", "ref": "../x"})
	assert.Error(t, err)
}

// Until the Reliant App is granted Commit statuses (read), GitHub refuses the
// call: a permission error naming it, not a retry.
func TestCommitStatusesWithoutThePermission(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"message": "Resource not accessible by integration", "status": "403"}`))
	})
	res := f.run("commit.statuses", map[string]any{"owner": "acme", "repo": "app", "ref": "main"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "Resource not accessible by integration")
	assert.Contains(t, res.Content, "permission")
}

const releaseJSON = `{"id": 1234567, "node_id": "RE_1", "html_url": "https://github.com/acme/app/releases/tag/v1.5.0",
  "tag_name": "v1.5.0", "name": "v1.5.0", "target_commitish": "main", "draft": false, "prerelease": false,
  "created_at": "2026-02-01T00:00:00Z", "published_at": "2026-02-01T00:01:00Z",
  "upload_url": "https://uploads.github.com/repos/acme/app/releases/1234567/assets{?name,label}",
  "author": {"login": "reliant-labs[bot]"}, "assets": [], "body": "notes"}`

func TestReleaseCreateSendsWhatWasGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/releases", jsonReply(201, releaseJSON))
	out := data(t, f.run("release.create", map[string]any{
		"owner": "acme", "repo": "app", "tag_name": "v1.5.0", "target_commitish": "main", "name": "v1.5.0",
		"body": "## What's new", "draft": false, "prerelease": false, "generate_release_notes": true, "make_latest": "true",
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/releases", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"tag_name": "v1.5.0", "target_commitish": "main", "name": "v1.5.0", "body": "## What's new",
		"draft": false, "prerelease": false, "generate_release_notes": true, "make_latest": "true",
	}, req.Body)
	assert.Equal(t, map[string]any{
		"id": 1234567.0, "node_id": "RE_1", "html_url": "https://github.com/acme/app/releases/tag/v1.5.0",
		"tag_name": "v1.5.0", "name": "v1.5.0", "target_commitish": "main", "draft": false, "prerelease": false,
		"created_at": "2026-02-01T00:00:00Z", "published_at": "2026-02-01T00:01:00Z",
	}, out)
}

func TestReleaseCreateMinimalDraft(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/releases", jsonReply(201,
		`{"id": 9, "html_url": "https://github.com/acme/app/releases/tag/untagged-1", "tag_name": "v2.0.0",
		  "name": null, "draft": true, "prerelease": false, "published_at": null}`))
	out := data(t, f.run("release.create", map[string]any{"owner": "acme", "repo": "app", "tag_name": "v2.0.0", "draft": true}))
	assert.Equal(t, map[string]any{"tag_name": "v2.0.0", "draft": true}, f.only().Body, "GitHub's defaults apply to the rest")
	assert.Equal(t, true, out["draft"])
	assert.Nil(t, out["published_at"], "a draft is not published")
	assert.Nil(t, out["name"])

	for name, p := range map[string]map[string]any{
		"no tag":            {"owner": "acme", "repo": "app"},
		"empty tag":         {"owner": "acme", "repo": "app", "tag_name": ""},
		"unknown latest":    {"owner": "acme", "repo": "app", "tag_name": "v2", "make_latest": "yes"},
		"boolean latest":    {"owner": "acme", "repo": "app", "tag_name": "v2", "make_latest": true},
		"unknown parameter": {"owner": "acme", "repo": "app", "tag_name": "v2", "discussion": "x"},
	} {
		_, err := f.try("release.create", p)
		assert.Error(t, err, name)
	}
	assert.Len(t, f.requests(), 1, "nothing is sent for a refused call")
}

func TestReleaseUpdatePublishesADraft(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/releases/1234567", jsonReply(200, releaseJSON))
	out := data(t, f.run("release.update", map[string]any{
		"owner": "acme", "repo": "app", "release_id": 1234567, "draft": false, "body": "Final notes",
	}))

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"draft": false, "body": "Final notes"}, req.Body, "only what changes")
	assert.Equal(t, 1234567.0, out["id"])
	assert.Equal(t, "https://github.com/acme/app/releases/tag/v1.5.0", out["html_url"])

	for name, p := range map[string]map[string]any{
		"no id":                  {"owner": "acme", "repo": "app", "draft": false},
		"zero id":                {"owner": "acme", "repo": "app", "release_id": 0},
		"generate notes on edit": {"owner": "acme", "repo": "app", "release_id": 1, "generate_release_notes": true},
	} {
		_, err := f.try("release.update", p)
		assert.Error(t, err, name)
	}
	assert.Len(t, f.requests(), 1)
}
