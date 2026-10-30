// Copyright (c) 2025 Reliant Labs

// Package ghmerge is the Go half of github/pr.merge@1
// (catalog/github/actions/pr.merge.yaml): arming, disarming, queueing and
// merging a pull request, which GitHub exposes only through GraphQL
// mutations keyed by the pull request's node id. The executor reads the pull
// request first (its node id, head and merge state), then sends one
// mutation. Both are declarative requests run through httpaction's
// ExecutorCall.Do, so they are authenticated, host-pinned, error-classified
// (catalog/github/schemas/github_errors.yaml) and scrubbed exactly as every
// other GitHub action.
//
// Importing the package registers the executor in httpaction.Executors().
//
// forge:outbound-io
//
// Two GraphQL POSTs to the github manifest's base_url, as the call's
// connection (the owner, or the Reliant bot for an automation that writes as
// it), through the guarded runner's HTTP client.
package ghmerge

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Executor is the name the manifest writes after "go:".
const Executor = "github.pr_merge"

func init() {
	httpaction.Executors().MustRegister(Executor, run)
}

// The four things pr.merge does.
const (
	ActionEnableAuto  = "enable_auto"
	ActionDisableAuto = "disable_auto"
	ActionDequeue     = "dequeue"
	ActionMerge       = "merge"
)

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// GitHub's answer to arming a pull request that is already mergeable.
	alreadyMergeable = regexp.MustCompile(`(?i)(clean|unstable) status`)
)

const lookupQuery = `query($o: String!, $r: String!, $n: Int!) { repository(owner: $o, name: $r) { pullRequest(number: $n) {
  id databaseId number url state headRefOid isMergeQueueEnabled isInMergeQueue autoMergeRequest { enabledAt } } } }`

// pullRequest is what the lookup reads.
type pullRequest struct {
	NodeID     string
	DatabaseID any
	Number     any
	URL        string
	State      string
	Head       string
	QueueOn    bool
	InQueue    bool
	AutoMerge  bool
}

func run(ctx context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
	p := call.Params
	owner, repo := str(p["owner"]), str(p["repo"])
	number := p["pull_number"]
	action, expect := str(p["action"]), strings.ToLower(str(p["match_head_commit"]))
	method := strings.ToUpper(str(p["method"]))
	if method == "" {
		method = "SQUASH"
	}
	if (action == ActionEnableAuto || action == ActionMerge) && !shaRe.MatchString(expect) {
		return nil, fmt.Errorf("invalid params: %s needs match_head_commit, the full 40-character sha of the head that was reviewed", action)
	}
	errs, err := errorRules(call.Manifest)
	if err != nil {
		return nil, err
	}

	res, data, err := graphql(ctx, call, errs, "pr.merge.lookup", lookupQuery, map[string]any{"o": owner, "r": repo, "n": number})
	if res != nil || err != nil {
		return res, err
	}
	pr, ok := readPR(data)
	if !ok {
		return refuse(404, fmt.Sprintf("GitHub returned not found: %s/%s#%v does not exist, or this connection cannot see it.", owner, repo, number)), nil
	}
	if pr.State != "OPEN" {
		return refuse(422, fmt.Sprintf("Pull request %s/%s#%v is %s: nothing to %s.", owner, repo, number, strings.ToLower(pr.State), strings.ReplaceAll(action, "_", " "))), nil
	}
	if expect != "" && pr.Head != expect {
		// The reviewed head is no longer the head: arming or merging now would
		// merge code nobody reviewed. Never retried.
		return refuse(409, fmt.Sprintf("The head of %s/%s#%v is %s, not %s (match_head_commit): it moved after the review, so nothing was changed.", owner, repo, number, pr.Head, expect)), nil
	}

	var state string
	switch action {
	case ActionDisableAuto:
		if !pr.AutoMerge {
			return done(pr, action, "unchanged", false), nil
		}
		res, _, err = graphql(ctx, call, errs, "pr.merge.disable_auto",
			`mutation($id: ID!) { disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId } }`,
			map[string]any{"id": pr.NodeID})
		state = "auto_merge_disabled"
	case ActionDequeue:
		if !pr.InQueue {
			return done(pr, action, "unchanged", false), nil
		}
		res, _, err = graphql(ctx, call, errs, "pr.merge.dequeue",
			`mutation($id: ID!) { dequeuePullRequest(input: {id: $id}) { clientMutationId } }`,
			map[string]any{"id": pr.NodeID})
		state = "dequeued"
	case ActionEnableAuto:
		if pr.InQueue || pr.AutoMerge {
			// Queued or armed already. A queue entry or an arming carries no
			// head of its own the API shows (pr-disarm undoes stale ones), so
			// this keeps the existing one, as gh pr merge --auto does.
			return done(pr, action, "unchanged", false), nil
		}
		res, _, err = graphql(ctx, call, errs, "pr.merge.enable_auto",
			`mutation($id: ID!, $m: PullRequestMergeMethod!, $h: GitObjectID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $m, expectedHeadOid: $h}) { clientMutationId } }`,
			map[string]any{"id": pr.NodeID, "m": method, "h": expect})
		state = "auto_merge_enabled"
		// Already mergeable: GitHub refuses to arm and wants it merged (or
		// queued) now, as gh pr merge --auto then does.
		if res != nil && res.IsError && alreadyMergeable.MatchString(res.Content) {
			res, state, err = mergeNow(ctx, call, errs, pr, method, expect)
		}
	case ActionMerge:
		res, state, err = mergeNow(ctx, call, errs, pr, method, expect)
	default:
		return nil, fmt.Errorf("invalid params: action %q is not enable_auto, disable_auto, dequeue or merge", action)
	}
	if res != nil || err != nil {
		return res, err
	}
	return done(pr, action, state, true), nil
}

