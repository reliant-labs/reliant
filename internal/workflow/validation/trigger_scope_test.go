// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// scopeIndex is a github whose attributes are typed the way the catalog will
// declare them: repository is a github.repository, sender a plain string, and
// `label` a github.label.
func scopeIndex() fakeIntegrationIndex {
	return fakeIntegrationIndex{
		"github": {
			events: []string{"pull_request.opened", "pull_request.closed", "issues.opened"},
			attrs:  []string{"repository", "sender", "label"},
			attrTypes: map[string]string{
				"repository": "github.repository",
				"label":      "github.label",
			},
		},
	}
}

func scopeWorkflow(t *testing.T, inputs, trigger string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte("name: scoped\ninputs:\n" + inputs +
		"entry: [a]\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\ntriggers:\n" + trigger))
	require.NoError(t, err)
	return wf
}

func scopeFindings(wf *reliantv1.Workflow) *Result {
	result := NewResult()
	validateTriggers(wf, &ValidationOptions{ValueTypes: valuetypestest.GitHub()}, scopeIndex(), result)
	return result
}

const reposRequired = "  repos: {type: github.repository, multi: true, required: true, min_items: 1}\n"

func TestTriggerScopeValidation(t *testing.T) {
	const pr = "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.opened]\n"
	cases := []struct {
		name     string
		inputs   string
		trigger  string
		severity Severity
		want     []string // every fragment in one finding; empty: no finding at all
	}{
		{
			name:    "a required, matching multi input is a valid scope",
			inputs:  reposRequired,
			trigger: pr + "      match: {repository: {input: repos}}\n",
		},
		{
			name:    "a single-valued input of the same type is a valid scope",
			inputs:  "  repo: {type: github.repository, required: true}\n",
			trigger: pr + "      match: {repository: {input: repo}}\n",
		},
		{
			name:    "a scope beside a literal match on another attribute",
			inputs:  reposRequired,
			trigger: pr + "      match: {sender: octocat, repository: {input: repos}}\n",
		},
		{
			name:    "a scope across several events that all carry the attribute",
			inputs:  reposRequired,
			trigger: "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.*, issues.opened]\n      match: {repository: {input: repos}}\n",
		},
		{
			name:     "an undeclared input",
			inputs:   reposRequired,
			trigger:  pr + "      match: {repository: {input: repo}}\n",
			severity: SeverityError,
			want:     []string{"integration.match.repository", `input "repo" is not a declared input`, "declared inputs: repos"},
		},
		{
			name:     "an input that is not typed by an integration",
			inputs:   "  repos: {type: array, default: []}\n",
			trigger:  pr + "      match: {repository: {input: repos}}\n",
			severity: SeverityError,
			want:     []string{"integration.match.repository", `input "repos" is a array`, "typed by the integration"},
		},
		{
			name:     "an input of another type than the attribute",
			inputs:   "  labels: {type: github.label, multi: true, required: true, scope: {repository: repos}}\n" + reposRequired,
			trigger:  pr + "      match: {repository: {input: labels}}\n",
			severity: SeverityError,
			want:     []string{"integration.match.repository", `input "labels" is a github.label`, `attribute "repository" of pull_request.opened is a github.repository`, "declare labels as type: github.repository"},
		},
		{
			name:     "an attribute no event of the trigger carries",
			inputs:   reposRequired,
			trigger:  pr + "      match: {nope: {input: repos}}\n",
			severity: SeverityError,
			want:     []string{"integration.match.nope", "carries no", `"nope"`},
		},
		{
			name:     "an attribute with no declared type",
			inputs:   reposRequired,
			trigger:  pr + "      match: {sender: {input: repos}}\n",
			severity: SeverityError,
			want:     []string{"integration.match.sender", `attribute "sender"`, "no value type"},
		},
		{
			name:     "an input the trigger sets from the event cannot scope that event",
			inputs:   "  repos: {type: github.repository, multi: true, required: true}\n",
			trigger:  pr + "      match: {repository: {input: repos}}\n    inputs:\n      repos: \"{{ trigger.payload.attributes.repository }}\"\n",
			severity: SeverityError,
			want:     []string{"integration.match.repository", `input "repos" is set from the event`},
		},
		{
			name:     "an optional input is a warning: an unset scope matches nothing",
			inputs:   "  repos: {type: github.repository, multi: true, default: []}\n",
			trigger:  pr + "      match: {repository: {input: repos}}\n",
			severity: SeverityWarning,
			want:     []string{"integration.match.repository", `input "repos" is optional`, "matches nothing", "required: true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := scopeFindings(scopeWorkflow(t, tc.inputs, tc.trigger))
			if len(tc.want) == 0 {
				assert.Empty(t, triggerFindings(result, SeverityError))
				assert.Empty(t, triggerFindings(result, SeverityWarning))
				return
			}
			requireTriggerFinding(t, result, tc.severity, tc.want...)
			if tc.severity == SeverityWarning {
				assert.Empty(t, triggerFindings(result, SeverityError), "an optional scope is allowed")
			}
		})
	}
}

