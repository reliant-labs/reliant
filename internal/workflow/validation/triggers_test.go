// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// triggerWorkflow parses a workflow whose graph is valid, so every finding
// in these tests comes from its triggers block.
func triggerWorkflow(t *testing.T, triggers string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(`name: triage
inputs:
  issue_number:
    type: integer
    default: 0
  title:
    type: string
    default: ""
entry: [a]
nodes:
  - id: a
    type: approval
    args:
      title: Proceed?
triggers:
` + triggers))
	require.NoError(t, err)
	return wf
}

func triggerFindings(result *Result, severity Severity) []string {
	var out []string
	for _, e := range result.All() {
		if e.Category == CategoryTrigger && e.Severity == severity {
			out = append(out, e.Error())
		}
	}
	return out
}

func requireTriggerFinding(t *testing.T, result *Result, severity Severity, want ...string) {
	t.Helper()
	findings := triggerFindings(result, severity)
	for _, f := range findings {
		matched := true
		for _, w := range want {
			if !strings.Contains(f, w) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("no %s finding containing %q; got %q (all: %v)", severity, want, findings, result.All())
}

func TestValidTriggersHaveNoFindings(t *testing.T) {
	wf := triggerWorkflow(t, `
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.data.issue.number > 0"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
      title: "#{{ trigger.payload.data.issue.number }}"
  - name: nightly
    schedule: {cron: "0 9 * * 1-5", timezone: America/New_York}
  - name: hook
    webhook: {}
  - name: on-failure
    workflow_event: {outcomes: [failed]}
`)
	result := StaticAnalysisWithOptions(wf, nil)
	assert.Empty(t, triggerFindings(result, SeverityError))
	assert.Empty(t, triggerFindings(result, SeverityWarning))
	assert.False(t, result.HasErrors(), "%v", result.All())
}

func TestTriggerValidationRules(t *testing.T) {
	cases := []struct {
		name     string
		triggers string
		severity Severity
		want     []string
	}{
		{
			name:     "name required",
			triggers: "  - webhook: {}\n",
			want:     []string{"name", "required"},
		},
		{
			name:     "name is a slug",
			triggers: "  - name: New Issue\n    webhook: {}\n",
			want:     []string{"New Issue", "slug"},
		},
		{
			name:     "name may not end in a separator",
			triggers: "  - name: issue-\n    webhook: {}\n",
			want:     []string{"issue-", "slug"},
		},
		{
			name:     "names unique",
			triggers: "  - name: hook\n    webhook: {}\n  - name: hook\n    webhook: {}\n",
			want:     []string{"hook", "more than once"},
		},
		{
			name:     "exactly one source",
			triggers: "  - name: nothing\n",
			want:     []string{"nothing", "source is required"},
		},
		{
			name:     "cron valid",
			triggers: "  - name: bad-cron\n    schedule: {cron: \"0 9 * *\"}\n",
			want:     []string{"bad-cron", "cron", "want 5 fields"},
		},
		{
			name:     "timezone valid",
			triggers: "  - name: bad-tz\n    schedule: {interval: 1h, timezone: Mars/Olympus}\n",
			want:     []string{"bad-tz", "timezone"},
		},
		{
			name:     "no filter on a schedule",
			triggers: "  - name: filtered\n    schedule: {cron: \"0 9 * * *\"}\n    filter: \"true\"\n",
			want:     []string{"filtered", "filter", "schedule"},
		},
		{
			name:     "an integration outside the catalog is a warning",
			triggers: "  - name: nope\n    integration: {integration: no_such_thing, events: [x.y]}\n",
			severity: SeverityWarning,
			want:     []string{"nope", "no_such_thing", "catalog"},
		},
		{
			name:     "integration needs events",
			triggers: "  - name: noevents\n    integration: {integration: github}\n",
			want:     []string{"noevents", "events"},
		},
		{
			name:     "filter compiles",
			triggers: "  - name: badfilter\n    webhook: {}\n    filter: \"trigger.payload.x ==\"\n",
			want:     []string{"badfilter", "filter"},
		},
		{
			name:     "filter is not a template",
			triggers: "  - name: braces\n    webhook: {}\n    filter: \"{{ trigger.payload.ok }}\"\n",
			want:     []string{"braces", "without {{ }}"},
		},
		{
			name:     "filter sees only trigger",
			triggers: "  - name: scope\n    webhook: {}\n    filter: \"inputs.issue_number > 1\"\n",
			want:     []string{"scope", "filter"},
		},
		{
			name:     "inputs keys are declared workflow inputs",
			triggers: "  - name: stray\n    webhook: {}\n    inputs: {issue: \"{{ trigger.payload.n }}\"}\n",
			want:     []string{"stray", "inputs.issue", "not a declared input", "issue_number"},
		},
		{
			name:     "inputs compile",
			triggers: "  - name: badinput\n    webhook: {}\n    inputs: {issue_number: \"{{ trigger.payload.n + }}\"}\n",
			want:     []string{"badinput", "inputs.issue_number"},
		},
		{
			name:     "inputs see only trigger",
			triggers: "  - name: inscope\n    webhook: {}\n    inputs: {issue_number: \"{{ nodes.a.response }}\"}\n",
			want:     []string{"inscope", "inputs.issue_number"},
		},
		{
			name:     "a bare expression in inputs is a warning",
			triggers: "  - name: bare\n    webhook: {}\n    inputs: {title: trigger.payload.data.title}\n",
			severity: SeverityWarning,
			want:     []string{"bare", "inputs.title", "{{"},
		},
		{
			name:     "workflow_event outcome",
			triggers: "  - name: badoutcome\n    workflow_event: {outcomes: [exploded]}\n",
			want:     []string{"badoutcome", "outcomes"},
		},
		{
			name:     "prompt reads only trigger",
			triggers: "  - name: hook\n    webhook: {}\n    prompt: \"Look at {{ inputs.title }}\"\n",
			want:     []string{"hook", "prompt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := triggerWorkflow(t, tc.triggers)
			result := StaticAnalysisWithOptions(wf, nil)
			requireTriggerFinding(t, result, tc.severity, tc.want...)
			if tc.severity == SeverityError {
				assert.True(t, result.HasErrors())
			} else {
				assert.False(t, result.HasErrors(), "a warning must not block: %v", result.All())
			}
		})
	}
}

// Trigger findings carry the trigger's name in their path, so an agent
// editing a workflow with five triggers knows which one to fix.
func TestTriggerFindingPathNamesTheTrigger(t *testing.T) {
	wf := triggerWorkflow(t, "  - name: ok\n    webhook: {}\n  - name: broken\n    schedule: {cron: nope}\n")
	result := StaticAnalysisWithOptions(wf, nil)
	errs := triggerFindings(result, SeverityError)
	require.Len(t, errs, 1)
	assert.True(t, strings.HasPrefix(errs[0], "triage.triggers[1](broken).schedule"), errs[0])
}

// Trigger findings do not wait on the graph: a workflow with a broken node
// still reports its broken trigger, so one save shows the agent everything.
func TestTriggerValidationRunsAlongsideStructuralErrors(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`name: w
entry: [missing]
nodes:
  - id: a
    type: call_llm
triggers:
  - name: bad
    schedule: {cron: nope}
`))
	require.NoError(t, err)
	result := StaticAnalysisWithOptions(wf, nil)
	requireTriggerFinding(t, result, SeverityError, "bad", "cron")
}

