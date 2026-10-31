// Copyright (c) 2025 Reliant Labs

package github_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// The actions an automation (a recurring responder, a labeler, a reviewer)
// needs: comment edit/list, atomic labels, search, and pull request
// create/update/review reads/inline replies. Same harness as github_test.go.

// Every write returns the object's id, node id and URL. A later change will
// match them to tell which events a run itself caused, so a write that
// drops one is a regression.
func TestWritesReturnIDNodeIDAndURL(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	for _, a := range resolved.Manifest.GetActions() {
		if !a.GetMutates() || a.GetId() == "workflow.dispatch" || strings.HasPrefix(a.GetId(), "issue.labels.") || a.GetId() == "labels.create" {
			continue
		}
		props, _ := a.GetOutput().GetSchema().AsMap()["properties"].(map[string]any)
		for _, k := range []string{"id", "node_id", "html_url"} {
			assert.Contains(t, props, k, "%s output must declare %s", a.GetId(), k)
		}
	}
}

func TestMutatingClassification(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	want := map[string]bool{
		"issue.comment.update": true, "issue.comment.list": false,
		"issue.labels.add": true, "issue.labels.remove": true, "issue.search": false,
		"labels.create": true, "labels.list": false,
		"pr.create": true, "pr.update": true, "pr.reviews.list": false,
		"pr.review_comments.list": false, "pr.review_comment.reply": true,
	}
	got := map[string]bool{}
	for _, a := range resolved.Manifest.GetActions() {
		if _, ok := want[a.GetId()]; ok {
			got[a.GetId()] = a.GetMutates()
			assert.True(t, a.GetTool().GetExpose(), "%s is an agent tool", a.GetId())
		}
	}
	assert.Equal(t, want, got)
}

// Merging is a pipeline's decision, never an agent's (owner decision,
// 2026-10-30: workflows may merge, as the Reliant bot, after a review of an
// exact head). So no agent TOOL can merge, whatever it is named, and the one
// action that can (pr.merge, a workflow action node) is not a tool and
// requires the reviewed head for every arming or merge.
func TestNoAgentToolCanMerge(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	var merging []string
	for _, a := range resolved.Manifest.GetActions() {
		if strings.Contains(a.GetId(), "merge") || strings.Contains(a.GetRequest().GetPath(), "/merge") {
			merging = append(merging, a.GetId())
			assert.False(t, a.GetTool().GetExpose(), "%s can merge, so it must not be an agent tool", a.GetId())
		}
	}
	assert.Equal(t, []string{"pr.merge"}, merging, "pr.merge is the only action that merges")
	props, _ := mergeAction(t).GetParams().AsMap()["properties"].(map[string]any)
	head, _ := props["match_head_commit"].(map[string]any)
	assert.Equal(t, "^[0-9a-f]{40}$", head["pattern"], "the expected head is a full sha")
}

func TestIssueCommentUpdate(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/issues/comments/555", jsonReply(200,
		`{"id": 555, "node_id": "IC_1", "body": "Status: done", "html_url": "https://github.com/acme/app/issues/42#issuecomment-555",
		  "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-05T00:00:00Z"}`))
	out := data(t, f.run("issue.comment.update", map[string]any{"owner": "acme", "repo": "app", "comment_id": 555, "body": "Status: done"}))

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/comments/555", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"body": "Status: done"}, req.Body)
	assert.Equal(t, map[string]any{
		"id": 555.0, "node_id": "IC_1", "html_url": "https://github.com/acme/app/issues/42#issuecomment-555",
		"updated_at": "2026-01-05T00:00:00Z",
	}, out)
}

func TestIssueCommentUpdateRefusesAnEmptyBodyAndABadID(t *testing.T) {
	f := newFake(t)
	for _, p := range []map[string]any{
		{"owner": "acme", "repo": "app", "comment_id": 5, "body": ""},
		{"owner": "acme", "repo": "app", "comment_id": 0, "body": "x"},
		{"owner": "acme", "repo": "app", "body": "x"},
	} {
		_, err := f.try("issue.comment.update", p)
		assert.Error(t, err, "%v", p)
	}
	assert.Empty(t, f.requests(), "nothing is sent for a refused call")
}

