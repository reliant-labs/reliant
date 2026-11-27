// Copyright (c) 2025 Reliant Labs

package ghrest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

// DefaultBaseURL is github.com's REST API.
const DefaultBaseURL = "https://api.github.com"

// apiVersion is the REST API version every request pins.
const apiVersion = "2022-11-28"

// userAgent names the queue to GitHub.
const userAgent = "reliant-merge-queue"

// Client is the [Service] over GitHub's REST API. It is safe for concurrent
// use.
type Client struct {
	base  string
	token TokenFunc
	http  *http.Client
}

var (
	_ Service       = (*Client)(nil)
	_ ghport.Client = (Service)(nil)
)

// New returns the adapter. A nil Deps.Token fails every call: a client that
// cannot authenticate never reaches GitHub.
//
// forge:constructor
func New(deps Deps) Service {
	hc := deps.HTTPClient
	if hc == nil {
		hc = observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second})
	}
	return &Client{base: DefaultBaseURL, token: deps.Token, http: hc}
}

// APIError is a GitHub answer none of ghport's typed errors describes: a 401,
// or a 403 that is not a rate limit (the token lacks a permission).
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ghrest: GitHub answered %d: %s", e.Status, e.Message)
}

// ---- refs ----

type refBody struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// GetRef implements [ghport.Client].
func (c *Client) GetRef(ctx context.Context, repo ghport.Repo, ref string) (ghport.Ref, error) {
	if err := ghport.ValidateRef(ref); err != nil {
		return ghport.Ref{}, err
	}
	var out refBody
	if err := c.do(ctx, repo, http.MethodGet, "git/ref/"+refPath(ref), nil, &out, "ref "+ref); err != nil {
		return ghport.Ref{}, err
	}
	return ghport.Ref{Name: out.Ref, SHA: out.Object.SHA}, nil
}

// CreateRef implements [ghport.Client].
func (c *Client) CreateRef(ctx context.Context, repo ghport.Repo, ref, sha string) error {
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	err := c.do(ctx, repo, http.MethodPost, "git/refs", map[string]string{"ref": ref, "sha": sha}, nil, "ref "+ref)
	var v *ghport.ValidationError
	if errors.As(err, &v) && strings.Contains(strings.ToLower(v.Reason), "already exists") {
		return fmt.Errorf("%w: ref %s", ghport.ErrAlreadyExists, ref)
	}
	return err
}

// UpdateRef implements [ghport.Client].
func (c *Client) UpdateRef(ctx context.Context, repo ghport.Repo, ref, sha string, force bool) error {
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	err := c.do(ctx, repo, http.MethodPatch, "git/refs/"+refPath(ref), map[string]any{"sha": sha, "force": force}, nil, "ref "+ref)
	var v *ghport.ValidationError
	if errors.As(err, &v) {
		reason := strings.ToLower(v.Reason)
		switch {
		case strings.Contains(reason, "fast forward") || strings.Contains(reason, "fast-forward"):
			return fmt.Errorf("%w: %s to %s", ghport.ErrNotFastForward, ref, sha)
		case strings.Contains(reason, "does not exist"):
			return &ghport.NotFoundError{What: "ref " + ref}
		}
	}
	return err
}

// DeleteRef implements [ghport.Client].
func (c *Client) DeleteRef(ctx context.Context, repo ghport.Repo, ref string) error {
	if err := ghport.ValidateRef(ref); err != nil {
		return err
	}
	err := c.do(ctx, repo, http.MethodDelete, "git/refs/"+refPath(ref), nil, nil, "ref "+ref)
	var v *ghport.ValidationError
	if errors.As(err, &v) && strings.Contains(strings.ToLower(v.Reason), "does not exist") {
		return &ghport.NotFoundError{What: "ref " + ref}
	}
	return err
}

