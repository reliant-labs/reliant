package githubqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

func init() { httpaction.Executors().MustRegister(Executor, enqueue) }

// Queries and mutations are fixed server code. No arbitrary GraphQL, jump,
// auto-merge flag, direct merge or admin bypass is accepted from action params.
const subjectQuery = `query ReliantQueueSubject($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    id databaseId nameWithOwner defaultBranchRef { name }
    pullRequest(number: $number) {
      id number state isDraft headRefOid baseRefName isMergeQueueEnabled
      repository { id databaseId }
      headRepository { id databaseId }
      mergeQueueEntry { id position pullRequest { id headRefOid repository { id } } }
    }
  }
}`

const enqueueMutation = `mutation ReliantEnqueue($input: EnqueuePullRequestInput!) {
  enqueuePullRequest(input: $input) {
    mergeQueueEntry { id position pullRequest { id headRefOid repository { id } } }
  }
}`

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type entry struct {
	ID          string
	Position    *int
	PullRequest *struct {
		ID         string
		HeadRefOid string
		Repository *struct{ ID string }
	}
}

type subject struct {
	ID               string
	DatabaseID       int64
	NameWithOwner    string
	DefaultBranchRef *struct{ Name string }
	PullRequest      *struct {
		ID                  string
		Number              int64
		State               string
		IsDraft             *bool
		HeadRefOid          string
		BaseRefName         string
		IsMergeQueueEnabled bool
		Repository          *struct {
			ID         string
			DatabaseID int64
		}
		HeadRepository *struct {
			ID         string
			DatabaseID int64
		}
		MergeQueueEntry *entry
	}
}

type failure struct {
	code   string
	status int
}

func (f *failure) Error() string { return f.code }

func failed(code string, status int) (*httpaction.Result, error) {
	text := map[string]string{
		"invalid":     "Queue request parameters are invalid.",
		"stale":       "The pull request head changed; provide the current exact expectedHeadOid.",
		"failed":      "GitHub could not enqueue this open, non-draft pull request on its default branch. Check repository access, native queue availability and required rules.",
		"unavailable": "Queue outcome is unavailable. Retry to read current queue membership before another enqueue attempt.",
	}[code]
	return &httpaction.Result{IsError: true, Retryable: code == "unavailable", StatusCode: status,
		Content: text, Data: map[string]any{"outcome": code, "accepted": false}}, nil
}

func accepted(e *entry, existing bool) (*httpaction.Result, error) {
	return &httpaction.Result{StatusCode: http.StatusOK, Content: "Pull request is accepted in GitHub's native merge queue; it has not necessarily merged.",
		Data: map[string]any{"outcome": "accepted", "accepted": true, "queue_entry_id": e.ID, "position": *e.Position, "already_queued": existing}}, nil
}

