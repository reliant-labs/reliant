// Copyright (c) 2025 Reliant Labs

// Package github_test drives every action in the embedded `github` manifest
// through the real declarative runner against an httptest fake of the GitHub
// REST API. The manifest is the one the binary ships (catalog.MustBuiltin),
// with only its base_url pointed at the fake, so these tests pin the request
// each action sends, the output it selects, and how it classifies failures.
//
// Request and response shapes come from GitHub's REST reference
// (docs.github.com/en/rest, API version 2022-11-28).
package github_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
)

const (
	apiVersion = "2022-11-28"
	accept     = "application/vnd.github+json"
	token      = "ghu_fake_token_5be1"
)

// recorded is one request the fake received.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   map[string]any
}

// fakeGitHub answers each "METHOD /path" with a canned handler and records
// every request it sees.
type fakeGitHub struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	got    []recorded
	routes map[string]http.HandlerFunc
}

func newFake(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, routes: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Header: r.Header.Clone()}
	if len(raw) > 0 {
		require.NoError(f.t, json.Unmarshal(raw, &rec.Body), "request body must be JSON: %s", raw)
	}
	f.mu.Lock()
	f.got = append(f.got, rec)
	h, ok := f.routes[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
		return
	}
	h(w, r)
}

func (f *fakeGitHub) on(method, path string, h http.HandlerFunc) { f.routes[method+" "+path] = h }

// json answers with status and a JSON body.
func jsonReply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func (f *fakeGitHub) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeGitHub) only() recorded {
	f.t.Helper()
	got := f.requests()
	require.Len(f.t, got, 1, "exactly one request")
	return got[0]
}

// staticCred stands in for a resolved GitHub connection: a bearer token,
// applied only by Apply and scrubbed from results.
type staticCred struct{}

func (staticCred) ConnectionID() string      { return "conn_github" }
func (staticCred) Params() map[string]string { return nil }
func (staticCred) Scrub(s string) string     { return strings.ReplaceAll(s, token, "[redacted]") }
func (staticCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type staticSource struct{}

func (staticSource) Credential(context.Context, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return staticCred{}, nil
}

// action resolves github/<id>@1 from the embedded catalog and points a clone
// of its manifest at the fake: the shipped declaration, a different host.
func (f *fakeGitHub) action(id string) (*reliantv1.IntegrationManifest, *reliantv1.ActionSpec) {
	f.t.Helper()
	resolved, err := catalog.MustBuiltin().Resolve("github/" + id + "@1")
	require.NoError(f.t, err)
	m := proto.Clone(resolved.Manifest).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = f.srv.URL
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			return m, a
		}
	}
	f.t.Fatalf("action %s vanished from the clone", id)
	return nil, nil
}

func (f *fakeGitHub) runner() *httpaction.Runner {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	return httpaction.NewRunner(g).WithRootCAs(pool)
}

// run executes github/<id>@1 with params, authenticated like a real call.
func (f *fakeGitHub) run(id string, params map[string]any) *httpaction.Result {
	f.t.Helper()
	res, err := f.try(id, params)
	require.NoError(f.t, err)
	return res
}

func (f *fakeGitHub) try(id string, params map[string]any) (*httpaction.Result, error) {
	m, a := f.action(id)
	return f.runner().RunAuthenticated(context.Background(), m, a, params, staticSource{}, httpaction.CallSite{RunID: "run-1", NodeID: "n"})
}

// assertGitHubHeaders checks what every GitHub request must carry.
func assertGitHubHeaders(t *testing.T, r recorded) {
	t.Helper()
	assert.Equal(t, accept, r.Header.Get("Accept"))
	assert.Equal(t, apiVersion, r.Header.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
	assert.NotEmpty(t, r.Header.Get("User-Agent"), "GitHub rejects requests without a User-Agent")
}

func data(t *testing.T, res *httpaction.Result) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "unexpected error result: %s", res.Content)
	return res.Data
}

// ---------------------------------------------------------------------------
// Request shape and output mapping, one test per action.
// ---------------------------------------------------------------------------

const issueJSON = `{
  "id": 1, "node_id": "I_1", "number": 42, "title": "Crash on save", "body": "Steps…",
  "state": "open", "state_reason": null, "locked": false, "comments": 3,
  "html_url": "https://github.com/acme/app/issues/42",
  "url": "https://api.github.com/repos/acme/app/issues/42",
  "user": {"login": "octocat", "id": 9},
  "labels": [{"id": 7, "name": "bug", "color": "f00"}, {"id": 8, "name": "p1", "color": "0f0"}],
  "assignees": [{"login": "hubot", "id": 2}],
  "created_at": "2026-01-02T03:04:05Z", "updated_at": "2026-01-03T03:04:05Z", "closed_at": null
}`

func TestIssueCreate(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/issues", jsonReply(201, issueJSON))
	out := data(t, f.run("issue.create", map[string]any{
		"owner": "acme", "repo": "app", "title": "Crash on save", "body": "Steps…",
		"labels": []any{"bug", "p1"}, "assignees": []any{"hubot"},
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/issues", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Equal(t, map[string]any{
		"title": "Crash on save", "body": "Steps…",
		"labels": []any{"bug", "p1"}, "assignees": []any{"hubot"},
	}, req.Body)

	assert.Equal(t, map[string]any{
		"number": 42.0, "id": 1.0, "title": "Crash on save", "state": "open",
		"html_url": "https://github.com/acme/app/issues/42",
	}, out)
}

// Optional fields the caller leaves out are absent from the body, not null.
func TestIssueCreateSendsOnlyWhatWasGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/issues", jsonReply(201, issueJSON))
	f.run("issue.create", map[string]any{"owner": "acme", "repo": "app", "title": "Only a title"})
	assert.Equal(t, map[string]any{"title": "Only a title"}, f.only().Body)
}