// refPath is a fully qualified ref as the git/refs endpoints name it:
// without "refs/", each segment escaped.
func refPath(ref string) string {
	parts := strings.Split(strings.TrimPrefix(ref, "refs/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// ---- pull requests ----

type prBody struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
}

func (b prBody) port() ghport.PullRequest {
	out := ghport.PullRequest{
		Number: b.Number, Title: b.Title, State: ghport.PRState(b.State),
		HeadSHA: b.Head.SHA, HeadRef: b.Head.Ref, BaseRef: b.Base.Ref,
		Merged: b.Merged, Mergeable: ghport.MergeableUnknown,
	}
	if b.Merged {
		out.MergeCommitSHA = b.MergeCommitSHA
	}
	switch {
	case b.MergeableState == "dirty" || (b.Mergeable != nil && !*b.Mergeable):
		out.Mergeable = ghport.MergeableConflicting
	case b.Mergeable != nil && *b.Mergeable:
		out.Mergeable = ghport.MergeableClean
	}
	return out
}

// GetPullRequest implements [ghport.Client].
func (c *Client) GetPullRequest(ctx context.Context, repo ghport.Repo, number int) (ghport.PullRequest, error) {
	b, err := c.getPR(ctx, repo, number)
	if err != nil {
		return ghport.PullRequest{}, err
	}
	return b.port(), nil
}

func (c *Client) getPR(ctx context.Context, repo ghport.Repo, number int) (prBody, error) {
	var out prBody
	err := c.do(ctx, repo, http.MethodGet, "pulls/"+strconv.Itoa(number), nil, &out, fmt.Sprintf("pull request #%d", number))
	return out, err
}

// MergePullRequest implements [ghport.Client]. A 405 is told apart by
// re-reading the PR: GitHub answers 405 both for a PR that already merged
// and for one it will not merge.
func (c *Client) MergePullRequest(ctx context.Context, repo ghport.Repo, number int, req ghport.MergeRequest) (ghport.MergeResult, error) {
	if req.Method == "" {
		req.Method = ghport.MergeSquash
	}
	body := map[string]string{"merge_method": string(req.Method)}
	if req.ExpectedHeadSHA != "" {
		body["sha"] = req.ExpectedHeadSHA
	}
	if req.CommitTitle != "" {
		body["commit_title"] = req.CommitTitle
	}
	if req.CommitMessage != "" {
		body["commit_message"] = req.CommitMessage
	}
	var out struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	err := c.do(ctx, repo, http.MethodPut, "pulls/"+strconv.Itoa(number)+"/merge", body, &out, fmt.Sprintf("pull request #%d", number))
	var status *statusError
	switch {
	case err == nil:
		return ghport.MergeResult{SHA: out.SHA}, nil
	case errors.As(err, &status) && status.status == http.StatusConflict:
		actual := ""
		if pr, rerr := c.getPR(ctx, repo, number); rerr == nil {
			actual = pr.Head.SHA
		}
		return ghport.MergeResult{}, &ghport.HeadMismatchError{Expected: req.ExpectedHeadSHA, Actual: actual}
	case errors.As(err, &status) && status.status == http.StatusMethodNotAllowed:
		pr, rerr := c.getPR(ctx, repo, number)
		if rerr != nil {
			// Cannot tell merged from refused: retryable, so the caller
			// asks again and the next 405 is read with the PR.
			return ghport.MergeResult{}, &ghport.TransientError{Status: http.StatusMethodNotAllowed,
				Message: "merge refused and the pull request could not be re-read: " + rerr.Error()}
		}
		switch {
		case pr.Merged:
			return ghport.MergeResult{}, &ghport.AlreadyMergedError{MergeCommitSHA: pr.MergeCommitSHA, HeadSHA: pr.Head.SHA}
		case pr.State == string(ghport.PRStateClosed):
			return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableClosed, Message: status.message}
		case pr.MergeableState == "dirty" || (pr.Mergeable != nil && !*pr.Mergeable):
			return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableConflict, Message: status.message}
		default:
			return ghport.MergeResult{}, &ghport.NotMergeableError{Reason: ghport.NotMergeableBlocked, Message: status.message}
		}
	}
	return ghport.MergeResult{}, err
}

