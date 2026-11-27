// Copyright (c) 2025 Reliant Labs
package ghrest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghrest"
)

var repo = ghport.Repo{Owner: "acme", Name: "widgets"}

// route is one canned answer: status, JSON body and headers.
type route struct {
	status  int
	body    any
	headers map[string]string
}

// server answers "METHOD /path" with its route and records what it got.
type server struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]route
	got    []recorded
}

type recorded struct {
	method, path, query, auth, version string
	body                               map[string]any
}

func newServer(t *testing.T, routes map[string]route) *server {
	s := &server{t: t, routes: routes}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.got = append(s.got, recorded{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		auth: r.Header.Get("Authorization"), version: r.Header.Get("X-GitHub-Api-Version"), body: body})
	rt, ok := s.routes[r.Method+" "+r.URL.Path]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	for k, v := range rt.headers {
		w.Header().Set(k, strings.ReplaceAll(v, "{base}", ghrest.DefaultBaseURL))
	}
	w.Header().Set("Content-Type", "application/json")
	if rt.status == 0 {
		rt.status = http.StatusOK
	}
	w.WriteHeader(rt.status)
	if rt.body != nil {
		_ = json.NewEncoder(w).Encode(rt.body)
	}
}

func (s *server) client() ghrest.Service {
	return newClient(s.srv.URL, func(context.Context, ghport.Repo) (string, error) { return "tok-1", nil })
}

// newClient is the adapter with its calls to api.github.com sent to target.
func newClient(target string, token ghrest.TokenFunc) ghrest.Service {
	u, _ := url.Parse(target)
	return ghrest.New(ghrest.Deps{Token: token, HTTPClient: &http.Client{Transport: redirect{u}}})
}

// redirect sends every request to one test server, keeping its path.
type redirect struct{ to *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

func (s *server) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.got...)
}

const p = "/repos/acme/widgets/"

func msg(m string) map[string]any { return map[string]any{"message": m} }

func TestGetRefSendsTokenAndVersion(t *testing.T) {
	s := newServer(t, map[string]route{
		"GET " + p + "git/ref/heads/main": {body: map[string]any{"ref": "refs/heads/main", "object": map[string]any{"sha": "abc"}}},
	})
	got, err := s.client().GetRef(t.Context(), repo, "refs/heads/main")
	if err != nil || got.SHA != "abc" || got.Name != "refs/heads/main" {
		t.Fatalf("GetRef = %+v, %v", got, err)
	}
	r := s.requests()[0]
	if r.auth != "Bearer tok-1" || r.version != "2022-11-28" {
		t.Fatalf("headers: auth %q version %q", r.auth, r.version)
	}
}