func TestIssueGet(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/issues/42", jsonReply(200, issueJSON))
	out := data(t, f.run("issue.get", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/42", req.Path)
	assertGitHubHeaders(t, req)
	assert.Nil(t, req.Body)

	assert.Equal(t, map[string]any{
		"number": 42.0, "id": 1.0, "title": "Crash on save", "body": "Steps…",
		"state": "open", "state_reason": nil, "locked": false, "comments": 3.0,
		"html_url": "https://github.com/acme/app/issues/42",
		"user":     "octocat", "labels": []any{"bug", "p1"}, "assignees": []any{"hubot"},
		"is_pull_request": false,
		"created_at":      "2026-01-02T03:04:05Z", "updated_at": "2026-01-03T03:04:05Z", "closed_at": nil,
	}, out)
}

// GitHub serves pull requests through the issues API too; the output says so.
func TestIssueGetFlagsAPullRequest(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/issues/7", jsonReply(200,
		`{"id":2,"number":7,"title":"PR","body":null,"state":"open","html_url":"h","user":null,"labels":[],"assignees":null,"comments":0,"locked":false,
		  "created_at":"c","updated_at":"u","closed_at":null,"pull_request":{"url":"https://api.github.com/repos/acme/app/pulls/7"}}`))
	out := data(t, f.run("issue.get", map[string]any{"owner": "acme", "repo": "app", "issue_number": 7}))
	assert.Equal(t, true, out["is_pull_request"])
	assert.Nil(t, out["user"], "a deleted (ghost) author is null, not an error")
	assert.Nil(t, out["body"])
	assert.Equal(t, []any{}, out["assignees"])
}

func TestIssueComment(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/issues/42/comments", jsonReply(201,
		`{"id": 555, "node_id": "IC_1", "body": "Looking into it",
		  "html_url": "https://github.com/acme/app/issues/42#issuecomment-555",
		  "user": {"login": "reliant-labs[bot]"}, "created_at": "2026-01-04T00:00:00Z", "updated_at": "2026-01-04T00:00:00Z"}`))
	out := data(t, f.run("issue.comment", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "body": "Looking into it"}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/42/comments", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"body": "Looking into it"}, req.Body)

	assert.Equal(t, map[string]any{
		"id": 555.0, "html_url": "https://github.com/acme/app/issues/42#issuecomment-555",
		"created_at": "2026-01-04T00:00:00Z",
	}, out)
}

func TestIssueUpdateSendsOnlyTheChangedFields(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/issues/42", jsonReply(200,
		`{"id":1,"number":42,"title":"Crash on save","state":"closed","state_reason":"completed",
		  "html_url":"https://github.com/acme/app/issues/42","labels":[{"name":"bug"}],"assignees":[]}`))
	out := data(t, f.run("issue.update", map[string]any{
		"owner": "acme", "repo": "app", "issue_number": 42,
		"state": "closed", "state_reason": "completed", "labels": []any{"bug"}, "assignees": []any{},
	}))

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assert.Equal(t, "/repos/acme/app/issues/42", req.Path)
	assertGitHubHeaders(t, req)
	// No title or body: a PATCH that sent `"body": null` would clear the body.
	// An empty assignees list is sent: it means "remove all assignees".
	assert.Equal(t, map[string]any{
		"state": "closed", "state_reason": "completed", "labels": []any{"bug"}, "assignees": []any{},
	}, req.Body)

	assert.Equal(t, map[string]any{
		"number": 42.0, "id": 1.0, "title": "Crash on save", "state": "closed", "state_reason": "completed",
		"html_url": "https://github.com/acme/app/issues/42", "labels": []any{"bug"}, "assignees": []any{},
	}, out)
}

// An update that names no field sends an empty patch — which GitHub answers
// with the unchanged issue — and never a null that would clear a field.
func TestIssueUpdateWithNoFieldsChangesNothing(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/issues/42", jsonReply(200, issueJSON))
	f.run("issue.update", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42})
	assert.Equal(t, map[string]any{}, f.only().Body)
}

// state_reason only means something alongside state, so it is not sent alone.
func TestIssueUpdateStateReasonNeedsState(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/issues/42", jsonReply(200, issueJSON))
	f.run("issue.update", map[string]any{"owner": "acme", "repo": "app", "issue_number": 42, "title": "New", "state_reason": "not_planned"})
	assert.Equal(t, map[string]any{"title": "New"}, f.only().Body)
}

// The action schemas double as agent tool input schemas, so they must be
// plain object schemas: providers reject or rewrite top-level combinators.
func TestParamSchemasArePlainObjects(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	for _, a := range resolved.Manifest.GetActions() {
		params := a.GetParams().AsMap()
		assert.Equal(t, "object", params["type"], a.GetId())
		assert.Equal(t, false, params["additionalProperties"], "%s: an unknown param is an error, not ignored", a.GetId())
		for _, k := range []string{"anyOf", "oneOf", "allOf", "if", "then", "else", "not"} {
			assert.NotContains(t, params, k, "%s: top-level %s", a.GetId(), k)
		}
	}
}

