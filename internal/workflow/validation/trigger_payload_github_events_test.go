// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The GitHub events an automation reacts to beyond "opened": labels added and
// removed, reopenings, inline diff comments and CI results. Each declares its
// payload, so a trigger's filter, inputs and prompt are checked against it.

func TestGitHubAutomationEventPayloadsAcceptWhatRuns(t *testing.T) {
	cases := map[string]struct{ events, body string }{
		"pr label as a name": {"pull_request.labeled",
			"    filter: \"trigger.payload.data.label == 'agent:ready' && !('wip' in trigger.payload.data.pull_request.labels)\"\n"},
		"pr unlabeled": {"pull_request.unlabeled",
			"    filter: \"trigger.payload.data.label in ['agent:ready', 'agent:respond']\"\n"},
		"pr label inputs": {"pull_request.labeled",
			"    inputs:\n      issue_number: \"{{ trigger.payload.data.pull_request.number }}\"\n      title: \"{{ trigger.payload.data.label }}\"\n" +
				"    prompt: \"Re-review {{ trigger.payload.data.pull_request.html_url }} on {{ trigger.payload.data.pull_request.head.ref }}\"\n"},
		"pr reopened":     {"pull_request.reopened", "    filter: \"trigger.payload.data.pull_request.base.ref == 'main'\"\n"},
		"issue unlabeled": {"issues.unlabeled", "    filter: \"trigger.payload.data.label == 'agent:ready' && !('agent:ready' in trigger.payload.data.issue.labels)\"\n"},
		"issue reopened": {"issues.reopened",
			"    filter: \"trigger.payload.data.issue.state_reason == 'reopened'\"\n    inputs:\n      issue_number: \"{{ trigger.payload.data.issue.number }}\"\n"},
		"inline comment": {"pull_request_review_comment.created",
			"    filter: \"trigger.payload.data.comment.path.endsWith('.go') && trigger.payload.data.comment.user.login != 'dependabot[bot]'\"\n" +
				"    inputs:\n      issue_number: \"{{ trigger.payload.data.pull_request.number }}\"\n" +
				"    prompt: \"{{ trigger.payload.data.comment.user.login }} on {{ trigger.payload.data.comment.path }}:{{ trigger.payload.data.comment.line }}: {{ trigger.payload.data.comment.body }}\"\n"},
		"inline comment optional fields": {"pull_request_review_comment.created",
			"    filter: \"has(trigger.payload.data.comment.in_reply_to_id) ? trigger.payload.data.comment.in_reply_to_id > 0 : true\"\n"},
		"inline comment attributes": {"pull_request_review_comment.created",
			"    filter: \"trigger.payload.attributes['is_reply'] == 'false' && trigger.payload.attributes['mentions_reliant'] == 'true'\"\n"},
		"check run": {"check_run.completed",
			"    filter: \"trigger.payload.data.check_run.conclusion == 'failure' && trigger.payload.data.check_run.app.slug == 'github-actions' && 7 in trigger.payload.data.check_run.pull_requests\"\n" +
				"    prompt: \"{{ trigger.payload.data.check_run.name }} failed: {{ trigger.payload.data.check_run.output.summary }}\"\n"},
		"check suite": {"check_suite.completed",
			"    filter: \"trigger.payload.data.check_suite.conclusion == 'success' && size(trigger.payload.data.check_suite.pull_requests) > 0\"\n" +
				"    inputs:\n      issue_number: \"{{ trigger.payload.data.check_suite.pull_requests[0] }}\"\n"},
		"several new events together": {"issues.unlabeled, issues.reopened",
			"    filter: \"trigger.payload.data.issue.number > 0\"\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wf := triggerWorkflow(t, githubTrigger(tc.events, tc.body))
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
}

func TestGitHubAutomationEventPayloadTypeErrors(t *testing.T) {
	cases := map[string]struct {
		events, body string
		want         []string
	}{
		"pr label is a name, not an object": {
			"pull_request.labeled",
			"    filter: \"trigger.payload.data.label.name == 'x'\"\n",
			[]string{"filter", "trigger.payload.data.label", "string", "field 'name'"},
		},
		"pr labels are names": {
			"pull_request.unlabeled",
			"    filter: \"trigger.payload.data.pull_request.labels.exists(l, l.name == 'x')\"\n",
			[]string{"filter", "trigger.payload.data.pull_request.labels", "list(string)", "field 'name'"},
		},
		"an inline comment has no issue": {
			"pull_request_review_comment.created",
			"    filter: \"trigger.payload.data.issue.number > 0\"\n",
			[]string{"filter", "no field 'issue'"},
		},
		"a check suite has no check run": {
			"check_suite.completed",
			"    filter: \"trigger.payload.data.check_run.conclusion == 'failure'\"\n",
			[]string{"filter", "no field 'check_run'"},
		},
		"a check run's app is an object, not a string": {
			"check_run.completed",
			"    filter: \"trigger.payload.data.check_run.app == 'github-actions'\"\n",
			[]string{"filter", "trigger.payload.data.check_run.app"},
		},
		"a reopened issue has no label": {
			"issues.reopened",
			"    filter: \"trigger.payload.data.label == 'bug'\"\n",
			[]string{"filter", "no field 'label'"},
		},
		"a reopened pull request has no label": {
			"pull_request.reopened",
			"    filter: \"trigger.payload.data.label == 'bug'\"\n",
			[]string{"filter", "no field 'label'"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wf := triggerWorkflow(t, githubTrigger(tc.events, tc.body))
			requireTriggerFinding(t, StaticAnalysisWithOptions(wf, nil), SeverityError, tc.want...)
		})
	}
}

// `match` keys are checked against the attributes GitHub's events declare: the
// new ones are accepted, and a key no GitHub event carries is an error rather
// than a trigger that never fires. (The check is per integration, not per
// event.)
func TestGitHubAutomationEventMatchKeys(t *testing.T) {
	match := func(events, kv string) string {
		return "  - name: gh\n    integration:\n      integration: github\n      events: [" + events + "]\n      match: {" + kv + "}\n"
	}
	for name, tc := range map[string]struct{ events, kv string }{
		"pr label":               {"pull_request.labeled", "label: agent:ready, base_branch: main"},
		"pr unlabeled":           {"pull_request.unlabeled", "label: agent:ready"},
		"issue unlabeled":        {"issues.unlabeled", "label: agent:ready"},
		"inline comment":         {"pull_request_review_comment.created", "is_reply: 'false', mentions_reliant: 'true'"},
		"check run":              {"check_run.completed", "conclusion: failure, app: github-actions, name: build, branch: main"},
		"check suite":            {"check_suite.completed", "conclusion: success, app: github-actions"},
		"a wildcard of an event": {"pull_request_review_comment.*", "is_reply: 'false'"},
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			wf := triggerWorkflow(t, match(tc.events, tc.kv))
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
	for name, tc := range map[string]struct{ events, kv string }{
		"a made-up key on an inline comment":  {"pull_request_review_comment.created", "reviewer: octocat"},
		"a made-up key on a check run":        {"check_run.completed", "status_text: failed"},
		"a payload field is not an attribute": {"pull_request.labeled", "pull_request: x"},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			wf := triggerWorkflow(t, match(tc.events, tc.kv))
			assert.NotEmpty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
}