func TestNotFoundIsErrNotFound(t *testing.T) {
	s := newServer(t, nil)
	_, err := s.client().GetRef(t.Context(), repo, "refs/heads/gone")
	if !errors.Is(err, ghport.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.client().GetPullRequest(t.Context(), repo, 9); !ghport.IsNotFound(err) {
		t.Fatalf("pr err = %v", err)
	}
}

func TestRefWriteOutcomes(t *testing.T) {
	s := newServer(t, map[string]route{
		"POST " + p + "git/refs":                {status: 422, body: msg("Reference already exists")},
		"PATCH " + p + "git/refs/heads/main":    {status: 422, body: msg("Update is not a fast forward")},
		"PATCH " + p + "git/refs/heads/missing": {status: 422, body: msg("Reference does not exist")},
		"DELETE " + p + "git/refs/heads/gone":   {status: 422, body: msg("Reference does not exist")},
		"DELETE " + p + "git/refs/heads/x":      {status: 204},
	})
	c := s.client()
	if err := c.CreateRef(t.Context(), repo, "refs/heads/main", "abc"); !errors.Is(err, ghport.ErrAlreadyExists) {
		t.Fatalf("create: %v", err)
	}
	if err := c.UpdateRef(t.Context(), repo, "refs/heads/main", "abc", false); !errors.Is(err, ghport.ErrNotFastForward) {
		t.Fatalf("update: %v", err)
	}
	if err := c.UpdateRef(t.Context(), repo, "refs/heads/missing", "abc", true); !ghport.IsNotFound(err) {
		t.Fatalf("update missing: %v", err)
	}
	if err := c.DeleteRef(t.Context(), repo, "refs/heads/gone"); !ghport.IsNotFound(err) {
		t.Fatalf("delete missing: %v", err)
	}
	if err := ghport.DeleteBranch(t.Context(), c, repo, "gone"); err != nil {
		t.Fatalf("DeleteBranch of a missing branch: %v", err)
	}
	if err := c.DeleteRef(t.Context(), repo, "refs/heads/x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	upd := s.requests()[1]
	if upd.body["sha"] != "abc" || upd.body["force"] != false {
		t.Fatalf("update body = %v", upd.body)
	}
}

func TestOtherValidationIsValidationError(t *testing.T) {
	s := newServer(t, map[string]route{
		"POST " + p + "git/commits": {status: 422, body: map[string]any{"message": "Validation Failed",
			"errors": []any{map[string]any{"field": "tree", "message": "tree not found"}}}},
	})
	_, err := s.client().CreateCommit(t.Context(), repo, ghport.NewCommit{Message: "m", TreeSHA: "t"})
	var v *ghport.ValidationError
	if !errors.As(err, &v) || v.Field != "tree" {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeSendsSquashAndSHA(t *testing.T) {
	s := newServer(t, map[string]route{
		"PUT " + p + "pulls/3/merge": {body: map[string]any{"sha": "merged1", "merged": true}},
	})
	got, err := s.client().MergePullRequest(t.Context(), repo, 3, ghport.MergeRequest{Method: ghport.MergeSquash, ExpectedHeadSHA: "head1", CommitTitle: "T (#3)"})
	if err != nil || got.SHA != "merged1" {
		t.Fatalf("merge = %+v, %v", got, err)
	}
	b := s.requests()[0].body
	if b["merge_method"] != "squash" || b["sha"] != "head1" || b["commit_title"] != "T (#3)" {
		t.Fatalf("merge body = %v", b)
	}
}

func TestMerge409IsHeadMismatchWithTheActualHead(t *testing.T) {
	s := newServer(t, map[string]route{
		"PUT " + p + "pulls/3/merge": {status: 409, body: msg("Head branch was modified. Review and try the merge again.")},
		"GET " + p + "pulls/3":       {body: map[string]any{"number": 3, "state": "open", "head": map[string]any{"sha": "head2"}}},
	})
	_, err := s.client().MergePullRequest(t.Context(), repo, 3, ghport.MergeRequest{ExpectedHeadSHA: "head1"})
	var hm *ghport.HeadMismatchError
	if !errors.As(err, &hm) || hm.Expected != "head1" || hm.Actual != "head2" {
		t.Fatalf("err = %v", err)
	}
	if retry, _ := ghport.Retryable(err); retry {
		t.Fatal("a head mismatch must never be retried")
	}
}

func TestMerge405ReadsThePullRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pr     map[string]any
		check  func(error) bool
		reason ghport.NotMergeableReason
	}{
		{name: "already merged", pr: map[string]any{"state": "closed", "merged": true, "merge_commit_sha": "m1", "head": map[string]any{"sha": "h1"}},
			check: func(err error) bool {
				var am *ghport.AlreadyMergedError
				return errors.As(err, &am) && am.MergeCommitSHA == "m1" && am.HeadSHA == "h1"
			}},
		{name: "closed", pr: map[string]any{"state": "closed", "head": map[string]any{"sha": "h1"}}, reason: ghport.NotMergeableClosed},
		{name: "conflict", pr: map[string]any{"state": "open", "mergeable": false, "mergeable_state": "dirty", "head": map[string]any{"sha": "h1"}}, reason: ghport.NotMergeableConflict},
		{name: "blocked", pr: map[string]any{"state": "open", "mergeable": true, "mergeable_state": "blocked", "head": map[string]any{"sha": "h1"}}, reason: ghport.NotMergeableBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t, map[string]route{
				"PUT " + p + "pulls/3/merge": {status: 405, body: msg("Pull Request is not mergeable")},
				"GET " + p + "pulls/3":       {body: tc.pr},
			})
			_, err := s.client().MergePullRequest(t.Context(), repo, 3, ghport.MergeRequest{ExpectedHeadSHA: "h1"})
			if tc.check != nil {
				if !tc.check(err) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var nm *ghport.NotMergeableError
			if !errors.As(err, &nm) || nm.Reason != tc.reason {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRateLimits(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	s := newServer(t, map[string]route{
		"GET " + p + "pulls/1": {status: 403, body: msg("API rate limit exceeded"),
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(reset.Unix(), 10)}},
		"GET " + p + "pulls/2": {status: 403, body: msg("You have exceeded a secondary rate limit"), headers: map[string]string{"Retry-After": "30"}},
		"GET " + p + "pulls/3": {status: 429, body: msg("too many")},
		"GET " + p + "pulls/4": {status: 403, body: msg("Resource not accessible by integration")},
	})
	c := s.client()
	_, err := c.GetPullRequest(t.Context(), repo, 1)
	var rl *ghport.RateLimitError
	if !errors.As(err, &rl) || !rl.Reset.Equal(reset) || rl.Secondary {
		t.Fatalf("primary: %v", err)
	}
	if retry, at := ghport.Retryable(err); !retry || !at.Equal(reset) {
		t.Fatalf("Retryable = %v %v", retry, at)
	}
	_, err = c.GetPullRequest(t.Context(), repo, 2)
	if !errors.As(err, &rl) || !rl.Secondary || time.Until(rl.Reset) < 20*time.Second {
		t.Fatalf("secondary: %v", err)
	}
	if _, err = c.GetPullRequest(t.Context(), repo, 3); !errors.Is(err, ghport.ErrRateLimited) {
		t.Fatalf("429: %v", err)
	}
	_, err = c.GetPullRequest(t.Context(), repo, 4)
	var api *ghrest.APIError
	if !errors.As(err, &api) || api.Status != 403 || errors.Is(err, ghport.ErrRateLimited) {
		t.Fatalf("permission 403: %v", err)
	}
}

func TestServerErrorsAndTransportAreTransient(t *testing.T) {
	s := newServer(t, map[string]route{"GET " + p + "pulls/1": {status: 502, body: msg("Bad Gateway")}})
	_, err := s.client().GetPullRequest(t.Context(), repo, 1)
	var te *ghport.TransientError
	if !errors.As(err, &te) || te.Status != 502 {
		t.Fatalf("5xx: %v", err)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	c := newClient(dead.URL, func(context.Context, ghport.Repo) (string, error) { return "secret-token", nil })
	_, err = c.GetPullRequest(t.Context(), repo, 1)
	if !errors.Is(err, ghport.ErrTransient) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("transport: %v", err)
	}
}

func TestUnauthorizedIsAPIError(t *testing.T) {
	s := newServer(t, map[string]route{"GET " + p + "git/commits/abc": {status: 401, body: msg("Bad credentials")}})
	_, err := s.client().GetCommit(t.Context(), repo, "abc")
	var api *ghrest.APIError
	if !errors.As(err, &api) || api.Status != 401 {
		t.Fatalf("err = %v", err)
	}
}

func TestPullRequestAndCommit(t *testing.T) {
	s := newServer(t, map[string]route{
		"GET " + p + "pulls/5": {body: map[string]any{"number": 5, "title": "T", "state": "closed", "merged": true, "merge_commit_sha": "m",
			"mergeable": nil, "head": map[string]any{"sha": "h", "ref": "feat"}, "base": map[string]any{"ref": "main"}}},
		"GET " + p + "git/commits/m": {body: map[string]any{"sha": "m", "tree": map[string]any{"sha": "t"}, "parents": []any{map[string]any{"sha": "p"}}, "message": "T (#5)\n"}},
		"POST " + p + "git/commits":  {status: 201, body: map[string]any{"sha": "r", "tree": map[string]any{"sha": "t0"}, "parents": []any{map[string]any{"sha": "m"}}}},
	})
	c := s.client()
	pr, err := c.GetPullRequest(t.Context(), repo, 5)
	if err != nil || !pr.Merged || pr.MergeCommitSHA != "m" || pr.HeadRef != "feat" || pr.BaseRef != "main" || pr.Mergeable != ghport.MergeableUnknown {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	cm, err := c.GetCommit(t.Context(), repo, "m")
	if err != nil || cm.TreeSHA != "t" || len(cm.Parents) != 1 || cm.Parents[0] != "p" {
		t.Fatalf("commit = %+v, %v", cm, err)
	}
	nc, err := c.CreateCommit(t.Context(), repo, ghport.NewCommit{Message: "Revert", TreeSHA: "t0", Parents: []string{"m"}})
	if err != nil || nc.SHA != "r" {
		t.Fatalf("create commit = %+v, %v", nc, err)
	}
}

func TestListsFollowEveryPage(t *testing.T) {
	s := newServer(t, map[string]route{
		"GET " + p + "issues/7/comments": {body: []any{map[string]any{"id": 1, "body": "a"}},
			headers: map[string]string{"Link": `<{base}/repos/acme/widgets/issues/7/comments2>; rel="next", <{base}/x>; rel="last"`}},
		"GET " + p + "issues/7/comments2":   {body: []any{map[string]any{"id": 2, "body": "b"}}},
		"POST " + p + "issues/7/comments":   {status: 201, body: map[string]any{"id": 3, "body": "c"}},
		"GET " + p + "commits/h/check-runs": {body: map[string]any{"check_runs": []any{map[string]any{"id": 4, "name": "Reliant", "status": "completed", "conclusion": "success"}, map[string]any{"id": 9, "name": "Reliant", "status": "queued"}}}},
		"POST " + p + "check-runs":          {status: 201, body: map[string]any{"id": 10, "name": "Reliant", "head_sha": "h", "status": "in_progress"}},
		"PATCH " + p + "check-runs/10":      {body: map[string]any{"id": 10, "name": "Reliant", "head_sha": "h", "status": "completed", "conclusion": "failure"}},
	})
	c := s.client()
	comments, err := c.ListIssueComments(t.Context(), repo, 7)
	if err != nil || len(comments) != 2 || comments[0].Body != "a" || comments[1].Body != "b" {
		t.Fatalf("comments = %+v, %v", comments, err)
	}
	if got, err := c.CreateIssueComment(t.Context(), repo, 7, "c"); err != nil || got.ID != 3 {
		t.Fatalf("comment = %+v, %v", got, err)
	}
	runs, err := c.ListCheckRuns(t.Context(), repo, "h", ghport.ListCheckRuns{Name: "Reliant"})
	if err != nil || len(runs) != 2 || runs[0].ID != 9 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if q := s.requests()[3].query; !strings.Contains(q, "check_name=Reliant") {
		t.Fatalf("check runs query = %q", q)
	}
	run, err := c.CreateCheckRun(t.Context(), repo, ghport.CreateCheckRun{Name: "Reliant", HeadSHA: "h", Status: ghport.CheckInProgress,
		Output: ghport.CheckOutput{Title: "Gating", Summary: "s"}})
	if err != nil || run.ID != 10 {
		t.Fatalf("create run = %+v, %v", run, err)
	}
	if out := s.requests()[4].body["output"].(map[string]any); out["title"] != "Gating" {
		t.Fatalf("output = %v", out)
	}
	upd, err := c.UpdateCheckRun(t.Context(), repo, 10, ghport.UpdateCheckRun{Conclusion: ghport.ConclusionFailure})
	if err != nil || upd.Conclusion != ghport.ConclusionFailure {
		t.Fatalf("update run = %+v, %v", upd, err)
	}
	if _, err := c.CreateCheckRun(t.Context(), repo, ghport.CreateCheckRun{Name: "Reliant", HeadSHA: "h", Status: ghport.CheckQueued, Conclusion: ghport.ConclusionSuccess}); err == nil {
		t.Fatal("a queued run with a conclusion must be refused before the call")
	}
}

func TestNextPageOutsideTheBaseIsRefused(t *testing.T) {
	s := newServer(t, map[string]route{
		"GET " + p + "issues/7/comments": {body: []any{}, headers: map[string]string{"Link": `<https://evil.example/x>; rel="next"`}},
	})
	if _, err := s.client().ListIssueComments(t.Context(), repo, 7); err == nil {
		t.Fatal("a next page on another host must be refused")
	}
}

func TestTokenErrorStopsTheCall(t *testing.T) {
	s := newServer(t, nil)
	c := newClient(s.srv.URL, func(context.Context, ghport.Repo) (string, error) { return "", fmt.Errorf("no token") })
	if _, err := c.GetRef(t.Context(), repo, "refs/heads/main"); err == nil || len(s.requests()) != 0 {
		t.Fatalf("err = %v, requests = %d", err, len(s.requests()))
	}
	if _, err := newClient(s.srv.URL, nil).GetRef(t.Context(), repo, "refs/heads/main"); err == nil || len(s.requests()) != 0 {
		t.Fatalf("a client without a token source must not call: %v", err)
	}
}
