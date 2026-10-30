package celeval

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluate_FilterAgainstAGeneratedGitHubPayload(t *testing.T) {
	// No payload supplied: it is generated from what github declares for issues.opened.
	res := Evaluate(Request{
		Expression:  "trigger.payload.data.issue.number > 0 && trigger.payload.event == 'issues.opened'",
		Integration: "github", Event: "issues.opened",
		Scope: ScopeTrigger,
	})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, true, res.Value)
	assert.Equal(t, "bool", res.Type)
	assert.Equal(t, "expression", res.Kind)
	assert.Contains(t, res.Source, "generated from the payload github declares for issues.opened")
	assert.Equal(t, "integration", res.Trigger["kind"])
}

func TestEvaluate_SuppliedPayloadIsMergedOverTheSample(t *testing.T) {
	res := Evaluate(Request{
		Expression:  "'wontfix' in trigger.payload.data.issue.labels",
		Integration: "github", Event: "issues.opened",
		TriggerPayload: map[string]any{"data": map[string]any{"issue": map[string]any{"labels": []any{"bug", "wontfix"}}}},
	})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, true, res.Value)
	assert.Contains(t, res.Source, "merged over it")
	// The rest of the sample is still there.
	issue := res.Trigger["payload"].(map[string]any)["data"].(map[string]any)["issue"].(map[string]any)
	assert.Contains(t, issue, "number")
	assert.Contains(t, issue, "title")
}

func TestEvaluate_TemplateKeepsItsTypeAndReportsIt(t *testing.T) {
	res := Evaluate(Request{
		Expression:  "{{ trigger.payload.data.issue.number }}",
		Integration: "github", Event: "issues.opened",
		TriggerPayload: map[string]any{"data": map[string]any{"issue": map[string]any{"number": float64(42)}}},
		Scope:          ScopeTrigger,
	})
	require.Nil(t, res.Error)
	assert.EqualValues(t, 42, res.Value, "a pure {{ }} yields the number, not a string")
	assert.Equal(t, "int", res.Type)
	assert.Equal(t, "template", res.Kind)

	res = Evaluate(Request{
		Expression:     "Triage #{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}",
		TriggerPayload: map[string]any{"data": map[string]any{"issue": map[string]any{"number": 7, "title": "boom"}}},
		Scope:          ScopeTrigger,
	})
	require.Nil(t, res.Error)
	assert.Equal(t, "Triage #7: boom", res.Value)
	assert.Equal(t, "string", res.Type)
}

func TestEvaluate_NodeOutputsAndInputs(t *testing.T) {
	res := Evaluate(Request{
		Expression: "{{ json.decode(nodes.start.stdout).?status.orValue('error') }} / {{ inputs.n + 1 }}",
		Inputs:     map[string]any{"n": float64(1)},
		Nodes:      map[string]any{"start": map[string]any{"stdout": `{"status":"ready"}`}},
	})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, "ready / 2", res.Value)
}

func TestEvaluate_SenderIsPartOfTheTrigger(t *testing.T) {
	res := Evaluate(Request{
		Expression:    "trigger.sender.verified && trigger.sender.id in ['583231']",
		TriggerSender: map[string]any{"kind": "github", "id": "583231", "verified": true},
		Scope:         ScopeTrigger,
	})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, true, res.Value)
}

