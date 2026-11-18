// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"context"
	"crypto/x509"
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

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

// The GitHub writer end to end: the real adapter (NewGitHub), the catalog's
// shipped check_run actions and the real declarative runner, against an
// httptest fake of GitHub's check runs API. Only the manifest's base_url is
// pointed at the fake. These pin the requests a check sends, how GitHub's
// answers are classified (retried, rejected, not authorized), and how the
// syncer batches annotations, falls back when GitHub refuses a check's
// details, and abandons a check GitHub no longer has.
//
// Request and response shapes: docs.github.com/en/rest/checks/runs
// (API version 2022-11-28).

const ghToken = "ghs_fake_installation_token"

// apiRequest is one request the fake received.
type apiRequest struct {
	Method string
	Path   string
	Query  url.Values
	Auth   string
	Accept string
	Body   map[string]any
}

// ghRun is a check run as the fake holds it.
type ghRun struct {
	ID                                  int64
	Name, HeadSHA, Status, Conclusion   string
	ExternalID, DetailsURL, Title, Summ string
	CompletedAt                         string
	// Annotations counts the annotations GitHub accepted.
	Annotations int
}

// checkRunsAPI is a fake of GitHub's check runs endpoints for
// reliant-labs/reliant. fail, when set, may answer a request first.
type checkRunsAPI struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	got  []apiRequest
	runs map[int64]*ghRun
	next int64
	fail func(r apiRequest) (status int, header http.Header, body string, ok bool)
}

func newCheckRunsAPI(t *testing.T) *checkRunsAPI {
	api := &checkRunsAPI{t: t, runs: map[int64]*ghRun{}, next: 4241}
	api.srv = netguardtest.NewTLSServer(t, http.HandlerFunc(api.serve))
	return api
}

func (api *checkRunsAPI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	req := apiRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Auth: r.Header.Get("Authorization"), Accept: r.Header.Get("Accept")}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req.Body); err != nil {
			api.t.Errorf("request body is not JSON: %s", raw)
		}
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	api.got = append(api.got, req)
	reply := func(status int, header http.Header, body string) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	if api.fail != nil {
		if status, header, body, ok := api.fail(req); ok {
			reply(status, header, body)
			return
		}
	}
	const repo = "/repos/reliant-labs/reliant/"
	rest, ok := strings.CutPrefix(r.URL.Path, repo)
	notFound := `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`
	switch parts := strings.Split(rest, "/"); {
	case !ok:
		reply(404, nil, notFound)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-runs":
		var list []string
		for id := api.next; id > 4241; id-- {
			run := api.runs[id]
			if run != nil && run.HeadSHA == parts[1] && (req.Query.Get("check_name") == "" || run.Name == req.Query.Get("check_name")) {
				list = append(list, runJSON(run))
			}
		}
		reply(200, nil, fmt.Sprintf(`{"total_count": %d, "check_runs": [%s]}`, len(list), strings.Join(list, ",")))
	case r.Method == http.MethodPost && rest == "check-runs":
		api.next++
		run := &ghRun{ID: api.next, Name: strOf(req.Body["name"]), HeadSHA: strOf(req.Body["head_sha"]), Status: "queued"}
		api.apply(run, req.Body)
		api.runs[run.ID] = run
		reply(201, nil, runJSON(run))
	case r.Method == http.MethodPatch && len(parts) == 2 && parts[0] == "check-runs":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		run := api.runs[id]
		if run == nil {
			reply(404, nil, notFound)
			return
		}
		api.apply(run, req.Body)
		reply(200, nil, runJSON(run))
	default:
		reply(404, nil, notFound)
	}
}

// apply is GitHub's update: what is given replaces what was; a conclusion
// completes the run; annotations add up.
func (api *checkRunsAPI) apply(run *ghRun, body map[string]any) {
	if s := strOf(body["status"]); s != "" {
		run.Status = s
	}
	if c := strOf(body["conclusion"]); c != "" {
		run.Status, run.Conclusion = "completed", c
	}
	if s := strOf(body["external_id"]); s != "" {
		run.ExternalID = s
	}
	if s := strOf(body["details_url"]); s != "" {
		run.DetailsURL = s
	}
	if s := strOf(body["completed_at"]); s != "" {
		run.CompletedAt = s
	}
	if out, ok := body["output"].(map[string]any); ok {
		run.Title, run.Summ = strOf(out["title"]), strOf(out["summary"])
		if list, ok := out["annotations"].([]any); ok {
			run.Annotations += len(list)
		}
	}
}

