// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

// plan is what one call intends, derived from its action and params BEFORE the
// request goes out: where it writes, and what its echo will look like.
type plan struct {
	scope       string
	target      string
	correlation provenance.Correlation
	// objectFrom says where the new object's id comes from in the response:
	// "number" (an issue or pull request is created), "id" (a comment, review
	// or reply), or "" (the write touches an existing object and creates none).
	objectFrom string
	scopeOnly  bool
}

// completion is the target, object id and url the response names. target is ""
// when the response changes nothing about it.
func (p plan) completion(data map[string]any) (target, objectID, url string) {
	url = provenance.IDString(data["html_url"])
	switch p.objectFrom {
	case "number":
		objectID = provenance.IDString(data["number"])
		if objectID != "" {
			target = provenance.TargetOf(p.scope, objectID)
		}
	case "id":
		objectID = provenance.IDString(data["id"])
	}
	return target, objectID, url
}

// planFor plans a call. Only GitHub writes carry a correlation: the webhook
// parser that produces the matching echo exists for GitHub alone. Any other
// integration still gets a ledger row, with its scope the integration itself.
func planFor(integration, action string, params map[string]any) plan {
	if integration != "github" {
		scope := integration + ":"
		return plan{scope: scope, target: scope + action, scopeOnly: true}
	}
	owner := provenance.IDString(params["owner"])
	repo := provenance.IDString(params["repo"])
	if owner == "" || repo == "" {
		return plan{scope: "github:", target: "github:", scopeOnly: true}
	}
	scope := provenance.ScopeOf(owner + "/" + repo)
	p := plan{scope: scope, target: scope}
	onNumber := func(key string) { p.target = provenance.TargetOf(scope, provenance.IDString(params[key])) }

	switch action {
	case "issue.create":
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindIssue, Events: []string{"issues.opened"},
			BodySHA256: bodyDigest(params["body"]),
		}
		p.objectFrom = "number"
		addLabelsAndAssignees(&p, params, "issues")
	case "issue.comment":
		onNumber("issue_number")
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindComment, Events: []string{"issue_comment.created"},
			BodySHA256: bodyDigest(params["body"]),
		}
		p.objectFrom = "id"
	case "issue.comment.update":
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindComment, Events: []string{"issue_comment.edited"},
			BodySHA256: bodyDigest(params["body"]),
		}
	case "issue.labels.add":
		onNumber("issue_number")
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindIssue, Label: provenance.IDString(params["label"]),
			Events: []string{"issues.labeled", "pull_request.labeled"},
		}
	case "issue.labels.remove":
		onNumber("issue_number")
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindIssue, Label: provenance.IDString(params["label"]),
			Events: []string{"issues.unlabeled", "pull_request.unlabeled"},
		}
	case "issue.update":
		onNumber("issue_number")
		p.correlation = provenance.Correlation{ObjectKind: provenance.KindIssue, State: stateOf(params)}
		p.correlation.Events = issueUpdateEvents(params)
		addLabelsAndAssignees(&p, params, "issues")
	case "pr.create":
		head := provenance.IDString(params["head"])
		// A head of "fork:branch" names the branch after the colon.
		if _, after, ok := strings.Cut(head, ":"); ok {
			head = after
		}
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindPullRequest, Events: []string{"pull_request.opened"},
			Branch: head, BodySHA256: bodyDigest(params["body"]),
		}
		p.objectFrom = "number"
	case "pr.update":
		onNumber("pull_number")
		p.correlation = provenance.Correlation{ObjectKind: provenance.KindPullRequest, State: stateOf(params)}
		switch p.correlation.State {
		case "closed":
			p.correlation.Events = []string{"pull_request.closed"}
		case "open":
			p.correlation.Events = []string{"pull_request.reopened"}
		}
	case "pr.review.create":
		onNumber("pull_number")
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindReview, Events: []string{"pull_request_review.submitted"},
			BodySHA256: bodyDigest(params["body"]), ReviewState: reviewState(params["event"]),
			HeadSHA: provenance.IDString(params["commit_id"]),
		}
		p.objectFrom = "id"
	case "pr.review_comment.reply":
		onNumber("pull_number")
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindReviewComment, Events: []string{"pull_request_review_comment.created"},
			BodySHA256: bodyDigest(params["body"]),
		}
		p.objectFrom = "id"
	case "workflow.dispatch":
		p.correlation = provenance.Correlation{
			ObjectKind: provenance.KindBranch, Events: []string{"workflow_run.completed"},
			Branch: provenance.IDString(params["ref"]),
		}
	}
	return p
}

func addLabelsAndAssignees(p *plan, params map[string]any, family string) {
	if labels := stringList(params["labels"]); len(labels) > 0 {
		p.correlation.Labels = labels
		p.correlation.Events = append(p.correlation.Events, family+".labeled", family+".unlabeled")
		if family == "issues" {
			p.correlation.Events = append(p.correlation.Events, "pull_request.labeled", "pull_request.unlabeled")
		}
	}
	if assignees := stringList(params["assignees"]); len(assignees) > 0 {
		p.correlation.Assignees = assignees
		p.correlation.Events = append(p.correlation.Events, "issues.assigned")
	}
}

// issueUpdateEvents are the events an issue.update's params can produce. A
// number that names a pull request produces the pull_request.* twin.
func issueUpdateEvents(params map[string]any) []string {
	var events []string
	switch stateOf(params) {
	case "closed":
		events = append(events, "issues.closed", "pull_request.closed")
	case "open":
		events = append(events, "issues.reopened", "pull_request.reopened")
	}
	if params["title"] != nil || params["body"] != nil {
		events = append(events, "issues.edited")
	}
	return events
}

func stateOf(params map[string]any) string {
	return strings.ToLower(strings.TrimSpace(provenance.IDString(params["state"])))
}

// reviewState is a review verdict as the webhook parser records it.
func reviewState(v any) string {
	switch strings.ToUpper(strings.TrimSpace(provenance.IDString(v))) {
	case "APPROVE":
		return "approved"
	case "REQUEST_CHANGES":
		return "changes_requested"
	default:
		return "commented"
	}
}

func bodyDigest(v any) string {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return ""
	}
	return provenance.BodyDigest(s)
}

func stringList(v any) []string {
	items, _ := v.([]any)
	var out []string
	for _, it := range items {
		if s := strings.TrimSpace(provenance.IDString(it)); s != "" {
			out = append(out, s)
		}
	}
	if out == nil {
		if ss, ok := v.([]string); ok {
			for _, s := range ss {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