func enqueue(ctx context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
	owner, _ := call.Params["owner"].(string)
	repo, _ := call.Params["repo"].(string)
	sha, _ := call.Params["expectedHeadOid"].(string)
	repositoryID := integer(call.Params["repository_id"])
	number := integer(call.Params["pull_number"])
	if !ownerPattern.MatchString(owner) || !repoPattern.MatchString(repo) || repo == "." || repo == ".." ||
		!shaPattern.MatchString(sha) || repositoryID <= 0 || number <= 0 || number > 2147483647 {
		return failed("invalid", http.StatusBadRequest)
	}
	if call.Runner == nil || call.Credential == nil || call.Manifest == nil {
		return failed("failed", http.StatusPreconditionFailed)
	}
	base, err := url.Parse(call.Manifest.GetConnection().GetBaseUrl())
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return failed("failed", http.StatusPreconditionFailed)
	}
	endpoint := strings.TrimSuffix(base.String(), "/") + "/graphql"
	variables := map[string]any{"owner": owner, "repo": repo, "number": number}
	read := func() (*subject, *failure) {
		var out struct{ Repository *subject }
		if f := graphql(ctx, call, endpoint, subjectQuery, variables, &out); f != nil {
			return nil, f
		}
		s := out.Repository
		if s == nil || s.ID == "" || s.DatabaseID != repositoryID || !strings.EqualFold(s.NameWithOwner, owner+"/"+repo) ||
			s.DefaultBranchRef == nil || s.DefaultBranchRef.Name == "" || s.PullRequest == nil {
			return nil, &failure{"failed", http.StatusConflict}
		}
		p := s.PullRequest
		if p.ID == "" || p.Number != number || p.Repository == nil || p.Repository.ID != s.ID || p.Repository.DatabaseID != repositoryID ||
			p.HeadRepository == nil || p.HeadRepository.ID != s.ID || p.HeadRepository.DatabaseID != repositoryID ||
			p.State != "OPEN" || p.IsDraft == nil || *p.IsDraft || p.BaseRefName != s.DefaultBranchRef.Name || !p.IsMergeQueueEnabled {
			return nil, &failure{"failed", http.StatusConflict}
		}
		if p.HeadRefOid != sha {
			return nil, &failure{"stale", http.StatusConflict}
		}
		if p.MergeQueueEntry != nil && !validEntry(p.MergeQueueEntry, s.ID, p.ID, sha) {
			return nil, &failure{"unavailable", http.StatusServiceUnavailable}
		}
		return s, nil
	}
	s, f := read()
	if f != nil {
		return failed(f.code, f.status)
	}
	if e := s.PullRequest.MergeQueueEntry; e != nil {
		return accepted(e, true)
	}
	var out struct {
		EnqueuePullRequest *struct{ MergeQueueEntry *entry }
	}
	f = graphql(ctx, call, endpoint, enqueueMutation, map[string]any{"input": map[string]any{
		"pullRequestId": s.PullRequest.ID, "expectedHeadOid": sha,
	}}, &out)
	if f == nil && out.EnqueuePullRequest != nil && validEntry(out.EnqueuePullRequest.MergeQueueEntry, s.ID, s.PullRequest.ID, sha) {
		return accepted(out.EnqueuePullRequest.MergeQueueEntry, false)
	}
	// A timeout or GraphQL error can follow a committed mutation. Read current
	// membership once, never blindly replay the mutation. Every later invocation
	// also begins with this read. GitHub owns queue membership uniqueness.
	if refreshed, readFailure := read(); readFailure == nil {
		if e := refreshed.PullRequest.MergeQueueEntry; e != nil {
			return accepted(e, true)
		}
	} else {
		return failed(readFailure.code, readFailure.status)
	}
	if f != nil {
		return failed(f.code, f.status)
	}
	return failed("unavailable", http.StatusServiceUnavailable)
}

func validEntry(e *entry, repoID, prID, sha string) bool {
	return e != nil && e.ID != "" && len(e.ID) <= 256 && e.Position != nil && *e.Position > 0 &&
		e.PullRequest != nil && e.PullRequest.ID == prID && e.PullRequest.HeadRefOid == sha &&
		e.PullRequest.Repository != nil && e.PullRequest.Repository.ID == repoID
}

func graphql(ctx context.Context, call httpaction.ExecutorCall, endpoint, query string, variables map[string]any, out any) *failure {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return &failure{"failed", http.StatusBadRequest}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil || call.Credential.Apply(req) != nil {
		return &failure{"failed", http.StatusPreconditionFailed}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := call.Runner.Client().Do(req)
	if err != nil {
		return &failure{"unavailable", http.StatusServiceUnavailable}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 429 || resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return &failure{"unavailable", resp.StatusCode}
		}
		return &failure{"failed", resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return &failure{"unavailable", http.StatusServiceUnavailable}
	}
	var envelope struct {
		Data   json.RawMessage
		Errors []struct{ Type string }
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return &failure{"unavailable", http.StatusServiceUnavailable}
	}
	if len(envelope.Errors) > 0 {
		for _, e := range envelope.Errors {
			if e.Type == "RATE_LIMITED" || e.Type == "INTERNAL" {
				return &failure{"unavailable", http.StatusServiceUnavailable}
			}
		}
		return &failure{"failed", http.StatusConflict}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return &failure{"unavailable", http.StatusServiceUnavailable}
	}
	return nil
}

func integer(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		if n > 0 && n <= 9007199254740991 && n == float64(int64(n)) {
			return int64(n)
		}
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err == nil {
			return i
		}
	}
	return 0
}

// Keep errors out of provider results; only fixed failure messages cross out.
var _ error = (*failure)(nil)