func runJSON(run *ghRun) string {
	conclusion := "null"
	if run.Conclusion != "" {
		conclusion = strconv.Quote(run.Conclusion)
	}
	return fmt.Sprintf(`{"id": %d, "node_id": "CR_%d", "html_url": "https://github.com/reliant-labs/reliant/runs/%d",
  "name": %q, "head_sha": %q, "status": %q, "conclusion": %s, "external_id": %q, "details_url": %q,
  "started_at": "2026-11-18T02:00:00Z", "completed_at": null,
  "output": {"title": %q, "summary": %q, "annotations_count": %d}, "app": {"id": 1041943, "slug": "reliant-labs"}}`,
		run.ID, run.ID, run.ID, run.Name, run.HeadSHA, run.Status, conclusion, run.ExternalID, run.DetailsURL, run.Title, run.Summ, run.Annotations)
}

func strOf(v any) string { s, _ := v.(string); return s }

func (api *checkRunsAPI) requests() []apiRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]apiRequest(nil), api.got...)
}

// calls is each request as "METHOD path".
func (api *checkRunsAPI) calls() []string {
	var out []string
	for _, r := range api.requests() {
		out = append(out, r.Method+" "+strings.TrimPrefix(r.Path, "/repos/reliant-labs/reliant"))
	}
	return out
}

// rebasedRunner is the real runner, with the shipped manifest pointed at
// the fake.
type rebasedRunner struct {
	inner *httpaction.Runner
	base  string
}

func (r rebasedRunner) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error) {
	m = proto.Clone(m).(*reliantv1.IntegrationManifest)
	m.Connection.BaseUrl = r.base
	return r.inner.RunAuthenticated(ctx, m, a, params, src, site)
}

func (api *checkRunsAPI) runner() ActionRunner {
	pool := x509.NewCertPool()
	pool.AddCert(api.srv.Certificate())
	return rebasedRunner{inner: httpaction.NewRunner(netguardtest.Guard()).WithRootCAs(pool), base: api.srv.URL}
}

// installationCred is the App's installation token for the repository.
type installationCred struct{}

func (installationCred) ConnectionID() string      { return "controlplane:github-app" }
func (installationCred) Params() map[string]string { return nil }
func (installationCred) Scrub(s string) string     { return strings.ReplaceAll(s, ghToken, "[redacted]") }
func (installationCred) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+ghToken)
	return nil
}

type installationBroker struct{}

func (installationBroker) ActingCredential(context.Context, string, httpaction.CredentialRequest) (httpaction.Credential, error) {
	return installationCred{}, nil
}

// memberCtx is a context in which u1 is a member of org-1.
func memberCtx() context.Context {
	m := &membership{}
	m.set("u1", "org-1", true, nil)
	return authz.WithMembership(context.Background(), m)
}

func (api *checkRunsAPI) checkRuns(t *testing.T) CheckRuns {
	t.Helper()
	runs, err := NewGitHub(GitHubDeps{Runner: api.runner(), Broker: installationBroker{}}).
		CheckRuns(memberCtx(), orgIdentity, "reliant-labs", "reliant")
	require.NoError(t, err)
	return runs
}

// httpHarness is the syncer over the fake store and the real GitHub writer.
func httpHarness(t *testing.T) (*harness, *checkRunsAPI) {
	api := newCheckRunsAPI(t)
	h := newHarness()
	h.svc.github = NewGitHub(GitHubDeps{Runner: api.runner(), Broker: installationBroker{}})
	return h, api
}

func (h *harness) syncHTTP(t *testing.T) error {
	t.Helper()
	_, err := h.svc.SyncOnce(memberCtx())
	return err
}