func TestEvaluate_ErrorsArePreciseAndPhased(t *testing.T) {
	t.Run("syntax error has a position", func(t *testing.T) {
		res := Evaluate(Request{Expression: "inputs.x ? 1", Inputs: map[string]any{"x": true}})
		require.NotNil(t, res.Error)
		assert.Equal(t, "syntax", res.Error.Phase)
		assert.Equal(t, 1, res.Error.Line)
		assert.Greater(t, res.Error.Column, 0)
		assert.Contains(t, res.Error.Message, "line 1, column")
	})
	t.Run("compile error names the problem", func(t *testing.T) {
		res := Evaluate(Request{Expression: "inputs.x + nope", Inputs: map[string]any{"x": 1}})
		require.NotNil(t, res.Error)
		assert.Equal(t, "compile", res.Error.Phase)
		assert.Contains(t, res.Error.Message, "nope")
		assert.NotEmpty(t, res.Error.Hint)
	})
	t.Run("a missing key is an eval error with a hint", func(t *testing.T) {
		res := Evaluate(Request{Expression: "trigger.payload.data.issue.number", TriggerPayload: map[string]any{"x": 1}, Scope: ScopeTrigger})
		require.NotNil(t, res.Error)
		assert.Equal(t, "eval", res.Error.Phase)
		assert.Contains(t, res.Error.Message, "no such key")
		assert.Contains(t, res.Error.Hint, "has(")
	})
	t.Run("an unclosed template is a syntax error with a position", func(t *testing.T) {
		res := Evaluate(Request{Expression: "x {{ inputs.n", Inputs: map[string]any{"n": 1}})
		require.NotNil(t, res.Error)
		assert.Equal(t, "syntax", res.Error.Phase)
		assert.Equal(t, 1, res.Error.Line)
		assert.Equal(t, 3, res.Error.Column)
	})
	t.Run("the cost limit is reported, not hung", func(t *testing.T) {
		res := Evaluate(Request{Expression: "lists.range(100000).map(a, lists.range(100000).size()).size()"})
		require.NotNil(t, res.Error)
		assert.Equal(t, "eval", res.Error.Phase)
		assert.Contains(t, res.Error.Message, "cost limit exceeded")
	})
}

func TestEvaluate_RequestErrors(t *testing.T) {
	res := Evaluate(Request{Expression: "1", Integration: "nope", Event: "x"})
	require.NotNil(t, res.Error)
	assert.Equal(t, "request", res.Error.Phase)
	assert.Contains(t, res.Error.Message, `no integration "nope"`)

	res = Evaluate(Request{Expression: "1", Integration: "github", Event: "issues.opend"})
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "issues.opened", "a misspelt event lists the real ones")

	res = Evaluate(Request{Expression: "1", Integration: "github"})
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "name both")

	res = Evaluate(Request{Expression: "trigger.kind", Scope: ScopeTrigger, Inputs: map[string]any{"a": 1}, TriggerPayload: map[string]any{"a": 1}})
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "sees only `trigger`")

	res = Evaluate(Request{Expression: "{{ 1 }}", Kind: KindExpression})
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "raw CEL but contains {{ }}")

	res = Evaluate(Request{})
	require.NotNil(t, res.Error)
}

func TestEvaluate_BraceInAStringLiteralIsAnExpression(t *testing.T) {
	res := Evaluate(Request{Expression: "inputs.s.startsWith('{{')", Inputs: map[string]any{"s": "{{x"}})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, "expression", res.Kind, "a {{ inside a string literal does not make it a template")
	assert.Equal(t, true, res.Value)
}

func TestEvaluate_EmptyExpressionShowsTheTrigger(t *testing.T) {
	res := Evaluate(Request{Integration: "github", Event: "issues.labeled"})
	require.Nil(t, res.Error)
	require.NotNil(t, res.Trigger)
	payload := res.Trigger["payload"].(map[string]any)
	assert.Equal(t, "issues.labeled", payload["event"])
	assert.Contains(t, payload, "data")
	assert.Contains(t, payload, "attributes")
}

// A filter that evaluates on the sample can still be wrong on a real event:
// the declared payload says issue.labels is a list of names, not objects.
func TestEvaluate_ReportsWhatTheDeclaredPayloadRulesOut(t *testing.T) {
	res := Evaluate(Request{
		Expression:  "trigger.payload.data.issue.labels.exists(l, l.name == 'bug')",
		Integration: "github", Event: "issues.opened",
		Scope: ScopeTrigger,
	})
	require.NotEmpty(t, res.Findings, "value=%v err=%v", res.Value, res.Error)
	joined := ""
	for _, f := range res.Findings {
		joined += f + "\n"
	}
	assert.Contains(t, joined, "name")
}

func TestSampleFromSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n":      map[string]any{"type": "integer"},
			"s":      map[string]any{"type": "string"},
			"ok":     map[string]any{"type": "boolean"},
			"state":  map[string]any{"type": "string", "enum": []any{"open", "closed"}},
			"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"user":   map[string]any{"type": "object", "properties": map[string]any{"login": map[string]any{"type": "string"}}},
			"fixed":  map[string]any{"const": "k"},
			"ex":     map[string]any{"type": "string", "examples": []any{"acme/app"}},
		},
	}
	got := SampleFromSchema(schema).(map[string]any)
	assert.EqualValues(t, 1, got["n"])
	assert.Equal(t, "s", got["s"])
	assert.Equal(t, false, got["ok"])
	assert.Equal(t, "open", got["state"])
	assert.Equal(t, []any{"label"}, got["labels"])
	assert.Equal(t, map[string]any{"login": "login"}, got["user"])
	assert.Equal(t, "k", got["fixed"])
	assert.Equal(t, "acme/app", got["ex"])

	recursive := map[string]any{"type": "object", "properties": map[string]any{}}
	recursive["properties"].(map[string]any)["self"] = recursive
	assert.NotPanics(t, func() { SampleFromSchema(recursive) }, "a recursive schema is cut off")
}

func TestDryRunTrigger_RunsFilterInputsAndPromptAgainstAPartialSample(t *testing.T) {
	wf := mustParse(t, `
name: dry
inputs:
  issue_number: {type: integer, default: 0}
  title: {type: string, default: ""}
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "!('wontfix' in trigger.payload.data.issue.labels) && trigger.payload.data.issue.number > 100"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
      title: "{{ trigger.payload.data.issue.title.trim() }}"
    prompt: "Triage #{{ trigger.payload.data.issue.number }}: {{ trigger.payload.data.issue.title }}"
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`)
	run := DryRunTrigger(wf.GetTriggers()[0], wf.GetInputs(), map[string]any{
		"data": map[string]any{"issue": map[string]any{"number": 101, "title": "  boom  ", "labels": []any{"bug"}}},
	}, nil, nil)
	assert.Empty(t, run.Errors(), "%+v", run)
	require.NotNil(t, run.Matched)
	assert.True(t, *run.Matched)
	assert.EqualValues(t, 101, run.Inputs["issue_number"])
	assert.Equal(t, "boom", run.Inputs["title"])
	assert.Equal(t, "Triage #101:   boom  ", run.Prompt)
	assert.Contains(t, run.Source, "generated from the payload github declares for issues.opened")

	run = DryRunTrigger(wf.GetTriggers()[0], wf.GetInputs(), map[string]any{
		"data": map[string]any{"issue": map[string]any{"number": 101, "labels": []any{"wontfix"}}},
	}, nil, nil)
	require.NotNil(t, run.Matched)
	assert.False(t, *run.Matched, "a filter that does not match is a result, not an error")
	assert.Empty(t, run.Errors())
}

func TestDryRunTrigger_ReportsWhatFailsOnTheSample(t *testing.T) {
	wf := mustParse(t, `
name: dry2
inputs:
  v: {type: string, default: ""}
triggers:
  - name: hook
    webhook: {}
    filter: "trigger.payload.body.alerts.size() > 0"
    inputs:
      v: "{{ trigger.payload.body.groupKey }}"
    prompt: "Alert {{ trigger.payload.body.nope.deeper }}"
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`)
	run := DryRunTrigger(wf.GetTriggers()[0], wf.GetInputs(), map[string]any{"body": map[string]any{"status": "firing"}}, nil, nil)
	errs := run.Errors()
	require.NotEmpty(t, errs)
	joined := strings.Join(errs, "\n")
	assert.Contains(t, joined, `trigger "hook": filter:`)
	assert.Contains(t, joined, "no such key")
	assert.Nil(t, run.Matched)
	assert.Equal(t, "webhook", run.Kind)
}

