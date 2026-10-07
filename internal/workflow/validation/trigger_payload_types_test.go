// Copyright (c) 2025 Reliant Labs
package validation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// An integration trigger's filter, inputs and prompt are type-checked against
// the payload its manifest declares for the trigger's events
// (manifest.TriggerPayloadSchema). The GitHub normalizer turns
// issue.labels into a list of label NAMES, so `l.name` on one of them reads a
// field of a string: an expression that fails on every labelled issue.

// githubTrigger is one GitHub trigger on events with body (filter:, inputs:
// and prompt: lines, indented four spaces).
func githubTrigger(events, body string) string {
	return "  - name: gh\n    integration:\n      integration: github\n      events: [" + events + "]\n" + body
}

func TestTriggerFilterReadingAFieldOfALabelNameIsAnError(t *testing.T) {
	wf := triggerWorkflow(t, githubTrigger("issues.opened",
		"    filter: \"!trigger.payload.data.issue.labels.exists(l, l.name == 'wontfix')\"\n"))
	result := StaticAnalysisWithOptions(wf, nil)
	requireTriggerFinding(t, result, SeverityError,
		"gh", "filter", "l.name", "trigger.payload.data.issue.labels", "list(string)", "field 'name'")
}

func TestTriggerPayloadTypeErrors(t *testing.T) {
	cases := map[string]struct {
		events, body string
		want         []string
	}{
		"a label added is a name, not an object": {
			"issues.labeled",
			"    filter: \"trigger.payload.data.label.name == 'bug'\"\n",
			[]string{"filter", "trigger.payload.data.label", "string", "field 'name'"},
		},
		"assignees are logins": {
			"issues.opened",
			"    filter: \"trigger.payload.data.issue.assignees.exists(a, a.login == 'octocat')\"\n",
			[]string{"filter", "a.login", "trigger.payload.data.issue.assignees", "list(string)"},
		},
		"requested reviewers are logins": {
			"pull_request.review_requested",
			"    filter: \"trigger.payload.data.pull_request.requested_reviewers.exists(r, r.login == 'octocat')\"\n",
			[]string{"filter", "requested_reviewers", "list(string)", "field 'login'"},
		},
		"an indexed label in an inputs mapping": {
			"issues.opened",
			"    inputs:\n      title: \"{{ trigger.payload.data.issue.labels[0].name }}\"\n",
			[]string{"inputs.title", "trigger.payload.data.issue.labels", "list(string)", "field 'name'"},
		},
		"a field of a string in the prompt": {
			"issues.opened",
			"    prompt: \"Triage {{ trigger.payload.data.issue.title.text }}\"\n",
			[]string{"prompt", "trigger.payload.data.issue.title", "string", "field 'text'"},
		},
		"a misspelled root field": {
			"issues.opened",
			"    filter: \"trigger.paylod.data.issue.number > 1\"\n",
			[]string{"filter", "trigger has no field 'paylod'", "payload"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wf := triggerWorkflow(t, githubTrigger(tc.events, tc.body))
			requireTriggerFinding(t, StaticAnalysisWithOptions(wf, nil), SeverityError, tc.want...)
		})
	}
}

// What the declared shape allows must stay allowed: these all run against a
// real payload, so a type error on any of them would be a false positive.
func TestTriggerPayloadTypesAcceptWhatRuns(t *testing.T) {
	cases := map[string]struct{ events, body string }{
		"label name membership": {"issues.opened", "    filter: \"!('wontfix' in trigger.payload.data.issue.labels)\"\n"},
		"compare the element":   {"issues.opened", "    filter: \"trigger.payload.data.issue.labels.exists(l, l.startsWith('p'))\"\n"},
		"a nested login":        {"issues.opened", "    filter: \"trigger.payload.data.issue.user.login != 'dependabot[bot]'\"\n"},
		"the label added":       {"issues.labeled", "    filter: \"trigger.payload.data.label in ['bug', 'p0']\"\n"},
		"has on an optional":    {"issues.closed", "    filter: \"has(trigger.payload.data.issue.closed_at)\"\n"},
		"a null comparison":     {"issues.closed", "    filter: \"trigger.payload.data.issue.state_reason != null\"\n"},
		"a null object":         {"issues.assigned", "    filter: \"trigger.payload.data.assignee != null && trigger.payload.data.assignee.login == 'octocat'\"\n"},
		"numbers are int or double at runtime": {"issues.opened",
			"    filter: \"trigger.payload.data.issue.number > 10 && trigger.payload.data.issue.number != 42.0\"\n"},
		"an attribute by key":             {"issues.opened", "    filter: \"trigger.payload.attributes['repository'] == 'acme/app' && trigger.payload.attributes.sender != ''\"\n"},
		"the sender":                      {"issues.opened", "    filter: \"trigger.sender.verified && trigger.sender.id in ['583231']\"\n"},
		"a field not declared":            {"issues.opened", "    filter: \"has(trigger.payload.data.issue.milestone)\"\n"},
		"size of a list":                  {"issues.opened", "    filter: \"size(trigger.payload.data.issue.labels) > 0\"\n"},
		"optional select":                 {"issues.*", "    filter: \"trigger.payload.data.?label.orValue('') == 'bug'\"\n"},
		"a field of one event among many": {"'*'", "    filter: \"has(trigger.payload.data.label) && trigger.payload.data.label == 'bug'\"\n"},
		"a commit's files":                {"push", "    filter: \"trigger.payload.data.commits.exists(c, c.modified.exists(f, f.startsWith('docs/')))\"\n"},
		"inputs and prompt": {"issues.opened",
			"    inputs:\n      issue_number: \"{{ trigger.payload.data.issue.number }}\"\n      title: \"#{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}\"\n" +
				"    prompt: \"Triage {{ trigger.payload.data.issue.html_url }} ({{ trigger.payload.data.issue.labels }})\"\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wf := triggerWorkflow(t, githubTrigger(tc.events, tc.body))
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
}

// An integration the catalog does not know has no declared payload, so its
// payload stays dynamic: only a warning about the integration itself.
func TestTriggerPayloadOfAnUnknownIntegrationIsNotTyped(t *testing.T) {
	wf := triggerWorkflow(t, `
  - name: elsewhere
    integration: {integration: not-in-catalog, events: [thing.happened]}
    filter: "trigger.payload.data.labels.exists(l, l.name == 'x')"
`)
	assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
}

// The trigger codec's goldens are the canonical example of each source; one a
// user copies must not be rejected by validation.
func TestTriggerGoldensValidate(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("..", "yaml", "testdata", "triggers", "*.golden.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, goldens)
	for _, path := range goldens {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			wf, err := wfyaml.ParseWorkflow(data)
			require.NoError(t, err)
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
		})
	}
}