func TestCheckRunsOverHTTPSendWhatGitHubTakes(t *testing.T) {
	api := newCheckRunsAPI(t)
	api.runs[4242] = &ghRun{ID: 4242, Name: "reliant/ci", HeadSHA: headSHA, Status: "completed", Conclusion: "success", ExternalID: "an-earlier-firing"}
	api.runs[4243] = &ghRun{ID: 4243, Name: "reliant/ci", HeadSHA: headSHA, Status: "queued", ExternalID: "row-1"}
	api.next = 4243
	runs := api.checkRuns(t)
	ctx := memberCtx()

	// Find matches the external id, not just the name.
	id, err := runs.Find(ctx, headSHA, "reliant/ci", "row-1")
	require.NoError(t, err)
	require.Equal(t, int64(4243), id)
	id, err = runs.Find(ctx, headSHA, "reliant/ci", "row-2")
	require.NoError(t, err)
	require.Zero(t, id, "a create whose answer was lost is found by its external id only")

	started, completed := t0, t0.Add(time.Minute)
	id, err = runs.Create(ctx, Write{Name: "reliant/ci", HeadSHA: headSHA, Status: core.CheckStatusQueued, ExternalID: "row-2",
		DetailsURL: HostedRunURL("chat-1"), Output: &Output{Title: "Queued", Summary: "Waiting for capacity."}})
	require.NoError(t, err)
	require.Equal(t, int64(4244), id)
	require.NoError(t, runs.Update(ctx, id, Write{Status: core.CheckStatusInProgress, StartedAt: &started,
		Output: &Output{Title: "Running", Summary: "s", Text: "t"}}))
	require.NoError(t, runs.Update(ctx, id, Write{Status: core.CheckStatusCompleted, Conclusion: "failure", StartedAt: &started, CompletedAt: &completed,
		Output: &Output{Title: "2 failures", Summary: "s", Annotations: []map[string]any{
			{"path": "a.go", "start_line": 3, "end_line": 3, "annotation_level": "failure", "message": "boom"},
		}}}))

	got := api.requests()
	require.Equal(t, []string{
		"GET /commits/" + headSHA + "/check-runs",
		"GET /commits/" + headSHA + "/check-runs",
		"POST /check-runs",
		"PATCH /check-runs/4244",
		"PATCH /check-runs/4244",
	}, api.calls())
	for _, r := range got {
		require.Equal(t, "Bearer "+ghToken, r.Auth, "every request carries the installation token")
		require.Equal(t, "application/vnd.github+json", r.Accept)
	}
	require.Equal(t, url.Values{"check_name": {"reliant/ci"}, "filter": {"all"}, "per_page": {"100"}}, got[0].Query,
		"every run of the name, not only the latest: an earlier firing's run must not hide this one's")
	require.Equal(t, map[string]any{
		"name": "reliant/ci", "head_sha": headSHA, "status": "queued", "external_id": "row-2",
		"details_url": "https://app.reliantlabs.io/workflows/runs/chat-1",
		"output":      map[string]any{"title": "Queued", "summary": "Waiting for capacity."},
	}, got[2].Body)
	require.Equal(t, map[string]any{
		"status": "in_progress", "started_at": "2026-11-18T02:00:00Z",
		"output": map[string]any{"title": "Running", "summary": "s", "text": "t"},
	}, got[3].Body)
	require.Equal(t, map[string]any{
		"status": "completed", "conclusion": "failure", "started_at": "2026-11-18T02:00:00Z", "completed_at": "2026-11-18T02:01:00Z",
		"output": map[string]any{"title": "2 failures", "summary": "s", "annotations": []any{
			map[string]any{"path": "a.go", "start_line": 3.0, "end_line": 3.0, "annotation_level": "failure", "message": "boom"},
		}},
	}, got[4].Body)
	require.Equal(t, "failure", api.runs[4244].Conclusion)
}

// How each answer is classified: retried (a plain error), rejected (final:
// the write is wrong or its target gone) or not authorized (final: the App
// lost the repository).
func TestCheckRunsOverHTTPClassifyGitHubsAnswers(t *testing.T) {
	type want int
	const (
		retried want = iota
		rejected
		notAuth
	)
	rateLimited := http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1763431200"}}
	for name, c := range map[string]struct {
		status int
		header http.Header
		body   string
		want   want
	}{
		"422 validation failed": {422, nil, `{"message":"Validation Failed","errors":[{"resource":"CheckRun","field":"head_sha","code":"invalid"}]}`, rejected},
		"404 repository gone":   {404, nil, `{"message":"Not Found"}`, rejected},
		"410 gone":              {410, nil, `{"message":"Checks are disabled"}`, rejected},
		"500":                   {500, nil, `{"message":"Server Error"}`, retried},
		"502":                   {502, nil, `<html>bad gateway</html>`, retried},
		"503":                   {503, nil, `{"message":"Service Unavailable"}`, retried},
		"429":                   {429, nil, `{"message":"slow down"}`, retried},
		"403 rate limit":        {403, rateLimited, `{"message":"API rate limit exceeded"}`, retried},
		"403 forbidden":         {403, nil, `{"message":"Resource not accessible by integration"}`, notAuth},
		"401 bad credentials":   {401, nil, `{"message":"Bad credentials"}`, notAuth},
	} {
		t.Run(name, func(t *testing.T) {
			api := newCheckRunsAPI(t)
			api.fail = func(apiRequest) (int, http.Header, string, bool) { return c.status, c.header, c.body, true }
			_, err := api.checkRuns(t).Create(memberCtx(), Write{Name: "reliant/ci", HeadSHA: headSHA, Status: core.CheckStatusQueued})
			require.Error(t, err)
			require.Equal(t, c.want == rejected, errors.Is(err, ErrRejected), "rejected: %v", err)
			require.Equal(t, c.want == notAuth, errors.Is(err, ErrNotAuthorized), "not authorized: %v", err)
			require.Contains(t, err.Error(), strconv.Itoa(c.status))
			require.NotContains(t, err.Error(), ghToken)
		})
	}
}