func TestIssueCommentListSendsSinceAndPaginates(t *testing.T) {
	f := newFake(t)
	path := "/repos/acme/app/issues/42/comments"
	f.on("GET", path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id": 3, "node_id": "IC_3", "body": "third", "html_url": "u3", "user": {"login": "hubot", "id": 2}, "created_at": "c3", "updated_at": "u3"}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+path+`?per_page=100&since=2026-01-02T00%3A00%3A00Z&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[
		  {"id": 1, "node_id": "IC_1", "body": "first", "html_url": "u1", "user": {"login": "octocat", "id": 9}, "author_association": "MEMBER", "created_at": "c1", "updated_at": "u1"},
		  {"id": 2, "node_id": "IC_2", "body": "second", "html_url": "u2", "user": null, "created_at": "c2", "updated_at": "u2"}]`))
	})
	out := data(t, f.run("issue.comment.list", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "since": "2026-01-02T00:00:00Z"}))

	reqs := f.requests()
	require.Len(t, reqs, 2, "follows rel=next once, then stops")
	assert.Equal(t, path, reqs[0].Path)
	assert.Equal(t, "per_page=100&since=2026-01-02T00%3A00%3A00Z", sortedQuery(reqs[0].Query), "since and the largest page size")
	assert.Equal(t, "page=2&per_page=100&since=2026-01-02T00%3A00%3A00Z", sortedQuery(reqs[1].Query), "the second request is the Link header's next URL")
	for _, r := range reqs {
		assertGitHubHeaders(t, r)
	}

	items, ok := out["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 3)
	first := items[0].(map[string]any)
	assert.Equal(t, map[string]any{
		"id": 1.0, "node_id": "IC_1", "body": "first", "html_url": "u1", "user": "octocat", "user_id": 9.0,
		"author_association": "MEMBER", "created_at": "c1", "updated_at": "u1",
	}, first)
	assert.Nil(t, items[1].(map[string]any)["user"], "a deleted account is null, not an error")
	assert.Equal(t, 3.0, items[2].(map[string]any)["id"])
}

func TestIssueCommentListWithoutSinceSendsNone(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/issues/42/comments", jsonReply(200, `[]`))
	out := data(t, f.run("issue.comment.list", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42}))
	assert.Equal(t, "per_page=100", f.only().Query)
	assert.Equal(t, []any{}, out["items"])
}

func TestIssueCommentListRejectsAMalformedSince(t *testing.T) {
	f := newFake(t)
	for _, since := range []string{"yesterday", "2026-01-02", "2026-01-02 03:04:05", "2026-01-02T03:04:05Z&per_page=1"} {
		_, err := f.try("issue.comment.list", map[string]any{"owner": "acme", "repo": "app", "issue_number": 1, "since": since})
		assert.Error(t, err, since)
	}
	assert.Empty(t, f.requests())
}

// Atomic: one request that names the single label, and no read of the current
// labels first. issue.update replaces the list and races with anyone else.
func TestIssueLabelsAddIsOneAtomicRequest(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/issues/42/labels", jsonReply(200,
		`[{"id": 7, "name": "bug"}, {"id": 8, "name": "agent:in-progress"}]`))
	out := data(t, f.run("issue.labels.add", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "label": "agent:in-progress"}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/42/labels", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"labels": []any{"agent:in-progress"}}, req.Body, "exactly the one label, no read-modify-write")
	assert.Equal(t, map[string]any{
		"added": "agent:in-progress", "labels": []any{"bug", "agent:in-progress"},
		"number": 42.0, "html_url": "https://github.com/acme/app/issues/42",
	}, out)
}

func TestIssueLabelsRemoveIsOneAtomicRequest(t *testing.T) {
	f := newFake(t)
	f.on("DELETE", "/repos/acme/app/issues/42/labels/agent:in-progress", jsonReply(200, `[{"id": 7, "name": "bug"}]`))
	out := data(t, f.run("issue.labels.remove", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "label": "agent:in-progress"}))

	req := f.only()
	assert.Equal(t, "DELETE", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/42/labels/agent:in-progress", req.Path)
	assert.Empty(t, req.Body)
	assert.Equal(t, map[string]any{
		"removed": "agent:in-progress", "labels": []any{"bug"},
		"number": 42.0, "html_url": "https://github.com/acme/app/issues/42",
	}, out)
}

// A label name is data: spaces, slashes and colons must stay inside the one
// path segment, and "." / ".." (which survive escaping) are refused.
func TestIssueLabelsRemoveEscapesTheNameAsOneSegment(t *testing.T) {
	f := newFake(t)
	f.on("DELETE", "/repos/acme/app/issues/1/labels/good first issue", jsonReply(200, `[]`))
	f.on("DELETE", "/repos/acme/app/issues/1/labels/a/b", jsonReply(200, `[]`))
	data(t, f.run("issue.labels.remove", map[string]any{"owner": "acme", "repo": "app", "issue_number": 1, "label": "good first issue"}))
	data(t, f.run("issue.labels.remove", map[string]any{"owner": "acme", "repo": "app", "issue_number": 1, "label": "a/b"}))
	got := f.requests()
	require.Len(t, got, 2)
	assert.Equal(t, "/repos/acme/app/issues/1/labels/good%20first%20issue", got[0].Path)
	assert.Equal(t, "/repos/acme/app/issues/1/labels/a%2Fb", got[1].Path, "a slash does not open a new path segment")

	for _, label := range []string{".", "..", "", strings.Repeat("x", 51)} {
		_, err := f.try("issue.labels.remove", map[string]any{"owner": "acme", "repo": "app", "issue_number": 1, "label": label})
		assert.Error(t, err, "%q", label)
	}
	assert.Len(t, f.requests(), 2, "the refused names sent nothing")
}

func TestIssueLabelsRemoveOfAnAbsentLabelIs404(t *testing.T) {
	f := newFake(t)
	f.on("DELETE", "/repos/acme/app/issues/42/labels/nope", jsonReply(404, `{"message":"Label does not exist","status":"404"}`))
	res := f.run("issue.labels.remove", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "label": "nope"})
	assert.True(t, res.IsError)
	assert.Equal(t, 404, res.StatusCode)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "not found")
}

func TestIssueSearch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/search/issues", jsonReply(200, `{
	  "total_count": 2, "incomplete_results": false,
	  "items": [
	    {"id": 1, "node_id": "I_1", "number": 42, "title": "Crash", "state": "open", "html_url": "https://github.com/acme/app/issues/42",
	     "repository_url": "https://api.github.com/repos/acme/app", "user": {"login": "octocat", "id": 9},
	     "labels": [{"name": "bug"}], "comments": 3, "created_at": "c", "updated_at": "u", "closed_at": null},
	    {"id": 2, "node_id": "PR_2", "number": 7, "title": "Fix", "state": "open", "html_url": "https://github.com/acme/app/pull/7",
	     "pull_request": {"url": "https://api.github.com/repos/acme/app/pulls/7"}, "user": null, "labels": []}
	  ]}`))
	out := data(t, f.run("issue.search", map[string]any{"q": `repo:acme/app is:open "autopilot:marker"`, "sort": "updated", "order": "desc", "per_page": 50, "page": 2}))

	req := f.only()
	assert.Equal(t, "/search/issues", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, "order=desc&page=2&per_page=50&q=repo%3Aacme%2Fapp+is%3Aopen+%22autopilot%3Amarker%22&sort=updated", sortedQuery(req.Query))

	assert.Equal(t, 2.0, out["total_count"])
	items := out["items"].([]any)
	require.Len(t, items, 2)
	issue := items[0].(map[string]any)
	assert.Equal(t, "I_1", issue["node_id"])
	assert.Equal(t, []any{"bug"}, issue["labels"])
	assert.Equal(t, false, issue["is_pull_request"])
	assert.Equal(t, "octocat", issue["user"])
	pr := items[1].(map[string]any)
	assert.Equal(t, true, pr["is_pull_request"], "a pull request hit says so")
	assert.Nil(t, pr["user"])
}

func TestIssueSearchDefaultsAndLimits(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/search/issues", jsonReply(200, `{"total_count": 0, "items": []}`))
	data(t, f.run("issue.search", map[string]any{"q": "repo:acme/app"}))
	assert.Equal(t, "page=1&per_page=30&q=repo%3Aacme%2Fapp", sortedQuery(f.only().Query), "no sort or order unless asked")

	for _, p := range []map[string]any{{"q": ""}, {"q": "x", "per_page": 101}, {"q": "x", "page": 0}, {"q": "x", "sort": "reactions"}} {
		_, err := f.try("issue.search", p)
		assert.Error(t, err, "%v", p)
	}
	assert.Len(t, f.requests(), 1)
}

const prCreatedJSON = `{
  "id": 9002, "node_id": "PR_kwDO2", "number": 18, "state": "open", "title": "Fix the crash (#42)", "draft": true,
  "html_url": "https://github.com/acme/app/pull/18",
  "head": {"ref": "agent/issue-42", "sha": "abc123"}, "base": {"ref": "main", "sha": "def456"}
}`

func TestPullRequestCreate(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls", jsonReply(201, prCreatedJSON))
	out := data(t, f.run("pr.create", map[string]any{
		"owner": "acme", "repo": "app", "head": "agent/issue-42", "base": "main",
		"title": "Fix the crash (#42)", "body": "Fixes #42", "draft": true,
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/pulls", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"head": "agent/issue-42", "base": "main", "title": "Fix the crash (#42)", "body": "Fixes #42", "draft": true,
	}, req.Body)
	assert.Equal(t, map[string]any{
		"number": 18.0, "id": 9002.0, "node_id": "PR_kwDO2", "html_url": "https://github.com/acme/app/pull/18",
		"title": "Fix the crash (#42)", "state": "open", "draft": true, "head_sha": "abc123", "base_ref": "main",
	}, out)
}

// Optional fields the caller leaves out are absent: draft is not sent as
// false, nor body as null.
func TestPullRequestCreateSendsOnlyWhatWasGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls", jsonReply(201, prCreatedJSON))
	f.run("pr.create", map[string]any{"owner": "acme", "repo": "app", "head": "octocat:fix", "base": "main", "title": "t"})
	assert.Equal(t, map[string]any{"head": "octocat:fix", "base": "main", "title": "t"}, f.only().Body)
}

func TestPullRequestCreateHasNoLabelsParam(t *testing.T) {
	f := newFake(t)
	_, err := f.try("pr.create", map[string]any{"owner": "acme", "repo": "app", "head": "h", "base": "main", "title": "t", "labels": []any{"x"}})
	assert.Error(t, err, "GitHub's pulls API takes no labels; they go through issue.labels.add")
	assert.Empty(t, f.requests())
}

func TestPullRequestCreateReportsAnExistingPullRequest(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls", jsonReply(422,
		`{"message":"Validation Failed","errors":[{"resource":"PullRequest","code":"custom","message":"A pull request already exists for acme:fix."}],"status":"422"}`))
	res := f.run("pr.create", map[string]any{"owner": "acme", "repo": "app", "head": "fix", "base": "main", "title": "t"})
	assert.True(t, res.IsError)
	assert.Equal(t, 422, res.StatusCode)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "A pull request already exists")
}

func TestPullRequestUpdateSendsOnlyTheChangedFields(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/pulls/18", jsonReply(200, strings.Replace(prCreatedJSON, `"state": "open"`, `"state": "closed"`, 1)))
	out := data(t, f.run("pr.update", map[string]any{"owner": "acme", "repo": "app", "pull_number": 18, "state": "closed", "base": "release-1.2"}))

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assert.Equal(t, "/repos/acme/app/pulls/18", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"state": "closed", "base": "release-1.2"}, req.Body, "no title or body: null would clear them")
	assert.Equal(t, "closed", out["state"])
	assert.Equal(t, "PR_kwDO2", out["node_id"])
	assert.Equal(t, 9002.0, out["id"])
	assert.Equal(t, "https://github.com/acme/app/pull/18", out["html_url"])
}

func TestPullRequestUpdateCannotMerge(t *testing.T) {
	f := newFake(t)
	for _, p := range []map[string]any{
		{"owner": "acme", "repo": "app", "pull_number": 1, "state": "merged"},
		{"owner": "acme", "repo": "app", "pull_number": 1, "merged": true},
		{"owner": "acme", "repo": "app", "pull_number": 1, "merge_method": "squash"},
	} {
		_, err := f.try("pr.update", p)
		assert.Error(t, err, "%v", p)
	}
	assert.Empty(t, f.requests())
}

func TestPullRequestReviewsListPaginates(t *testing.T) {
	f := newFake(t)
	path := "/repos/acme/app/pulls/17/reviews"
	f.on("GET", path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id": 3, "node_id": "PRR_3", "state": "COMMENTED", "body": null, "user": {"login": "hubot", "id": 2}, "commit_id": "c3", "submitted_at": "s3"}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+path+`?per_page=100&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[
		  {"id": 1, "node_id": "PRR_1", "state": "APPROVED", "body": "LGTM", "html_url": "h1", "user": {"login": "octocat", "id": 9}, "author_association": "MEMBER", "commit_id": "c1", "submitted_at": "s1"},
		  {"id": 2, "node_id": "PRR_2", "state": "CHANGES_REQUESTED", "body": "No", "user": null, "commit_id": "c2", "submitted_at": "s2"}]`))
	})
	out := data(t, f.run("pr.reviews.list", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17}))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "per_page=100", reqs[0].Query)
	assert.Equal(t, "page=2&per_page=100", sortedQuery(reqs[1].Query))
	items := out["items"].([]any)
	require.Len(t, items, 3)
	assert.Equal(t, map[string]any{
		"id": 1.0, "node_id": "PRR_1", "state": "APPROVED", "body": "LGTM", "html_url": "h1", "user": "octocat", "user_id": 9.0,
		"author_association": "MEMBER", "commit_id": "c1", "submitted_at": "s1",
	}, items[0])
	assert.Nil(t, items[1].(map[string]any)["user"])
	assert.Equal(t, "", items[2].(map[string]any)["body"], "a null review body reads as empty text")
}

func TestPullRequestReviewCommentsListSendsSinceAndPaginates(t *testing.T) {
	f := newFake(t)
	path := "/repos/acme/app/pulls/17/comments"
	f.on("GET", path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`[{"id": 12, "node_id": "PRRC_12", "pull_request_review_id": 50, "in_reply_to_id": 11, "path": "a.go", "line": null, "original_line": 4,
			  "side": "RIGHT", "body": "done", "user": {"login": "reliant", "id": 7}, "created_at": "c", "updated_at": "u"}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+path+`?per_page=100&since=2026-01-02T00%3A00%3A00Z&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[{"id": 11, "node_id": "PRRC_11", "pull_request_review_id": 50, "path": "a.go", "line": 12, "original_line": 12, "side": "RIGHT",
		  "commit_id": "abc", "diff_hunk": "@@ -1 +1 @@", "body": "why?", "html_url": "h", "user": {"login": "hubot", "id": 2}, "author_association": "MEMBER",
		  "created_at": "c", "updated_at": "u"}]`))
	})
	out := data(t, f.run("pr.review_comments.list", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "since": "2026-01-02T00:00:00Z"}))

	reqs := f.only2(t)
	assert.Equal(t, "per_page=100&since=2026-01-02T00%3A00%3A00Z", sortedQuery(reqs[0].Query))
	assert.Equal(t, "page=2&per_page=100&since=2026-01-02T00%3A00%3A00Z", sortedQuery(reqs[1].Query))
	items := out["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, 11.0, first["id"])
	assert.Equal(t, 50.0, first["review_id"])
	assert.Nil(t, first["in_reply_to_id"], "the first comment of a thread replies to nothing")
	assert.Equal(t, "a.go", first["path"])
	assert.Equal(t, 12.0, first["line"])
	assert.Equal(t, "@@ -1 +1 @@", first["diff_hunk"])
	assert.Equal(t, "hubot", first["user"])
	reply := items[1].(map[string]any)
	assert.Equal(t, 11.0, reply["in_reply_to_id"])
	assert.Nil(t, reply["line"], "an outdated comment has no line")
	assert.Equal(t, 4.0, reply["original_line"])
}