// owner/repo/workflow are path segments: a slash or a dot segment is refused
// before any request, because "." and ".." survive percent-escaping.
func TestPathParamsRejectTraversal(t *testing.T) {
	f := newFake(t)
	for _, p := range []map[string]any{
		{"owner": "acme", "repo": "..", "issue_number": 1},
		{"owner": "acme", "repo": ".", "issue_number": 1},
		{"owner": "..", "repo": "app", "issue_number": 1},
		{"owner": "acme", "repo": "app/../../user", "issue_number": 1},
		{"owner": "acme/x", "repo": "app", "issue_number": 1},
	} {
		_, err := f.try("issue.get", p)
		assert.Error(t, err, "%v", p)
	}
	_, err := f.try("workflow.dispatch", map[string]any{"owner": "acme", "repo": "app", "workflow": "..", "ref": "main"})
	assert.Error(t, err)
	assert.Empty(t, f.requests())

	f.on("GET", "/repos/acme/.github/issues/1", jsonReply(200, issueJSON))
	f.run("issue.get", map[string]any{"owner": "acme", "repo": ".github", "issue_number": 1})
	assert.Equal(t, "/repos/acme/.github/issues/1", f.only().Path, "a dot-led repo name is legitimate")
}

const prJSON = `{
  "id": 9001, "number": 17, "state": "open", "title": "Add retries", "body": "Adds retries.",
  "draft": false, "merged": false, "mergeable": true, "mergeable_state": "clean", "rebaseable": true,
  "html_url": "https://github.com/acme/app/pull/17",
  "user": {"login": "octocat"},
  "head": {"label": "octocat:retries", "ref": "retries", "sha": "aaa111",
           "repo": {"full_name": "octocat/app"}},
  "base": {"label": "acme:main", "ref": "main", "sha": "bbb222",
           "repo": {"full_name": "acme/app"}},
  "merge_commit_sha": "ccc333", "commits": 3, "additions": 40, "deletions": 2, "changed_files": 5,
  "comments": 1, "review_comments": 4,
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "closed_at": null, "merged_at": null
}`

func TestPullRequestGet(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/pulls/17", jsonReply(200, prJSON))
	out := data(t, f.run("pr.get", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app/pulls/17", req.Path)
	assertGitHubHeaders(t, req)

	assert.Equal(t, map[string]any{
		"number": 17.0, "id": 9001.0, "title": "Add retries", "body": "Adds retries.",
		"state": "open", "draft": false, "merged": false,
		"mergeable": true, "mergeable_state": "clean", "rebaseable": true,
		"html_url": "https://github.com/acme/app/pull/17", "user": "octocat",
		"head":             map[string]any{"ref": "retries", "sha": "aaa111", "label": "octocat:retries", "repo": "octocat/app"},
		"base":             map[string]any{"ref": "main", "sha": "bbb222", "label": "acme:main", "repo": "acme/app"},
		"merge_commit_sha": "ccc333", "commits": 3.0, "additions": 40.0, "deletions": 2.0, "changed_files": 5.0,
		"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "closed_at": nil, "merged_at": nil,
	}, out)
}

// mergeable is null while GitHub computes it, and a fork's head repo is null
// once the fork is deleted. Neither is an error.
func TestPullRequestGetToleratesNulls(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/pulls/18", jsonReply(200,
		`{"id":1,"number":18,"state":"open","title":"t","body":null,"merged":false,"mergeable":null,"mergeable_state":"unknown",
		  "html_url":"h","user":{"login":"o"},"head":{"label":"gone:x","ref":"x","sha":"s","repo":null},
		  "base":{"label":"acme:main","ref":"main","sha":"b","repo":{"full_name":"acme/app"}},
		  "commits":1,"additions":0,"deletions":0,"changed_files":0,"created_at":"c","updated_at":"u"}`))
	out := data(t, f.run("pr.get", map[string]any{"owner": "acme", "repo": "app", "pull_number": 18}))
	assert.Nil(t, out["mergeable"])
	assert.Equal(t, "unknown", out["mergeable_state"])
	assert.Nil(t, out["head"].(map[string]any)["repo"])
	assert.Nil(t, out["draft"], "a field GitHub omits is null")
}