// A check's whole life over HTTP: created queued, moved to in_progress,
// concluded without annotations, then its 120 annotations in batches of 50
// (GitHub's limit per request), each batch carrying the output it belongs
// to and no status.
func TestSyncerOverHTTPBatchesAnnotations(t *testing.T) {
	h, api := httpHarness(t)
	ctx := memberCtx()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(launched(status(core.Active())))
	require.NoError(t, h.syncHTTP(t))
	require.NoError(t, h.svc.Started(ctx, "chat-1"))
	require.NoError(t, h.syncHTTP(t))

	annotations := make([]any, 120)
	for i := range annotations {
		annotations[i] = map[string]any{"path": fmt.Sprintf("f%03d.go", i), "start_line": i + 1, "message": "lint", "level": "warning"}
	}
	require.NoError(t, h.svc.Ended(ctx, "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunCompleted, Verdict: "failure",
		Output: map[string]any{"title": "120 lint findings", "summary": "See the annotations.", "annotations": annotations}}))
	h.observe(launched(status(core.Completed())))
	require.NoError(t, h.syncHTTP(t))

	id := *row.GitHubCheckRunID
	path := fmt.Sprintf("/check-runs/%d", id)
	require.Equal(t, []string{
		"GET /commits/" + headSHA + "/check-runs", "POST /check-runs",
		"PATCH " + path,
		"PATCH " + path, "PATCH " + path, "PATCH " + path, "PATCH " + path,
	}, api.calls())
	got := api.requests()
	require.Equal(t, "queued", got[1].Body["status"])
	require.Equal(t, row.ID, got[1].Body["external_id"])
	require.Equal(t, "in_progress", got[2].Body["status"])
	conclude := got[3].Body
	require.Equal(t, "completed", conclude["status"])
	require.Equal(t, "failure", conclude["conclusion"])
	require.NotContains(t, conclude["output"], "annotations", "the conclusion goes first, on its own")
	sizes := []int{}
	for i, r := range got[4:] {
		require.NotContains(t, r.Body, "status", "batch %d moves nothing", i)
		require.NotContains(t, r.Body, "conclusion", "batch %d moves nothing", i)
		out := r.Body["output"].(map[string]any)
		require.Equal(t, "120 lint findings", out["title"], "GitHub requires the output's title and summary with its annotations")
		require.Equal(t, "See the annotations.", out["summary"])
		list := out["annotations"].([]any)
		sizes = append(sizes, len(list))
		first := list[0].(map[string]any)
		require.Equal(t, fmt.Sprintf("f%03d.go", 50*i), first["path"], "batches go in order")
		require.Equal(t, "warning", first["annotation_level"])
	}
	require.Equal(t, []int{50, 50, 20}, sizes)
	require.Equal(t, 120, api.runs[id].Annotations)
	require.Equal(t, 120, row.AnnotationsSent)
	require.NotNil(t, row.FinishedAt)
}

// GitHub failing (5xx) is retried with backoff, and the check is written once
// it answers.
func TestSyncerOverHTTPRetries5xx(t *testing.T) {
	h, api := httpHarness(t)
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(&core.WorkflowCheckObservation{EventOutcome: core.TriggerEventPending})
	api.fail = func(r apiRequest) (int, http.Header, string, bool) {
		return 503, nil, `{"message":"Service Unavailable"}`, r.Method == http.MethodPost
	}
	require.Error(t, h.syncHTTP(t))
	require.Nil(t, row.GitHubCheckRunID)
	require.Nil(t, row.FinishedAt, "retried, not abandoned")
	require.Equal(t, 1, row.Attempts)
	require.Contains(t, row.LastError, "503")
	require.True(t, row.NextSyncAt.After(h.clock.now), "backs off")

	api.fail = nil
	h.clock.now = row.NextSyncAt
	require.NoError(t, h.syncHTTP(t))
	require.NotNil(t, row.GitHubCheckRunID)
	require.Equal(t, core.CheckStatusQueued, api.runs[*row.GitHubCheckRunID].Status)
	require.Zero(t, row.Attempts)
	require.Empty(t, row.LastError)
}