// Bug 5b73a559: `reliant workflow validate --trigger-payload` evaluated
// release-forge's `asap: 'true'` happily, and every real firing then failed
// with "input 'asap' expects boolean, got string". The dry run checks the
// values the mapping yields against the workflow's inputs, as the launch does.
func TestDryRunTrigger_ChecksTheInputValuesTheLaunchWouldRefuse(t *testing.T) {
	const workflow = `
name: release-forge
inputs:
  asap: {type: boolean, default: true}
  pr: {type: integer, default: 0}
triggers:
  - name: asap-after-merge
    webhook: {}
    inputs:
      pr: "{{ trigger.payload.body.number }}"
      asap: %s
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`
	payload := map[string]any{"body": map[string]any{"number": 1414}}

	quoted := mustParse(t, strings.Replace(workflow, "%s", "'true'", 1))
	run := DryRunTrigger(quoted.GetTriggers()[0], quoted.GetInputs(), payload, nil, nil)
	joined := strings.Join(run.Errors(), "\n")
	assert.Contains(t, joined, `trigger "asap-after-merge": inputs: input 'asap' expects boolean, got string "true"`)

	bare := mustParse(t, strings.Replace(workflow, "%s", "true", 1))
	run = DryRunTrigger(bare.GetTriggers()[0], bare.GetInputs(), payload, nil, nil)
	assert.Empty(t, run.Errors(), "%+v", run)
	assert.Equal(t, true, run.Inputs["asap"])
}

func mustParse(t *testing.T, src string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(src))
	require.NoError(t, err)
	return wf
}

func TestDryRunTrigger_RendersTheDedupeKey(t *testing.T) {
	wf := mustParse(t, `
name: dd
triggers:
  - name: am
    webhook: {}
    dedupe_key: "{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}"
    dedupe_window: 2h
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`)
	run := DryRunTrigger(wf.GetTriggers()[0], wf.GetInputs(), map[string]any{"body": map[string]any{"groupKey": "g1", "status": "firing"}}, nil, nil)
	assert.Empty(t, run.Errors())
	assert.Equal(t, "g1:firing", run.DedupeKey)

	run = DryRunTrigger(wf.GetTriggers()[0], wf.GetInputs(), map[string]any{"body": map[string]any{"status": "firing"}}, nil, nil)
	require.NotEmpty(t, run.Errors())
	assert.Contains(t, run.Errors()[0], "dedupe_key")
}

// A declared trigger that reads trigger.activation.params is dry-run with the params a
// sample activation sets; the rest read as the workflow's defaults.
func TestDryRunTrigger_ReadsActivationParams(t *testing.T) {
	wf := mustParse(t, `
name: dry3
inputs:
  repos: {type: array, default: []}
  team: {type: string, default: platform}
  number: {type: integer, default: 0}
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.attributes.repository in trigger.activation.params.repos"
    inputs:
      number: "{{ trigger.payload.data.issue.number }}"
    prompt: "{{ trigger.activation.params.team }} triages #{{ trigger.payload.data.issue.number }}"
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`)
	wt := wf.GetTriggers()[0]
	sample := map[string]any{"attributes": map[string]any{"repository": "acme/web"}, "data": map[string]any{"issue": map[string]any{"number": 7}}}

	in := DryRunTrigger(wt, wf.GetInputs(), sample, nil, &ActivationSample{Params: map[string]any{"repos": []any{"acme/web", "acme/docs"}, "team": "frontend"}})
	assert.Empty(t, in.Errors(), "%+v", in)
	require.NotNil(t, in.Matched)
	assert.True(t, *in.Matched, "acme/web is in this activation's repos")
	assert.Equal(t, "frontend triages #7", in.Prompt)

	out := DryRunTrigger(wt, wf.GetInputs(), sample, nil, &ActivationSample{Params: map[string]any{"repos": []any{"acme/api"}}})
	require.NotNil(t, out.Matched)
	assert.False(t, *out.Matched, "acme/web is not in the other activation's repos")
	assert.Equal(t, "platform triages #7", out.Prompt, "a param nobody set reads the workflow's default")

	unset := DryRunTrigger(wt, wf.GetInputs(), sample, nil, nil)
	require.NotNil(t, unset.Matched)
	assert.False(t, *unset.Matched, "an unset list is empty, so nothing matches")
	assert.Empty(t, unset.Errors())
}