func TestPullRequestReviewCommentReply(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls/17/comments/11/replies", jsonReply(201,
		`{"id": 13, "node_id": "PRRC_13", "in_reply_to_id": 11, "html_url": "https://github.com/acme/app/pull/17#discussion_r13", "created_at": "2026-01-06T00:00:00Z"}`))
	out := data(t, f.run("pr.review_comment.reply", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "comment_id": 11, "body": "Fixed in abc1234."}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/pulls/17/comments/11/replies", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"body": "Fixed in abc1234."}, req.Body)
	assert.Equal(t, map[string]any{
		"id": 13.0, "node_id": "PRRC_13", "html_url": "https://github.com/acme/app/pull/17#discussion_r13",
		"in_reply_to_id": 11.0, "created_at": "2026-01-06T00:00:00Z",
	}, out)
}

// Pagination stops at max_pages instead of walking a runaway Link chain.
func TestPaginatedListsStopAtMaxPages(t *testing.T) {
	for _, tc := range []struct {
		id, path string
		params   map[string]any
	}{
		{"issue.comment.list", "/repos/acme/app/issues/1/comments", map[string]any{"owner": "acme", "repo": "app", "issue_number": 1}},
		{"pr.reviews.list", "/repos/acme/app/pulls/1/reviews", map[string]any{"owner": "acme", "repo": "app", "pull_number": 1}},
		{"pr.review_comments.list", "/repos/acme/app/pulls/1/comments", map[string]any{"owner": "acme", "repo": "app", "pull_number": 1}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			f := newFake(t)
			n := 0
			f.on("GET", tc.path, func(w http.ResponseWriter, _ *http.Request) {
				n++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Link", fmt.Sprintf(`<%s%s?per_page=100&page=%d>; rel="next"`, f.srv.URL, tc.path, n+1))
				_, _ = w.Write([]byte(`[]`))
			})
			data(t, f.run(tc.id, tc.params))
			assert.Equal(t, 10, len(f.requests()), "bounded at the manifest's max_pages")
		})
	}
}