// GitHub refuses the conclusion's details (422): the conclusion is sent once
// more with details that say so, so the check still concludes.
func TestSyncerOverHTTPConcludesWhenGitHubRefusesTheDetails(t *testing.T) {
	h, api := httpHarness(t)
	ctx := memberCtx()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(launched(status(core.Active())))
	require.NoError(t, h.syncHTTP(t))
	require.NoError(t, h.svc.Ended(ctx, "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunCompleted, Verdict: "success",
		Output: map[string]any{"title": "Passed", "summary": "something GitHub will not take"}}))
	h.observe(launched(status(core.Completed())))
	refused := 0
	api.fail = func(r apiRequest) (int, http.Header, string, bool) {
		out, _ := r.Body["output"].(map[string]any)
		if r.Method == http.MethodPatch && out["title"] == "Passed" {
			refused++
			return 422, nil, `{"message":"Validation Failed","errors":["output.summary is invalid"]}`, true
		}
		return 0, nil, "", false
	}
	require.NoError(t, h.syncHTTP(t))
	require.Equal(t, 1, refused)
	run := api.runs[*row.GitHubCheckRunID]
	require.Equal(t, "completed", run.Status)
	require.Equal(t, "success", run.Conclusion, "the check concludes as the run did")
	require.Equal(t, "GitHub refused this check's details", run.Title)
	require.Contains(t, run.Summ, "output.summary is invalid")
	require.NotNil(t, row.FinishedAt)
	require.Empty(t, row.LastError)
}

// GitHub refuses one batch of annotations (422): that batch is skipped and
// the rest are sent; the check, already concluded, finishes.
func TestSyncerOverHTTPSkipsARefusedAnnotationBatch(t *testing.T) {
	h, api := httpHarness(t)
	ctx := memberCtx()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	annotations := make([]any, 120)
	for i := range annotations {
		annotations[i] = map[string]any{"path": fmt.Sprintf("f%03d.go", i), "start_line": 1, "message": "m"}
	}
	require.NoError(t, h.svc.Ended(ctx, "chat-1", core.WorkflowCheckEnding{Status: core.TriggerRunFailed,
		Output: map[string]any{"title": "Failed", "summary": "s", "annotations": annotations}}))
	h.observe(launched(status(core.Failed())))
	api.fail = func(r apiRequest) (int, http.Header, string, bool) {
		out, _ := r.Body["output"].(map[string]any)
		list, _ := out["annotations"].([]any)
		if len(list) > 0 && list[0].(map[string]any)["path"] == "f050.go" {
			return 422, nil, `{"message":"Validation Failed","errors":["annotations[3].path is invalid"]}`, true
		}
		return 0, nil, "", false
	}
	require.NoError(t, h.syncHTTP(t))
	run := api.runs[*row.GitHubCheckRunID]
	require.Equal(t, "failure", run.Conclusion)
	require.Equal(t, 70, run.Annotations, "the first and last batches")
	require.Equal(t, 120, row.AnnotationsSent)
	require.NotNil(t, row.FinishedAt)
}

// The check run is gone from GitHub (404 on its update): the check is
// abandoned at once rather than retried for an hour past its timeout.
func TestSyncerOverHTTPAbandonsACheckGitHubNoLongerHas(t *testing.T) {
	h, api := httpHarness(t)
	ctx := memberCtx()
	row := h.open(t, &reliantv1.WorkflowCheck{Name: "reliant/ci"})
	h.observe(launched(status(core.Active())))
	require.NoError(t, h.syncHTTP(t))
	delete(api.runs, *row.GitHubCheckRunID)

	require.NoError(t, h.svc.Started(ctx, "chat-1"))
	require.NoError(t, h.syncHTTP(t), "an abandoned check is an outcome, not a failed sync")
	require.NotNil(t, row.FinishedAt)
	require.Contains(t, row.LastError, "abandoned, GitHub rejected it")
	require.Contains(t, row.LastError, "404")

	n := len(api.requests())
	h.clock.advance(time.Hour)
	require.NoError(t, h.syncHTTP(t))
	require.Len(t, api.requests(), n, "never retried")
}