// ---- check runs ----

type checkBody struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	ExternalID string `json:"external_id"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

func (b checkBody) port() ghport.CheckRun {
	return ghport.CheckRun{
		ID: b.ID, Name: b.Name, HeadSHA: b.HeadSHA, Status: ghport.CheckStatus(b.Status),
		Conclusion: ghport.CheckConclusion(b.Conclusion), ExternalID: b.ExternalID, DetailsURL: b.DetailsURL,
		Output: ghport.CheckOutput{Title: b.Output.Title, Summary: b.Output.Summary, Text: b.Output.Text},
	}
}

func outputBody(o ghport.CheckOutput) map[string]string {
	if o.Title == "" && o.Summary == "" && o.Text == "" {
		return nil
	}
	m := map[string]string{"title": o.Title, "summary": o.Summary}
	if o.Text != "" {
		m["text"] = o.Text
	}
	return m
}

// CreateCheckRun implements [ghport.Client].
func (c *Client) CreateCheckRun(ctx context.Context, repo ghport.Repo, in ghport.CreateCheckRun) (ghport.CheckRun, error) {
	status := in.Status
	if status == "" && in.Conclusion != "" {
		status = ghport.CheckCompleted
	}
	if err := ghport.ValidateCheckRun(status, in.Conclusion); err != nil {
		return ghport.CheckRun{}, err
	}
	body := map[string]any{"name": in.Name, "head_sha": in.HeadSHA}
	if status != "" {
		body["status"] = status
	}
	if in.Conclusion != "" {
		body["conclusion"] = in.Conclusion
	}
	if o := outputBody(in.Output); o != nil {
		body["output"] = o
	}
	if in.ExternalID != "" {
		body["external_id"] = in.ExternalID
	}
	if in.DetailsURL != "" {
		body["details_url"] = in.DetailsURL
	}
	var out checkBody
	if err := c.do(ctx, repo, http.MethodPost, "check-runs", body, &out, "commit "+in.HeadSHA); err != nil {
		return ghport.CheckRun{}, err
	}
	return out.port(), nil
}

// UpdateCheckRun implements [ghport.Client].
func (c *Client) UpdateCheckRun(ctx context.Context, repo ghport.Repo, id int64, in ghport.UpdateCheckRun) (ghport.CheckRun, error) {
	body := map[string]any{}
	if in.Status != "" {
		body["status"] = in.Status
	}
	if in.Conclusion != "" {
		body["conclusion"] = in.Conclusion
	}
	if in.Output != nil {
		if o := outputBody(*in.Output); o != nil {
			body["output"] = o
		}
	}
	if in.DetailsURL != "" {
		body["details_url"] = in.DetailsURL
	}
	var out checkBody
	if err := c.do(ctx, repo, http.MethodPatch, "check-runs/"+strconv.FormatInt(id, 10), body, &out, fmt.Sprintf("check run %d", id)); err != nil {
		return ghport.CheckRun{}, err
	}
	return out.port(), nil
}

// ListCheckRuns implements [ghport.Client]: every page, newest first.
func (c *Client) ListCheckRuns(ctx context.Context, repo ghport.Repo, sha string, opts ghport.ListCheckRuns) ([]ghport.CheckRun, error) {
	q := url.Values{"per_page": {"100"}, "filter": {"all"}}
	if opts.Name != "" {
		q.Set("check_name", opts.Name)
	}
	var out []ghport.CheckRun
	err := c.pages(ctx, repo, "commits/"+url.PathEscape(sha)+"/check-runs?"+q.Encode(), "commit "+sha, func(raw []byte) error {
		var page struct {
			CheckRuns []checkBody `json:"check_runs"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, b := range page.CheckRuns {
			out = append(out, b.port())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ---- contents and commits ----

// GetContents implements [ghport.Client].
func (c *Client) GetContents(ctx context.Context, repo ghport.Repo, path, ref string) ([]byte, error) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	endpoint := "contents/" + strings.Join(segs, "/")
	if ref != "" {
		endpoint += "?ref=" + url.QueryEscape(ref)
	}
	what := fmt.Sprintf("file %s at %s", path, ref)
	var raw json.RawMessage
	if err := c.do(ctx, repo, http.MethodGet, endpoint, nil, &raw, what); err != nil {
		return nil, err
	}
	var file struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	// A directory is a JSON array: not a regular file.
	if err := json.Unmarshal(raw, &file); err != nil || file.Type != "file" {
		return nil, &ghport.NotFoundError{What: what}
	}
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("ghrest: %s is too large for the contents API (encoding %q)", what, file.Encoding)
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("ghrest: decoding %s: %w", what, err)
	}
	return data, nil
}