// the page-2 helper: the fake must have seen exactly two requests.
func (f *fakeGitHub) only2(t *testing.T) []recorded {
	t.Helper()
	got := f.requests()
	require.Len(t, got, 2)
	return got
}

// Setup creates its labels every time it runs, so a label the repository
// already has must be a success that changes nothing.
func TestLabelsCreateSendsNameColorAndDescription(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/labels", jsonReply(201,
		`{"id": 208045946, "node_id": "LA_1", "name": "agent:ready", "color": "0e8a16", "description": "An agent may pick this up", "default": false}`))
	out := data(t, f.run("labels.create", map[string]any{
		"owner": "acme", "repo": "app", "name": "agent:ready", "color": "0e8a16", "description": "An agent may pick this up",
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/labels", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"name": "agent:ready", "color": "0e8a16", "description": "An agent may pick this up"}, req.Body)
	assert.Equal(t, map[string]any{
		"name": "agent:ready", "color": "0e8a16", "description": "An agent may pick this up",
		"id": 208045946.0, "node_id": "LA_1", "created": true,
	}, out)
}

func TestLabelsCreateOmitsWhatWasNotGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/labels", jsonReply(201, `{"id": 1, "name": "triage", "color": "ededed", "description": null}`))
	data(t, f.run("labels.create", map[string]any{"owner": "acme", "repo": "app", "name": "triage"}))
	assert.Equal(t, map[string]any{"name": "triage"}, f.only().Body, "no null color or description is sent")
}

func TestLabelsCreateIsIdempotentWhenTheLabelExists(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/labels", jsonReply(422,
		`{"message":"Validation Failed","errors":[{"resource":"Label","code":"already_exists","field":"name"}],"documentation_url":"https://docs.github.com/rest/issues/labels#create-a-label","status":"422"}`))
	res := f.run("labels.create", map[string]any{"owner": "acme", "repo": "app", "name": "Agent:Ready", "color": "0e8a16"})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, map[string]any{
		"name": "Agent:Ready", "color": nil, "description": nil, "id": nil, "node_id": nil, "created": false,
	}, data(t, res))
	assert.Len(t, f.requests(), 1, "one request: no read, no update of the existing label")
}

func TestLabelsCreateStillFailsOnOtherErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		wantErr string
	}{
		"invalid color":  {422, `{"message":"Validation Failed","errors":[{"resource":"Label","code":"invalid","field":"color"}],"status":"422"}`, "color"},
		"422 w/o errors": {422, `{"message":"Validation Failed","status":"422"}`, "validation failed"},
		"no permission":  {403, `{"message":"Resource not accessible by integration"}`, "refused"},
		"not found":      {404, `{"message":"Not Found"}`, "not found"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.on("POST", "/repos/acme/app/labels", jsonReply(tc.status, tc.body))
			res := f.run("labels.create", map[string]any{"owner": "acme", "repo": "app", "name": "x"})
			assert.True(t, res.IsError)
			assert.Equal(t, tc.status, res.StatusCode)
			assert.Contains(t, strings.ToLower(res.Content), tc.wantErr)
		})
	}
}

func TestLabelsCreateRefusesBadInput(t *testing.T) {
	f := newFake(t)
	for _, in := range []map[string]any{
		{"owner": "acme", "repo": "app", "name": ""},
		{"owner": "acme", "repo": "app", "name": "x", "color": "#0e8a16"},
		{"owner": "acme", "repo": "app", "name": "x", "color": "green"},
		{"owner": "acme", "repo": "app", "name": strings.Repeat("x", 51)},
		{"owner": "acme", "repo": "app", "name": "x", "description": strings.Repeat("d", 101)},
	} {
		_, err := f.try("labels.create", in)
		assert.Error(t, err, "%v", in)
	}
	assert.Empty(t, f.requests())
}