// A workflow_event trigger names other workflows. One that does not resolve
// may simply not exist YET (the user is building both), so it is a warning,
// not an error — but only when a loader could have found it.
func TestWorkflowEventRefsResolve(t *testing.T) {
	wf := triggerWorkflow(t, "  - name: after\n    workflow_event: {workflows: [deploy, builtin://agent, not-yet-written]}\n")
	known := map[string]bool{"deploy": true, "builtin://agent": true}
	loader := func(ref string) (*reliantv1.Workflow, error) {
		if known[ref] {
			return &reliantv1.Workflow{Name: ref}, nil
		}
		return nil, nil
	}
	result := StaticAnalysisWithOptions(wf, &ValidationOptions{WorkflowLoader: loader})
	assert.Empty(t, triggerFindings(result, SeverityError))
	warnings := triggerFindings(result, SeverityWarning)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "not-yet-written")

	// Without a loader there is nothing to check against, and nothing is
	// reported.
	result = StaticAnalysisWithOptions(wf, nil)
	assert.Empty(t, triggerFindings(result, SeverityWarning))
}

// A required plain input the trigger does not map can only come from each
// activation's params; the author is told, without blocking. Inputs presets
// or the launch supply (a model, the message) are not flagged.
func TestUnmappedRequiredInputIsAWarning(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`name: w
inputs:
  repo: {type: string}
  issue: {type: integer}
  model: {type: model}
  note: {type: string, default: ""}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
triggers:
  - name: hook
    webhook: {}
    inputs:
      issue: "{{ trigger.payload.body.n }}"
`))
	require.NoError(t, err)
	result := StaticAnalysisWithOptions(wf, nil)
	requireTriggerFinding(t, result, SeverityWarning, "hook", "inputs", "repo")
	for _, w := range triggerFindings(result, SeverityWarning) {
		assert.NotContains(t, w, "model", "a model input is supplied by presets")
		assert.NotContains(t, w, "note", "an input with a default is not required")
		assert.NotContains(t, w, " issue", "a mapped input is not flagged")
	}
	assert.Empty(t, triggerFindings(result, SeverityError))
}