type commitBody struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Message string `json:"message"`
}

func (b commitBody) port() ghport.Commit {
	out := ghport.Commit{SHA: b.SHA, TreeSHA: b.Tree.SHA, Message: b.Message}
	for _, p := range b.Parents {
		out.Parents = append(out.Parents, p.SHA)
	}
	return out
}

// GetCommit implements [ghport.Client].
func (c *Client) GetCommit(ctx context.Context, repo ghport.Repo, sha string) (ghport.Commit, error) {
	var out commitBody
	if err := c.do(ctx, repo, http.MethodGet, "git/commits/"+url.PathEscape(sha), nil, &out, "commit "+sha); err != nil {
		return ghport.Commit{}, err
	}
	return out.port(), nil
}

// CreateCommit implements [ghport.Client].
func (c *Client) CreateCommit(ctx context.Context, repo ghport.Repo, in ghport.NewCommit) (ghport.Commit, error) {
	parents := in.Parents
	if parents == nil {
		parents = []string{}
	}
	var out commitBody
	if err := c.do(ctx, repo, http.MethodPost, "git/commits",
		map[string]any{"message": in.Message, "tree": in.TreeSHA, "parents": parents}, &out, "tree "+in.TreeSHA); err != nil {
		return ghport.Commit{}, err
	}
	return out.port(), nil
}

// ---- comments ----

type commentBody struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// CreateIssueComment implements [ghport.Client].
func (c *Client) CreateIssueComment(ctx context.Context, repo ghport.Repo, number int, body string) (ghport.IssueComment, error) {
	var out commentBody
	if err := c.do(ctx, repo, http.MethodPost, "issues/"+strconv.Itoa(number)+"/comments",
		map[string]string{"body": body}, &out, fmt.Sprintf("pull request #%d", number)); err != nil {
		return ghport.IssueComment{}, err
	}
	return ghport.IssueComment{ID: out.ID, Body: out.Body}, nil
}

// ListIssueComments implements [ghport.Client]: every page, oldest first.
func (c *Client) ListIssueComments(ctx context.Context, repo ghport.Repo, number int) ([]ghport.IssueComment, error) {
	var out []ghport.IssueComment
	err := c.pages(ctx, repo, "issues/"+strconv.Itoa(number)+"/comments?per_page=100", fmt.Sprintf("pull request #%d", number), func(raw []byte) error {
		var page []commentBody
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, b := range page {
			out = append(out, ghport.IssueComment{ID: b.ID, Body: b.Body})
		}
		return nil
	})
	return out, err
}

// ---- transport ----

// statusError is a non-2xx answer before it is mapped: the callers that
// read a status themselves (a merge's 405 and 409) look for it.
type statusError struct {
	status  int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("ghrest: GitHub answered %d: %s", e.status, e.message)
}

// maxPages bounds a listing: 100 per page, so 5,000 items.
const maxPages = 50

// pages GETs endpoint and every page its Link header names next.
func (c *Client) pages(ctx context.Context, repo ghport.Repo, endpoint, what string, each func([]byte) error) error {
	next := c.repoURL(repo, endpoint)
	for range maxPages {
		raw, link, err := c.send(ctx, repo, http.MethodGet, next, nil, what)
		if err != nil {
			return err
		}
		if err := each(raw); err != nil {
			return fmt.Errorf("ghrest: decoding %s: %w", what, err)
		}
		if next = nextLink(link); next == "" {
			return nil
		}
		if !strings.HasPrefix(next, c.base+"/") {
			return fmt.Errorf("ghrest: refusing a next page outside %s", c.base)
		}
	}
	return fmt.Errorf("ghrest: %s has more than %d pages", what, maxPages)
}