func TestPullRequestListFilesPaginates(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/pulls/17/files", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			w.Header().Set("Link", `<`+f.srv.URL+`/repos/acme/app/pulls/17/files?per_page=100&page=1>; rel="prev", <`+f.srv.URL+`/repos/acme/app/pulls/17/files?per_page=100&page=1>; rel="first"`)
			_, _ = w.Write([]byte(`[{"sha":"s3","filename":"c.go","status":"renamed","previous_filename":"old_c.go","additions":0,"deletions":0,"changes":0,
			  "blob_url":"b3","raw_url":"r3","contents_url":"c3"}]`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/repos/acme/app/pulls/17/files?per_page=100&page=2>; rel="next", <`+f.srv.URL+`/repos/acme/app/pulls/17/files?per_page=100&page=2>; rel="last"`)
		_, _ = w.Write([]byte(`[
		  {"sha":"s1","filename":"a.go","status":"modified","additions":3,"deletions":1,"changes":4,"patch":"@@ -1 +1 @@\n-x\n+y","blob_url":"b1","raw_url":"r1","contents_url":"c1"},
		  {"sha":"s2","filename":"bin.png","status":"added","additions":0,"deletions":0,"changes":0,"blob_url":"b2","raw_url":"r2","contents_url":"c2"}]`))
	})
	out := data(t, f.run("pr.list_files", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17}))

	reqs := f.requests()
	require.Len(t, reqs, 2, "follows rel=next once, then stops")
	assert.Equal(t, "/repos/acme/app/pulls/17/files", reqs[0].Path)
	assert.Equal(t, "per_page=100", reqs[0].Query, "asks for the largest page GitHub allows")
	assert.Equal(t, "page=2&per_page=100", sortedQuery(reqs[1].Query), "the second request is the Link header's next URL")
	for _, r := range reqs {
		assertGitHubHeaders(t, r)
	}

	assert.Equal(t, []any{
		map[string]any{"filename": "a.go", "status": "modified", "additions": 3.0, "deletions": 1.0, "changes": 4.0,
			"patch": "@@ -1 +1 @@\n-x\n+y", "previous_filename": nil, "sha": "s1"},
		map[string]any{"filename": "bin.png", "status": "added", "additions": 0.0, "deletions": 0.0, "changes": 0.0,
			"patch": nil, "previous_filename": nil, "sha": "s2"},
		map[string]any{"filename": "c.go", "status": "renamed", "additions": 0.0, "deletions": 0.0, "changes": 0.0,
			"patch": nil, "previous_filename": "old_c.go", "sha": "s3"},
	}, out["items"])
}

func TestPullRequestReviewCreate(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls/17/reviews", jsonReply(200,
		`{"id": 80, "node_id": "PRR_1", "user": {"login": "octocat"}, "body": "Needs a test.",
		  "state": "CHANGES_REQUESTED", "html_url": "https://github.com/acme/app/pull/17#pullrequestreview-80",
		  "submitted_at": "2026-01-05T00:00:00Z", "commit_id": "6dcb09b5b57875f334f61aebed695e2e4193db5e"}`))
	out := data(t, f.run("pr.review.create", map[string]any{
		"owner": "acme", "repo": "app", "pull_number": 17, "event": "REQUEST_CHANGES", "body": "Needs a test.", "commit_id": "6dcb09b5b57875f334f61aebed695e2e4193db5e",
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/pulls/17/reviews", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{"event": "REQUEST_CHANGES", "body": "Needs a test.", "commit_id": "6dcb09b5b57875f334f61aebed695e2e4193db5e"}, req.Body)

	assert.Equal(t, map[string]any{
		"id": 80.0, "state": "CHANGES_REQUESTED",
		"html_url":     "https://github.com/acme/app/pull/17#pullrequestreview-80",
		"submitted_at": "2026-01-05T00:00:00Z", "commit_id": "6dcb09b5b57875f334f61aebed695e2e4193db5e",
	}, out)
}

// event defaults to COMMENT, never to GitHub's blank (a PENDING review that is
// never submitted), and PENDING itself is not accepted. APPROVE needs no body.
func TestPullRequestReviewCreateEventRules(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls/17/reviews", jsonReply(200, `{"id":1,"state":"APPROVED","html_url":"h","commit_id":"c"}`))

	f.run("pr.review.create", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "body": "LGTM"})
	assert.Equal(t, map[string]any{"event": "COMMENT", "body": "LGTM"}, f.requests()[0].Body)

	f.run("pr.review.create", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "event": "APPROVE"})
	assert.Equal(t, map[string]any{"event": "APPROVE"}, f.requests()[1].Body)

	_, err := f.try("pr.review.create", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "event": "PENDING", "body": "x"})
	assert.Error(t, err, "only the three submitting events are accepted")
	assert.Len(t, f.requests(), 2)
}

// GitHub enforces "body is required for REQUEST_CHANGES and COMMENT" with a
// 422; the action surfaces GitHub's own explanation, field included.
func TestPullRequestReviewWithoutBodyReportsGitHubsReason(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/pulls/17/reviews", jsonReply(422,
		`{"message":"Unprocessable Entity","errors":["Body is required for REQUEST_CHANGES and COMMENT events"],"status":"422"}`))
	res := f.run("pr.review.create", map[string]any{"owner": "acme", "repo": "app", "pull_number": 17, "event": "REQUEST_CHANGES"})
	assert.True(t, res.IsError)
	assert.False(t, res.Retryable)
	assert.Contains(t, res.Content, "Body is required for REQUEST_CHANGES and COMMENT events")
	assert.Equal(t, map[string]any{"event": "REQUEST_CHANGES"}, f.only().Body)
}

func TestRepoListForUserPaginates(t *testing.T) {
	f := newFake(t)
	repo := func(id int, full string, private bool) string {
		owner, name, _ := strings.Cut(full, "/")
		b, _ := json.Marshal(map[string]any{
			"id": id, "node_id": "R", "name": name, "full_name": full, "private": private,
			"owner":    map[string]any{"login": owner, "id": 1, "type": "User"},
			"html_url": "https://github.com/" + full, "description": nil, "fork": false,
			"default_branch": "main", "archived": false, "visibility": map[bool]string{true: "private", false: "public"}[private],
			"pushed_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			"permissions": map[string]any{"admin": false, "maintain": false, "push": true, "triage": true, "pull": true},
		})
		return string(b)
	}
	f.on("GET", "/user/repos", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte("[" + repo(3, "acme/web", true) + "]"))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+`/user/repositories?page=2&per_page=100&sort=pushed>; rel="next"`)
		_, _ = w.Write([]byte("[" + repo(1, "acme/app", true) + "," + repo(2, "octocat/dotfiles", false) + "]"))
	})
	// GitHub's next links name /user/repositories (an alias); answer it the same.
	f.on("GET", "/user/repositories", f.routes["GET /user/repos"])

	out := data(t, f.run("repo.list_for_user", map[string]any{"sort": "pushed"}))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "/user/repos", reqs[0].Path)
	assert.Equal(t, "per_page=100&sort=pushed", sortedQuery(reqs[0].Query))
	assert.Equal(t, "page=2&per_page=100&sort=pushed", sortedQuery(reqs[1].Query))
	for _, r := range reqs {
		assertGitHubHeaders(t, r)
	}

	items, _ := out["items"].([]any)
	require.Len(t, items, 3, "items from both pages, in order")
	assert.Equal(t, map[string]any{
		"id": 1.0, "full_name": "acme/app", "name": "app", "owner": "acme", "private": true,
		"html_url": "https://github.com/acme/app", "description": nil, "default_branch": "main",
		"archived": false, "fork": false, "pushed_at": "2026-01-01T00:00:00Z", "can_push": true,
	}, items[0])
	assert.Equal(t, "octocat/dotfiles", items[1].(map[string]any)["full_name"])
	assert.Equal(t, "acme/web", items[2].(map[string]any)["full_name"])
}

// Filters are sent only when given; GitHub 422s on type combined with
// visibility or affiliation, so the action never adds them on its own.
func TestRepoListForUserFilters(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user/repos", jsonReply(200, `[]`))
	f.run("repo.list_for_user", map[string]any{"visibility": "private", "affiliation": "owner,organization_member", "direction": "asc"})
	assert.Equal(t, "affiliation=owner%2Corganization_member&direction=asc&per_page=100&visibility=private", sortedQuery(f.only().Query))
}

func TestRepoGet(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app", jsonReply(200, `{
	  "id": 1296269, "node_id": "R_1", "name": "app", "full_name": "acme/app", "private": true,
	  "owner": {"login": "acme", "id": 1, "type": "Organization"},
	  "html_url": "https://github.com/acme/app", "description": "The app.", "fork": false,
	  "language": "Go", "topics": ["cli", "agents"], "visibility": "private", "default_branch": "trunk",
	  "archived": false, "pushed_at": "2026-01-01T00:00:00Z", "stargazers_count": 80,
	  "permissions": {"admin": false, "push": true, "pull": true}}`))
	out := data(t, f.run("repo.get", map[string]any{"owner": "acme", "repo": "app"}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app", req.Path)
	assertGitHubHeaders(t, req)

	assert.Equal(t, map[string]any{
		"id": 1296269.0, "full_name": "acme/app", "name": "app", "owner": "acme",
		"description": "The app.", "private": true, "visibility": "private", "default_branch": "trunk",
		"language": "Go", "topics": []any{"cli", "agents"}, "archived": false, "fork": false,
		"html_url": "https://github.com/acme/app", "pushed_at": "2026-01-01T00:00:00Z", "can_push": true,
	}, out)
}

// contentJSON is a contents-API file object: content base64 in 60-column
// lines, as GitHub wraps it.
func contentJSON(t *testing.T, path, encoding string, raw []byte) string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(raw)
	var wrapped strings.Builder
	for len(b64) > 60 {
		wrapped.WriteString(b64[:60] + "\n")
		b64 = b64[60:]
	}
	wrapped.WriteString(b64 + "\n")
	content := wrapped.String()
	if encoding == "none" {
		content = ""
	}
	name := path[strings.LastIndex(path, "/")+1:]
	b, err := json.Marshal(map[string]any{
		"type": "file", "encoding": encoding, "size": len(raw), "name": name, "path": path,
		"content": content, "sha": "3d21ec53a331a6f037a91c368710b99387d012c1",
		"url":          "https://api.github.com/repos/acme/app/contents/" + path,
		"html_url":     "https://github.com/acme/app/blob/dev/" + path,
		"download_url": "https://raw.githubusercontent.com/acme/app/dev/" + path,
		"_links":       map[string]any{"self": "https://api.github.com/repos/acme/app/contents/" + path},
	})
	require.NoError(t, err)
	return string(b)
}

// A file's content comes back as its text: the base64 GitHub wraps it in is
// decoded, and a nested path reaches GitHub as one escaped segment.
func TestRepoGetContentReadsAFileAsText(t *testing.T) {
	source := strings.Repeat("package main\n\nfunc main() { println(\"héllo\") }\n", 4)
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/src/main.go", jsonReply(200, contentJSON(t, "src/main.go", "base64", []byte(source))))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": "src/main.go", "ref": "dev"}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/repos/acme/app/contents/src%2Fmain.go", req.Path, "the path is one escaped segment; GitHub decodes %2F")
	assert.Equal(t, "ref=dev", req.Query)
	assertGitHubHeaders(t, req)

	assert.Equal(t, map[string]any{
		"type": "file", "path": "src/main.go", "name": "main.go",
		"sha": "3d21ec53a331a6f037a91c368710b99387d012c1", "size": float64(len(source)),
		"html_url": "https://github.com/acme/app/blob/dev/src/main.go",
		"content":  source, "binary": false, "too_large": false,
		"target": nil, "submodule_git_url": nil,
	}, out)
}

// A binary file is reported as such, with no content, rather than failing.
func TestRepoGetContentReportsABinaryFile(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R'}
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/logo.png", jsonReply(200, contentJSON(t, "logo.png", "base64", png)))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": "logo.png"}))
	assert.Equal(t, true, out["binary"])
	assert.Nil(t, out["content"])
	assert.Empty(t, f.only().Query, "no ref means the default branch")
}

// GitHub serves files over 1 MB through the contents API without their
// bytes (encoding "none"); the result says so instead of returning "".
func TestRepoGetContentReportsAFileTooLargeToServe(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/data.csv", jsonReply(200, contentJSON(t, "data.csv", "none", []byte("big"))))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": "data.csv"}))
	assert.Equal(t, true, out["too_large"])
	assert.Equal(t, false, out["binary"])
	assert.Nil(t, out["content"])
}

// An empty file is text: content "" rather than null.
func TestRepoGetContentReadsAnEmptyFile(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/.keep", jsonReply(200, contentJSON(t, ".keep", "base64", nil)))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": ".keep"}))
	assert.Equal(t, "", out["content"])
	assert.Equal(t, false, out["binary"])
}

// A directory (the root, when path is omitted) lists its entries.
func TestRepoGetContentListsADirectory(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/", jsonReply(200, `[
	  {"type":"file","size":625,"name":"README.md","path":"README.md","sha":"a1","url":"u","html_url":"h","download_url":"d"},
	  {"type":"dir","size":0,"name":"src","path":"src","sha":"b2","url":"u","html_url":"h","download_url":null},
	  {"type":"symlink","size":9,"name":"latest","path":"latest","sha":"c3","url":"u","html_url":"h","download_url":"d"}]`))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app"}))

	assert.Equal(t, "/repos/acme/app/contents/", f.only().Path)
	assert.Equal(t, map[string]any{
		"type": "dir", "path": "",
		"entries": []any{
			map[string]any{"name": "README.md", "path": "README.md", "type": "file", "size": 625.0},
			map[string]any{"name": "src", "path": "src", "type": "dir", "size": 0.0},
			map[string]any{"name": "latest", "path": "latest", "type": "symlink", "size": 9.0},
		},
	}, out)
}

// A symlink whose target is outside the repository is described, not read.
func TestRepoGetContentDescribesASymlink(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/contents/latest", jsonReply(200,
		`{"type":"symlink","target":"/etc/hosts","size":10,"name":"latest","path":"latest","sha":"c3","url":"u","html_url":"h","download_url":"d"}`))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": "latest"}))
	assert.Equal(t, "symlink", out["type"])
	assert.Equal(t, "/etc/hosts", out["target"])
	assert.Nil(t, out["content"])
	assert.Equal(t, false, out["binary"])
}

// path and a tree's ref become URL path text, so a dot segment or an empty
// one is refused before any request; dots inside a name are fine.
func TestCodeReadingPathsRejectTraversal(t *testing.T) {
	f := newFake(t)
	for _, path := range []string{"..", ".", "../secrets", "src/../../user", "src/./x", "/etc/passwd", "src//x", "src/"} {
		_, err := f.try("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": path})
		assert.Error(t, err, "path %q", path)
	}
	for _, ref := range []string{"..", "a/../b", "/main"} {
		_, err := f.try("repo.get_tree", map[string]any{"owner": "acme", "repo": "app", "ref": ref})
		assert.Error(t, err, "ref %q", ref)
	}
	assert.Empty(t, f.requests())

	f.on("GET", "/repos/acme/app/contents/.github/workflows/ci.yml",
		jsonReply(200, contentJSON(t, ".github/workflows/ci.yml", "base64", []byte("on: push\n"))))
	out := data(t, f.run("repo.get_content", map[string]any{"owner": "acme", "repo": "app", "path": ".github/workflows/ci.yml"}))
	assert.Equal(t, "on: push\n", out["content"], "a dot-led name is legitimate")
}

const treeJSON = `{
  "sha": "9fb037999f264ba9a7fc6274d15fa3ae2ab98312",
  "url": "https://api.github.com/repos/acme/app/git/trees/9fb03",
  "tree": [
    {"path": "README.md", "mode": "100644", "type": "blob", "size": 30, "sha": "a1", "url": "u"},
    {"path": "internal", "mode": "040000", "type": "tree", "sha": "b2", "url": "u"},
    {"path": "internal/llm", "mode": "040000", "type": "tree", "sha": "c3", "url": "u"},
    {"path": "internal/llm/tools.go", "mode": "100644", "type": "blob", "size": 1200, "sha": "d4", "url": "u"},
    {"path": "internalize.md", "mode": "100644", "type": "blob", "size": 7, "sha": "e5", "url": "u"},
    {"path": "vendor/lib", "mode": "160000", "type": "commit", "sha": "f6"}
  ],
  "truncated": false
}`

func TestRepoGetTreeListsEveryPathOnTheDefaultBranch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/git/trees/HEAD", jsonReply(200, treeJSON))
	out := data(t, f.run("repo.get_tree", map[string]any{"owner": "acme", "repo": "app"}))

	req := f.only()
	assert.Equal(t, "/repos/acme/app/git/trees/HEAD", req.Path, "no ref is the default branch")
	assert.Equal(t, "recursive=1", req.Query)
	assertGitHubHeaders(t, req)

	assert.Equal(t, "9fb037999f264ba9a7fc6274d15fa3ae2ab98312", out["sha"])
	assert.Equal(t, false, out["truncated"])
	assert.Equal(t, []any{
		map[string]any{"path": "README.md", "type": "blob", "size": 30.0},
		map[string]any{"path": "internal", "type": "tree", "size": nil},
		map[string]any{"path": "internal/llm", "type": "tree", "size": nil},
		map[string]any{"path": "internal/llm/tools.go", "type": "blob", "size": 1200.0},
		map[string]any{"path": "internalize.md", "type": "blob", "size": 7.0},
		map[string]any{"path": "vendor/lib", "type": "commit", "size": nil},
	}, out["entries"])
}

// path asks GitHub for that subtree only (the tree-ish <ref>:<path>) and puts
// the directory back in front of each entry. A branch with a slash is part of
// one escaped segment.
func TestRepoGetTreeOfOneDirectoryOnABranch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/git/trees/feature/retries:internal", jsonReply(200, `{
	  "sha": "b2", "url": "u", "truncated": false,
	  "tree": [
	    {"path": "llm", "mode": "040000", "type": "tree", "sha": "c3", "url": "u"},
	    {"path": "llm/tools.go", "mode": "100644", "type": "blob", "size": 1200, "sha": "d4", "url": "u"}]}`))
	out := data(t, f.run("repo.get_tree", map[string]any{"owner": "acme", "repo": "app", "ref": "feature/retries", "path": "internal"}))

	req := f.only()
	assert.Equal(t, "/repos/acme/app/git/trees/feature%2Fretries:internal", req.Path)
	assert.Equal(t, "recursive=1", req.Query)
	assert.Equal(t, []any{
		map[string]any{"path": "internal/llm", "type": "tree", "size": nil},
		map[string]any{"path": "internal/llm/tools.go", "type": "blob", "size": 1200.0},
	}, out["entries"])
}

// With no ref, a directory is read from the default branch.
func TestRepoGetTreeOfOneDirectoryOnTheDefaultBranch(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/git/trees/HEAD:internal/llm", jsonReply(200,
		`{"sha":"c3","url":"u","truncated":false,"tree":[{"path":"tools.go","mode":"100644","type":"blob","size":1200,"sha":"d4","url":"u"}]}`))
	out := data(t, f.run("repo.get_tree", map[string]any{"owner": "acme", "repo": "app", "path": "internal/llm"}))
	assert.Equal(t, "/repos/acme/app/git/trees/HEAD:internal%2Fllm", f.only().Path)
	assert.Equal(t, []any{map[string]any{"path": "internal/llm/tools.go", "type": "blob", "size": 1200.0}}, out["entries"])
}

func TestCodeSearchReturnsFilesAndMatchedFragments(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/search/code", jsonReply(200, `{
	  "total_count": 2, "incomplete_results": false,
	  "items": [
	    {"name": "duration.go", "path": "src/time/duration.go", "sha": "a1", "score": 1,
	     "url": "u", "git_url": "g", "html_url": "https://github.com/golang/go/blob/abc/src/time/duration.go",
	     "repository": {"id": 1, "full_name": "golang/go", "name": "go", "owner": {"login": "golang"}},
	     "text_matches": [
	       {"object_type": "FileContent", "property": "content", "fragment": "func ParseDuration(s string) (Duration, error) {",
	        "matches": [{"text": "ParseDuration", "indices": [5, 18]}]},
	       {"object_type": "FileContent", "property": "content", "fragment": "// ParseDuration parses a duration string.", "matches": []}]},
	    {"name": "flag.go", "path": "src/flag/flag.go", "sha": "b2", "score": 0.5,
	     "url": "u", "git_url": "g", "html_url": "https://github.com/golang/go/blob/abc/src/flag/flag.go",
	     "repository": {"id": 1, "full_name": "golang/go", "name": "go", "owner": {"login": "golang"}}}]}`))
	out := data(t, f.run("code.search", map[string]any{"q": "ParseDuration repo:golang/go language:go"}))

	req := f.only()
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/search/code", req.Path)
	assert.Equal(t, "page=1&per_page=30&q=ParseDuration+repo%3Agolang%2Fgo+language%3Ago", sortedQuery(req.Query))
	assert.Equal(t, "application/vnd.github.text-match+json", req.Header.Get("Accept"), "asks for the matched fragments")
	assert.Equal(t, apiVersion, req.Header.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))

	assert.Equal(t, map[string]any{
		"total_count": 2.0, "incomplete_results": false,
		"items": []any{
			map[string]any{"repository": "golang/go", "path": "src/time/duration.go", "name": "duration.go", "sha": "a1",
				"html_url":  "https://github.com/golang/go/blob/abc/src/time/duration.go",
				"fragments": []any{"func ParseDuration(s string) (Duration, error) {", "// ParseDuration parses a duration string."}},
			map[string]any{"repository": "golang/go", "path": "src/flag/flag.go", "name": "flag.go", "sha": "b2",
				"html_url": "https://github.com/golang/go/blob/abc/src/flag/flag.go", "fragments": []any{}},
		},
	}, out)
}

func TestCodeSearchPaging(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/search/code", jsonReply(200, `{"total_count": 0, "incomplete_results": true, "items": []}`))
	out := data(t, f.run("code.search", map[string]any{"q": "x org:acme", "per_page": 100, "page": 3}))
	assert.Equal(t, "page=3&per_page=100&q=x+org%3Aacme", sortedQuery(f.only().Query))
	assert.Equal(t, true, out["incomplete_results"])

	_, err := f.try("code.search", map[string]any{"q": ""})
	assert.Error(t, err, "an empty query is refused before any request")
	_, err = f.try("code.search", map[string]any{"q": "x", "per_page": 101})
	assert.Error(t, err)
	assert.Len(t, f.requests(), 1)
}

func TestWorkflowDispatch(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/actions/workflows/deploy.yml/dispatches", jsonReply(200,
		`{"workflow_run_id": 123456, "run_url": "https://api.github.com/repos/acme/app/actions/runs/123456",
		  "html_url": "https://github.com/acme/app/actions/runs/123456"}`))
	out := data(t, f.run("workflow.dispatch", map[string]any{
		"owner": "acme", "repo": "app", "workflow": "deploy.yml", "ref": "main",
		"inputs": map[string]any{"environment": "staging", "dry_run": "true"},
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assert.Equal(t, "/repos/acme/app/actions/workflows/deploy.yml/dispatches", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"ref": "main", "inputs": map[string]any{"environment": "staging", "dry_run": "true"},
		"return_run_details": true,
	}, req.Body)

	assert.Equal(t, map[string]any{
		"dispatched":      true,
		"workflow_run_id": 123456.0,
		"run_url":         "https://api.github.com/repos/acme/app/actions/runs/123456",
		"html_url":        "https://github.com/acme/app/actions/runs/123456",
	}, out)
}

// A GitHub that ignores return_run_details answers 204 with no body; the
// dispatch still happened.
func TestWorkflowDispatchNoContent(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/actions/workflows/1234/dispatches", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	out := data(t, f.run("workflow.dispatch", map[string]any{"owner": "acme", "repo": "app", "workflow": "1234", "ref": "v1.2.0"}))
	assert.Equal(t, map[string]any{"ref": "v1.2.0", "return_run_details": true}, f.only().Body)
	assert.Equal(t, map[string]any{"dispatched": true, "workflow_run_id": nil, "run_url": nil, "html_url": nil}, out)
}

func TestUserGet(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/user", jsonReply(200, `{"login":"octocat","id":583231,"name":"The Octocat","html_url":"https://github.com/octocat"}`))
	out := data(t, f.run("user.get", map[string]any{}))
	req := f.only()
	assert.Equal(t, "/user", req.Path)
	assertGitHubHeaders(t, req)
	assert.Equal(t, "octocat", out["login"])
}

// ---------------------------------------------------------------------------
// Error mapping, for every action.
// ---------------------------------------------------------------------------

// minimal is the smallest valid params for each action, used to drive the
// error responses through every one of them.
var minimal = map[string]map[string]any{
	"user.get":           {},
	"issue.create":       {"owner": "acme", "repo": "app", "title": "t"},
	"issue.get":          {"owner": "acme", "repo": "app", "issue_number": 1},
	"issue.comment":      {"owner": "acme", "repo": "app", "issue_number": 1, "body": "b"},
	"issue.update":       {"owner": "acme", "repo": "app", "issue_number": 1, "state": "closed"},
	"pr.get":             {"owner": "acme", "repo": "app", "pull_number": 1},
	"pr.list_files":      {"owner": "acme", "repo": "app", "pull_number": 1},
	"pr.review.create":   {"owner": "acme", "repo": "app", "pull_number": 1, "event": "APPROVE"},
	"repo.list_for_user": {},
	"repo.get":           {"owner": "acme", "repo": "app"},
	"repo.get_content":   {"owner": "acme", "repo": "app", "path": "README.md"},
	"repo.get_tree":      {"owner": "acme", "repo": "app"},
	"code.search":        {"q": "retry repo:acme/app"},
	"workflow.dispatch":  {"owner": "acme", "repo": "app", "workflow": "ci.yml", "ref": "main"},
}

// Every action in the manifest is covered by these tests: adding one without
// extending `minimal` (and so the error tests) fails here.
func TestEveryActionIsCovered(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/user.get@1")
	require.NoError(t, err)
	var ids []string
	for _, a := range resolved.Manifest.GetActions() {
		ids = append(ids, a.GetId())
	}
	var want []string
	for id := range minimal {
		want = append(want, id)
	}
	sort.Strings(ids)
	sort.Strings(want)
	assert.Equal(t, want, ids)
}

// errorCase is one upstream failure and how every action must classify it.
type errorCase struct {
	name      string
	status    int
	headers   map[string]string
	body      string
	retryable bool
	contains  []string
}

var errorCases = []errorCase{
	{
		name: "404 not found or no access", status: 404,
		body:     `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`,
		contains: []string{"not found", "installed"},
	},
	{
		name: "422 validation", status: 422,
		body:     `{"message":"Validation Failed","errors":[{"resource":"Issue","code":"invalid","field":"assignees","value":"ghost"}],"status":"422"}`,
		contains: []string{"Validation Failed", "assignees", "invalid"},
	},
	{
		name: "403 primary rate limit", status: 403,
		headers:   map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1767225600"},
		body:      `{"message":"API rate limit exceeded for user ID 1.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api","status":"403"}`,
		retryable: true, contains: []string{"rate limit", "2026-01-01T00:00:00Z"},
	},
	{
		name: "403 secondary rate limit", status: 403,
		headers:   map[string]string{"Retry-After": "60", "X-RateLimit-Remaining": "4000"},
		body:      `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again.","status":"403"}`,
		retryable: true, contains: []string{"rate limit", "60"},
	},
	{
		name: "429 rate limit", status: 429,
		headers:   map[string]string{"Retry-After": "30"},
		body:      `{"message":"Too many requests"}`,
		retryable: true, contains: []string{"rate limit", "30"},
	},
	{
		name: "403 permission", status: 403,
		headers:   map[string]string{"X-RateLimit-Remaining": "4999"},
		body:      `{"message":"Resource not accessible by integration","documentation_url":"https://docs.github.com/rest","status":"403"}`,
		retryable: false, contains: []string{"Resource not accessible by integration", "permission"},
	},
	{
		name: "401 bad credentials", status: 401,
		body:     `{"message":"Bad credentials","status":"401"}`,
		contains: []string{"reconnect"},
	},
	{
		name: "502 upstream", status: 502, body: `<html>bad gateway</html>`,
		retryable: true, contains: []string{"502"},
	},
}

func TestErrorMappingForEveryAction(t *testing.T) {
	for id, params := range minimal {
		for _, tc := range errorCases {
			t.Run(id+"/"+tc.name, func(t *testing.T) {
				f := newFake(t)
				f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					for k, v := range tc.headers {
						w.Header().Set(k, v)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				res := f.run(id, params)
				assert.True(t, res.IsError, "a %d is an error result", tc.status)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, "retryable for %s: %s", tc.name, res.Content)
				for _, want := range tc.contains {
					assert.Contains(t, res.Content, want)
				}
				assert.NotContains(t, res.Content, token)
			})
		}
	}
}

// sortedQuery normalises a raw query so assertions do not depend on the
// order url.Values or GitHub's Link header happened to use.
func sortedQuery(raw string) string {
	parts := strings.Split(raw, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}