// mergeNow merges at the expected head, or joins the merge queue when the
// base branch requires one (a merge there IS the queue).
func mergeNow(ctx context.Context, call httpaction.ExecutorCall, errs []*reliantv1.ErrorRule, pr pullRequest, method, expect string) (*httpaction.Result, string, error) {
	if pr.QueueOn {
		res, _, err := graphql(ctx, call, errs, "pr.merge.enqueue",
			`mutation($id: ID!, $h: GitObjectID!) { enqueuePullRequest(input: {pullRequestId: $id, expectedHeadOid: $h}) { clientMutationId } }`,
			map[string]any{"id": pr.NodeID, "h": expect})
		return res, "queued", err
	}
	res, _, err := graphql(ctx, call, errs, "pr.merge.merge",
		`mutation($id: ID!, $m: PullRequestMergeMethod!, $h: GitObjectID!) { mergePullRequest(input: {pullRequestId: $id, mergeMethod: $m, expectedHeadOid: $h}) { clientMutationId } }`,
		map[string]any{"id": pr.NodeID, "m": method, "h": expect})
	return res, "merged", err
}

// graphql sends one GraphQL request. A non-nil Result is the call's answer
// (an HTTP error, classified by the manifest's rules, or a GraphQL error);
// otherwise data is the response's `data`.
func graphql(ctx context.Context, call httpaction.ExecutorCall, errs []*reliantv1.ErrorRule, id, query string, vars map[string]any) (*httpaction.Result, map[string]any, error) {
	sub := &reliantv1.ActionSpec{
		Id: id,
		Request: &reliantv1.HttpRequestSpec{
			Method: "POST", Path: "/graphql", BodyExpr: "params.body", Errors: errs,
		},
	}
	res, err := call.Do(ctx, sub, map[string]any{"body": map[string]any{"query": query, "variables": vars}})
	if err != nil || res.IsError {
		return res, nil, err
	}
	if list, ok := res.Data["errors"].([]any); ok && len(list) > 0 {
		var msgs []string
		kind := ""
		for _, e := range list {
			m, _ := e.(map[string]any)
			msgs = append(msgs, str(m["message"]))
			if kind == "" {
				kind = str(m["type"])
			}
		}
		status := 422
		switch kind {
		case "NOT_FOUND":
			status = 404
		case "FORBIDDEN":
			status = 403
		}
		return refuse(status, "GitHub refused it: "+strings.Join(msgs, "; ")), nil, nil
	}
	data, _ := res.Data["data"].(map[string]any)
	return nil, data, nil
}

func readPR(data map[string]any) (pullRequest, bool) {
	repo, _ := data["repository"].(map[string]any)
	p, _ := repo["pullRequest"].(map[string]any)
	if p == nil || str(p["id"]) == "" {
		return pullRequest{}, false
	}
	return pullRequest{
		NodeID: str(p["id"]), DatabaseID: p["databaseId"], Number: p["number"], URL: str(p["url"]),
		State: str(p["state"]), Head: str(p["headRefOid"]),
		QueueOn: p["isMergeQueueEnabled"] == true, InQueue: p["isInMergeQueue"] == true,
		AutoMerge: p["autoMergeRequest"] != nil,
	}, true
}

// errorRules are the GitHub manifest's own error rules (github_errors.yaml),
// read from a declarative action every GitHub manifest ships: one source of
// how a GitHub failure reads.
func errorRules(m *reliantv1.IntegrationManifest) ([]*reliantv1.ErrorRule, error) {
	for _, a := range m.GetActions() {
		if a.GetId() == "pr.get" {
			return a.GetRequest().GetErrors(), nil
		}
	}
	return nil, fmt.Errorf("%s: the manifest has no pr.get action to take GitHub's error rules from", m.GetId())
}

func done(pr pullRequest, action, state string, changed bool) *httpaction.Result {
	return &httpaction.Result{StatusCode: 200, Data: map[string]any{
		"id": pr.DatabaseID, "node_id": pr.NodeID, "number": pr.Number, "html_url": pr.URL,
		"head_sha": pr.Head, "action": action, "state": state, "changed": changed,
	}}
}

func refuse(status int, msg string) *httpaction.Result {
	return &httpaction.Result{IsError: true, StatusCode: status, Content: msg}
}

func str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		return fmt.Sprint(s)
	}
}
