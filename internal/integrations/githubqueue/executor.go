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
	"time"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

func init() { httpaction.Executors().MustRegister(Executor, enqueue) }

// Queries and mutations are fixed server code. No arbitrary GraphQL, jump,
// auto-merge flag, direct merge or admin bypass is accepted from action params.
const subjectQuery = `query ReliantQueueSubject($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    id databaseId nameWithOwner defaultBranchRef { name }
    pullRequest(number: $number) {
      id databaseId url number state isDraft headRefOid baseRefName isMergeQueueEnabled
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
		DatabaseID          int64
		URL                 string
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
	code              string
	status            int
	retryAfterSeconds int
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

func failedResponse(f *failure) (*httpaction.Result, error) {
	res, err := failed(f.code, f.status)
	if f.retryAfterSeconds > 0 {
		res.Data["retry_after_seconds"] = f.retryAfterSeconds
	}
	return res, err
}

func accepted(s *subject, e *entry, existing bool) (*httpaction.Result, error) {
	// Common write identifiers describe the authenticated PR subject. The
	// separately named queue receipt is not the PR's numeric or node identity.
	p := s.PullRequest
	return &httpaction.Result{StatusCode: http.StatusOK, Content: "Pull request is accepted in GitHub's native merge queue; it has not necessarily merged.",
		Data: map[string]any{"outcome": "accepted", "accepted": true, "id": p.DatabaseID, "node_id": p.ID, "html_url": p.URL,
			"queue_entry_id": e.ID, "position": *e.Position, "already_queued": existing}}, nil
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
			return nil, &failure{code: "failed", status: http.StatusConflict}
		}
		p := s.PullRequest
		if p.ID == "" || p.DatabaseID <= 0 || p.URL == "" || p.Number != number || p.Repository == nil || p.Repository.ID != s.ID || p.Repository.DatabaseID != repositoryID ||
			p.HeadRepository == nil || p.HeadRepository.ID != s.ID || p.HeadRepository.DatabaseID != repositoryID ||
			p.State != "OPEN" || p.IsDraft == nil || *p.IsDraft || p.BaseRefName != s.DefaultBranchRef.Name || !p.IsMergeQueueEnabled {
			return nil, &failure{code: "failed", status: http.StatusConflict}
		}
		if p.HeadRefOid != sha {
			return nil, &failure{code: "stale", status: http.StatusConflict}
		}
		if p.MergeQueueEntry != nil && !validEntry(p.MergeQueueEntry, s.ID, p.ID, sha) {
			return nil, &failure{code: "unavailable", status: http.StatusServiceUnavailable}
		}
		return s, nil
	}
	s, f := read()
	if f != nil {
		return failedResponse(f)
	}
	if e := s.PullRequest.MergeQueueEntry; e != nil {
		return accepted(s, e, true)
	}
	var out struct {
		EnqueuePullRequest *struct{ MergeQueueEntry *entry }
	}
	f = graphql(ctx, call, endpoint, enqueueMutation, map[string]any{"input": map[string]any{
		"pullRequestId": s.PullRequest.ID, "expectedHeadOid": sha,
	}}, &out)
	if f == nil && out.EnqueuePullRequest != nil && validEntry(out.EnqueuePullRequest.MergeQueueEntry, s.ID, s.PullRequest.ID, sha) {
		return accepted(s, out.EnqueuePullRequest.MergeQueueEntry, false)
	}
	// A timeout or GraphQL error can follow a committed mutation. Read current
	// membership once, never blindly replay the mutation. Every later invocation
	// also begins with this read. GitHub owns queue membership uniqueness.
	// A known rate limit must wait; do not immediately spend another read.
	if f != nil && f.retryAfterSeconds > 0 {
		return failedResponse(f)
	}
	if refreshed, readFailure := read(); readFailure == nil {
		if e := refreshed.PullRequest.MergeQueueEntry; e != nil {
			return accepted(refreshed, e, true)
		}
	} else {
		return failedResponse(readFailure)
	}
	if f != nil {
		return failedResponse(f)
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
		return &failure{code: "failed", status: http.StatusBadRequest}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil || call.Credential.Apply(req) != nil {
		return &failure{code: "failed", status: http.StatusPreconditionFailed}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Credentials are applied to this fixed endpoint only. Never follow even a
	// same-host redirect (POST can become GET or retain its bearer on 307).
	// Copy the client, preserving its guarded transport without changing shared policy.
	client := *call.Runner.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return &failure{code: "unavailable", status: http.StatusServiceUnavailable}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return &failure{code: "unavailable", status: http.StatusServiceUnavailable}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && rateLimited(resp.Header, raw)) {
			return &failure{code: "unavailable", status: resp.StatusCode, retryAfterSeconds: retryHint(resp.Header, time.Now())}
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			return &failure{code: "unavailable", status: resp.StatusCode}
		}
		return &failure{code: "failed", status: resp.StatusCode}
	}
	var envelope struct {
		Data   json.RawMessage
		Errors []struct{ Type, Message string }
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return &failure{code: "unavailable", status: http.StatusServiceUnavailable}
	}
	if len(envelope.Errors) > 0 {
		for _, e := range envelope.Errors {
			if e.Type == "RATE_LIMITED" || limitMessage(e.Message) {
				return &failure{code: "unavailable", status: http.StatusServiceUnavailable, retryAfterSeconds: retryHint(resp.Header, time.Now())}
			}
			if e.Type == "INTERNAL" {
				return &failure{code: "unavailable", status: http.StatusServiceUnavailable}
			}
		}
		return &failure{code: "failed", status: http.StatusConflict}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return &failure{code: "unavailable", status: http.StatusServiceUnavailable}
	}
	return nil
}

// rateLimited recognizes GitHub's documented primary/secondary limit signals.
// Provider prose is inspected only in bounded decoded fields, never returned.
func rateLimited(h http.Header, raw []byte) bool {
	if h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != "" {
		return true
	}
	var body struct {
		Message string
		Errors  []struct{ Type, Code, Message string }
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	if limitMessage(body.Message) {
		return true
	}
	for _, e := range body.Errors {
		if e.Type == "RATE_LIMITED" || e.Code == "rate_limit_exceeded" || e.Code == "secondary_rate_limit" || limitMessage(e.Message) {
			return true
		}
	}
	return false
}

func limitMessage(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "secondary rate limit") || strings.Contains(message, "api rate limit exceeded") ||
		strings.Contains(message, "abuse detection mechanism")
}

// retryHint is advisory, clamped to 1..3600 seconds; callers must still read
// membership/head before any new mutation. No sleeps or in-call retries occur.
func retryHint(h http.Header, now time.Time) int {
	seconds := int64(60) // GitHub recommends at least a minute for secondary limits.
	if value := h.Get("Retry-After"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			seconds = n
		} else if date, err := http.ParseTime(value); err == nil {
			seconds = int64(date.Sub(now).Seconds()) + 1
		}
	} else if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > now.Unix() {
			// Bound before subtraction, including maliciously huge reset values.
			if reset > now.Unix()+3600 {
				return 3600
			}
			seconds = reset - now.Unix()
		}
	}
	if seconds < 1 {
		return 1
	}
	if seconds > 3600 {
		return 3600
	}
	return int(seconds)
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