// Every listed event must carry the attribute with the input's type: one that
// does not is named.
func TestTriggerScopeChecksEveryListedEvent(t *testing.T) {
	index := scopeIndex()
	gh := index["github"]
	gh.events = append(gh.events, "ping")
	index["github"] = gh
	wf := scopeWorkflow(t, reposRequired, "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.opened]\n      match: {repository: {input: repos}}\n")
	result := NewResult()
	validateTriggers(wf, &ValidationOptions{ValueTypes: valuetypestest.GitHub()}, index, result)
	assert.Empty(t, triggerFindings(result, SeverityError))

	// An index where one of the events does not carry the attribute.
	perEvent := perEventIndex{scopeIndex(), map[string]map[string]string{
		"pull_request.opened": {"repository": "github.repository"},
		"issues.opened":       {"sender": ""},
	}}
	wf = scopeWorkflow(t, reposRequired, "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.opened, issues.opened]\n      match: {repository: {input: repos}}\n")
	result = NewResult()
	validateTriggers(wf, &ValidationOptions{ValueTypes: valuetypestest.GitHub()}, perEvent, result)
	requireTriggerFinding(t, result, SeverityError, "integration.match.repository", "issues.opened carries no", `"repository"`)
}

type perEventIndex struct {
	fakeIntegrationIndex
	byEvent map[string]map[string]string
}

func (p perEventIndex) EventAttributes(_, eventType string) map[string]string {
	return p.byEvent[eventType]
}

// An integration the catalog does not know cannot be type-checked: the scope
// is accepted with a warning rather than blocking a deployment that receives
// that integration's events.
func TestTriggerScopeOnAnUnknownIntegrationWarns(t *testing.T) {
	wf := scopeWorkflow(t, reposRequired, "  - name: x\n    integration:\n      integration: elsewhere\n      events: [a.b]\n      match: {repository: {input: repos}}\n")
	result := scopeFindings(wf)
	assert.Empty(t, triggerFindings(result, SeverityError))
	requireTriggerFinding(t, result, SeverityWarning, "integration.match", "cannot be checked against the inputs")
}

// The legacy hand-written scope still works, and is pointed at the typed one.
func TestLegacyScopeFilterWarns(t *testing.T) {
	const inputs = "  repos: {type: array, default: []}\n"
	trigger := func(filter string) string {
		return "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.opened]\n    filter: \"" + filter + "\"\n"
	}
	cases := []struct {
		name   string
		filter string
		warn   bool
	}{
		{"attribute in a param", "trigger.payload.attributes.repository in trigger.activation.params.repos", true},
		{"indexed attribute", "trigger.payload.attributes['repository'] in trigger.activation.params.repos", true},
		{"embedded in a longer filter", "!trigger.payload.data.pull_request.draft && trigger.payload.attributes.repository in trigger.activation.params.repos", true},
		{"a different comparison", "trigger.payload.attributes.repository == 'acme/app'", false},
		{"a membership that is not on an attribute", "trigger.payload.data.pull_request.title in trigger.activation.params.repos", false},
		{"a param that is not a declared input", "trigger.payload.attributes.repository in trigger.activation.params.nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := scopeFindings(scopeWorkflow(t, inputs, trigger(tc.filter)))
			if !tc.warn {
				for _, w := range triggerFindings(result, SeverityWarning) {
					assert.NotContains(t, w, "match: {")
				}
				return
			}
			requireTriggerFinding(t, result, SeverityWarning, "filter", "trigger.payload.attributes.repository", "input repos",
				"type: github.repository", "match: {repository: {input: repos}}")
		})
	}

	// Once scoped, the same filter is not nagged.
	scoped := "  - name: pr\n    integration:\n      integration: github\n      events: [pull_request.opened]\n      match: {repository: {input: repos}}\n" +
		"    filter: \"trigger.payload.attributes.repository in trigger.activation.params.repos\"\n"
	result := scopeFindings(scopeWorkflow(t, reposRequired, scoped))
	for _, w := range triggerFindings(result, SeverityWarning) {
		assert.False(t, strings.Contains(w, "match: {repository"), "no hint once the scope is declared: %s", w)
	}
}