func TestLabelsListFollowsPagination(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/labels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") != "2" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/acme/app/labels?per_page=100&page=2>; rel="next"`, "https://"+r.Host))
			_, _ = w.Write([]byte(`[{"id": 1, "node_id": "LA_1", "name": "bug", "color": "d73a4a", "description": "Something broken", "default": true}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id": 2, "node_id": "LA_2", "name": "agent:ready", "color": "0e8a16", "description": null, "default": false}]`))
	})
	out := data(t, f.run("labels.list", map[string]any{"owner": "acme", "repo": "app"}))
	assert.Equal(t, []any{
		map[string]any{"id": 1.0, "node_id": "LA_1", "name": "bug", "color": "d73a4a", "description": "Something broken", "default": true},
		map[string]any{"id": 2.0, "node_id": "LA_2", "name": "agent:ready", "color": "0e8a16", "description": nil, "default": false},
	}, out["items"])
	got := f.requests()
	require.Len(t, got, 2)
	assert.Equal(t, "per_page=100", got[0].Query)
}

func TestLabelsListOfARepositoryWithNoLabelsIsAnEmptyList(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/labels", jsonReply(200, `[]`))
	out := data(t, f.run("labels.list", map[string]any{"owner": "acme", "repo": "app"}))
	assert.Equal(t, []any{}, out["items"])
}