// Underscores are allowed: get_integration_schema suggests `name: issue_opened`.
func TestTriggerNamesAcceptUnderscores(t *testing.T) {
	wf := triggerWorkflow(t, "  - name: issue_opened\n    webhook: {}\n  - name: nightly-2\n    webhook: {}\n")
	assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(wf, nil), SeverityError))
}

// When an integration's manifest declares triggers, a source's events must
// be among the event types they deliver (a trailing ".*" or "*" matches),
// and its match keys among the attributes those events carry.
func TestIntegrationSourceMatchesDeclaredTriggers(t *testing.T) {
	index := fakeIntegrationIndex{
		"github": {
			events: []string{"issues.opened", "issues.closed", "pull_request.opened"},
			attrs:  []string{"repository", "sender"},
		},
		"http": {}, // in the catalog, declares no triggers
	}
	cases := []struct {
		source string
		want   []string // empty: valid
	}{
		{"{integration: github, events: [issues.opened]}", nil},
		{"{integration: github, events: [issues.*]}", nil},
		{"{integration: github, events: ['*']}", nil},
		{"{integration: github, events: [issues.opened], match: {repository: acme/app}}", nil},
		{"{integration: github, events: [issues.opened, issues.deleted]}", []string{"gh", "integration.events", "issues.deleted", "issues.opened"}},
		{"{integration: github, events: [push]}", []string{"gh", "integration.events", "push"}},
		{"{integration: github, events: [issues.opened], match: {repo: acme/app}}", []string{"gh", "integration.match.repo", "repository"}},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			wf := triggerWorkflow(t, "  - name: gh\n    integration: "+tc.source+"\n")
			result := NewResult()
			validateTriggers(wf, &ValidationOptions{}, index, result)
			if len(tc.want) == 0 {
				assert.Empty(t, triggerFindings(result, SeverityError))
			} else {
				requireTriggerFinding(t, result, SeverityError, tc.want...)
			}
		})
	}

	// An integration that declares no triggers is not narrowed.
	wf := triggerWorkflow(t, "  - name: h\n    integration: {integration: http, events: [anything.at.all], match: {x: y}}\n")
	result := NewResult()
	validateTriggers(wf, &ValidationOptions{}, index, result)
	assert.Empty(t, triggerFindings(result, SeverityError))
}

type fakeIntegration struct{ events, attrs []string }

type fakeIntegrationIndex map[string]fakeIntegration

func (f fakeIntegrationIndex) TriggerTypes(integration string) ([]string, []string, bool) {
	in, ok := f[integration]
	return in.events, in.attrs, ok
}

func (f fakeIntegrationIndex) PayloadSchemas(string, string) []map[string]any { return nil }