// nextLink returns the rel="next" URL of a Link header, or "".
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		u, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(params, `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(u), "<>")
	}
	return ""
}

func (c *Client) repoURL(repo ghport.Repo, endpoint string) string {
	return c.base + "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/" + endpoint
}

// do sends one request to a repository endpoint and decodes a 2xx body into
// out (nil discards it). what names the object for a 404.
func (c *Client) do(ctx context.Context, repo ghport.Repo, method, endpoint string, in, out any, what string) error {
	raw, _, err := c.send(ctx, repo, method, c.repoURL(repo, endpoint), in, what)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ghrest: decoding %s %s: %w", method, endpoint, err)
	}
	return nil
}

// send performs one request and maps a non-2xx answer. It returns the body
// and the Link header.
func (c *Client) send(ctx context.Context, repo ghport.Repo, method, target string, in any, what string) ([]byte, string, error) {
	if c.token == nil {
		return nil, "", errors.New("ghrest: no token source configured")
	}
	token, err := c.token(ctx, repo)
	if err != nil {
		return nil, "", fmt.Errorf("ghrest: token for %s: %w", repo, err)
	}
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return nil, "", err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		// The request may or may not have reached GitHub: transient, and the
		// queue's writes are idempotent on retry. The error never wraps the
		// request (no credential can ride along).
		return nil, "", &ghport.TransientError{Status: 0, Message: method + " " + redactURL(target) + ": " + errText(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", &ghport.TransientError{Status: resp.StatusCode, Message: "reading the response: " + err.Error()}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, resp.Header.Get("Link"), nil
	}
	return nil, "", mapStatus(resp, raw, what)
}

// redactURL drops a URL's query, which is never a credential here but keeps
// the habit.
func redactURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func errText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// mapStatus turns a non-2xx answer into ghport's typed errors.
func mapStatus(resp *http.Response, raw []byte, what string) error {
	var body struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Field   string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &body)
	msg := body.Message
	if len(msg) > 300 {
		msg = msg[:300]
	}
	status := resp.StatusCode
	if limited := rateLimit(resp, msg); limited != nil {
		return limited
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return &ghport.NotFoundError{What: what}
	case status == http.StatusConflict || status == http.StatusMethodNotAllowed:
		return &statusError{status: status, message: msg}
	case status == http.StatusUnprocessableEntity:
		reason := msg
		field := ""
		for _, e := range body.Errors {
			if e.Message != "" {
				reason += ": " + e.Message
			}
			if field == "" {
				field = e.Field
			}
		}
		if field == "" {
			field = "request"
		}
		return &ghport.ValidationError{Field: field, Reason: reason}
	case status >= 500:
		return &ghport.TransientError{Status: status, Message: msg}
	}
	return &APIError{Status: status, Message: msg}
}

// rateLimit reads GitHub's primary and secondary rate-limit answers: a 403
// or 429 with X-RateLimit-Remaining: 0 (reset at X-RateLimit-Reset), with a
// Retry-After, or whose message names a secondary rate limit.
func rateLimit(resp *http.Response, msg string) *ghport.RateLimitError {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	h := resp.Header
	if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && secs >= 0 {
		return &ghport.RateLimitError{Reset: time.Now().Add(time.Duration(secs) * time.Second), Secondary: true}
	}
	if strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" {
		out := &ghport.RateLimitError{}
		if epoch, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && epoch > 0 {
			out.Reset = time.Unix(epoch, 0)
		}
		return out
	}
	if strings.Contains(strings.ToLower(msg), "secondary rate limit") || resp.StatusCode == http.StatusTooManyRequests {
		return &ghport.RateLimitError{Secondary: true}
	}
	return nil
}