func TestEvaluate_ScopeTriggerReadsActivationParams(t *testing.T) {
	res := Evaluate(Request{
		Expression: "trigger.payload.attributes.repository in trigger.activation.params.repos",
		Kind:       KindExpression, Scope: ScopeTrigger,
		TriggerPayload:   map[string]any{"attributes": map[string]any{"repository": "acme/web"}},
		ActivationParams: map[string]any{"repos": []any{"acme/web"}},
	})
	require.Nil(t, res.Error, "%+v", res.Error)
	assert.Equal(t, true, res.Value)

	// With no workflow named there are no input defaults, so a param nobody
	// supplied is the "no such key" a real run would hit without one.
	res = Evaluate(Request{
		Expression: "trigger.activation.params.repos.size()", Kind: KindExpression, Scope: ScopeTrigger,
		TriggerPayload: map[string]any{"x": 1},
	})
	require.NotNil(t, res.Error)
	assert.Contains(t, res.Error.Message, "no such key: repos")

	res = Evaluate(Request{
		Expression: "size(trigger.activation.params)", Kind: KindExpression, Scope: ScopeTrigger,
		TriggerPayload: map[string]any{"x": 1},
	})
	require.Nil(t, res.Error, "trigger.activation.params itself always exists: %+v", res.Error)
	assert.EqualValues(t, 0, res.Value)
}

// A node cannot read `activation`: it is not part of a run's scope.
func TestEvaluate_RunScopeHasNoActivation(t *testing.T) {
	res := Evaluate(Request{Expression: "trigger.activation.params.repos", Kind: KindExpression, TriggerPayload: map[string]any{"x": 1}})
	require.NotNil(t, res.Error)
}

func TestDryRunTrigger_ReadsTheActivationOwner(t *testing.T) {
	wf := mustParse(t, `
name: dry4
inputs:
  trusted_ids: {type: array, default: []}
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.sender.verified && (trigger.sender.id == trigger.activation.owner.github_id || trigger.sender.id in trigger.activation.params.trusted_ids)"
entry: [a]
nodes:
  - id: a
    type: save_message
    args: {role: assistant, content: x}
`)
	wt := wf.GetTriggers()[0]
	sender := map[string]any{"kind": "github", "id": "583231", "verified": true}

	me := DryRunTrigger(wt, wf.GetInputs(), map[string]any{}, sender, &ActivationSample{OwnerGitHubID: "583231"})
	assert.Empty(t, me.Errors(), "%+v", me)
	require.NotNil(t, me.Matched)
	assert.True(t, *me.Matched)

	other := DryRunTrigger(wt, wf.GetInputs(), map[string]any{}, sender, &ActivationSample{OwnerGitHubID: "1"})
	require.NotNil(t, other.Matched)
	assert.False(t, *other.Matched)

	trusted := DryRunTrigger(wt, wf.GetInputs(), map[string]any{}, sender, &ActivationSample{Params: map[string]any{"trusted_ids": []any{"583231"}}})
	require.NotNil(t, trusted.Matched)
	assert.True(t, *trusted.Matched, "a trusted id by string")
}
