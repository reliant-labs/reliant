// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/lint"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runEval(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newWorkflowEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestWorkflowEval_FilterAgainstAGeneratedPayload(t *testing.T) {
	out, err := runEval(t, "--integration", "github", "--event", "issues.opened", "--scope", "trigger",
		"!('wontfix' in trigger.payload.data.issue.labels)")
	require.NoError(t, err, out)
	assert.Contains(t, out, "true")
	assert.Contains(t, out, "type: bool")
	assert.Contains(t, out, "generated from the payload github declares for issues.opened")
}

func TestWorkflowEval_JSONOutputAndPayloadMerge(t *testing.T) {
	out, err := runEval(t, "--json", "--integration", "github", "--event", "issues.opened",
		"--trigger-payload", `{"data":{"issue":{"number":42}}}`, "{{ trigger.payload.data.issue.number }}")
	require.NoError(t, err, out)
	var res struct {
		Value float64 `json:"value"`
		Type  string  `json:"type"`
		Kind  string  `json:"kind"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	assert.EqualValues(t, 42, res.Value)
	assert.Equal(t, "int", res.Type)
	assert.Equal(t, "template", res.Kind)
}

func TestWorkflowEval_FailureExitsNonZeroAndSaysWhy(t *testing.T) {
	out, err := runEval(t, "inputs.x ? 1", "--input", "x=true")
	require.Error(t, err)
	assert.Contains(t, out, "syntax error")
	assert.Contains(t, out, "line 1, column")

	out, err = runEval(t, "--trigger-payload", `{"a":1}`, "--scope", "trigger", "trigger.payload.nope")
	require.Error(t, err)
	assert.Contains(t, out, "eval error")
	assert.Contains(t, out, "no such key")
	assert.Contains(t, out, "hint:")
}

func TestWorkflowEval_PayloadFromAFileAndExpressionFromStdin(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "event.json")
	require.NoError(t, os.WriteFile(payload, []byte(`{"body":{"status":"firing"}}`), 0o644))

	cmd := newWorkflowEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(bytes.NewBufferString("trigger.payload.body.status == 'firing'\n"))
	cmd.SetArgs([]string{"--scope", "trigger", "--trigger-payload", "@" + payload, "-"})
	require.NoError(t, cmd.Execute(), out.String())
	assert.Contains(t, out.String(), "true")
}

func TestWorkflowEval_NoExpressionShowsTheTrigger(t *testing.T) {
	out, err := runEval(t, "--json", "--integration", "github", "--event", "issues.labeled")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"trigger"`)
	assert.Contains(t, out, "issues.labeled")
}

func TestWorkflowEval_RejectsUnusableFlags(t *testing.T) {
	_, err := runEval(t, "--scope", "bogus", "1")
	require.Error(t, err)
	_, err = runEval(t, "--trigger-payload", "not json", "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a JSON object")
	_, err = runEval(t, "--input", "novalue", "1")
	require.Error(t, err)
}

// workflow validate --trigger-payload dry-runs each declared trigger.
const triggerDryRunWorkflow = `name: triage
inputs:
  issue_number: {type: integer, default: 0}
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "!('wontfix' in trigger.payload.data.issue.labels)"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage #{{ trigger.payload.data.issue.number }}"
entry: [note]
nodes:
  - id: note
    type: save_message
    args:
      role: assistant
      content: "hello"
`

func TestWorkflowValidate_TriggerPayloadDryRunsDeclaredTriggers(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{"triage.yaml": triggerDryRunWorkflow})
	file := filepath.Join(dir, "triage.yaml")

	sample, err := triggerSampleFromFlags(`{"data":{"issue":{"number":7,"labels":["bug"]}}}`, "", "", "", "")
	require.NoError(t, err)
	layout, err := readWorkflowLayout(dir)
	require.NoError(t, err)

	res := validateWorkflowFileIn(file, dir, layout)
	dryRunTriggers(&res, file, sample)
	assert.True(t, res.Valid, "%v", res.Errors)

	// A filter that does not match is a note, not an error.
	res = validateWorkflowFileIn(file, dir, layout)
	sample, _ = triggerSampleFromFlags(`{"data":{"issue":{"number":7,"labels":["wontfix"]}}}`, "", "", "", "")
	dryRunTriggers(&res, file, sample)
	assert.True(t, res.Valid, "%v", res.Errors)
	assert.Contains(t, res.Warnings[len(res.Warnings)-1], "does not match this sample event")

	// A filter that reads what the declared payload rules out is an error even
	// though it ran: here labels are names, not objects.
	bad := `name: triage-bad
triggers:
  - name: new-issue
    integration:
      integration: github
      events: [issues.opened]
    filter: "trigger.payload.data.issue.labels.exists(l, l.name == 'bug')"
entry: [note]
nodes:
  - id: note
    type: save_message
    args: {role: assistant, content: hello}
`
	dir2 := writeWorkflowsDir(t, map[string]string{"bad.yaml": bad})
	layout2, err := readWorkflowLayout(dir2)
	require.NoError(t, err)
	file2 := filepath.Join(dir2, "bad.yaml")
	res = validateWorkflowFileIn(file2, dir2, layout2)
	sample, _ = triggerSampleFromFlags(`{"data":{"issue":{"labels":["bug"]}}}`, "", "", "", "")
	dryRunTriggers(&res, file2, sample)
	assert.False(t, res.Valid, "a filter that cannot work on a real event fails the dry run")
}

func TestWorkflowValidate_TriggerSenderNeedsAPayload(t *testing.T) {
	_, err := triggerSampleFromFlags("", `{"id":"1"}`, "", "", "")
	require.Error(t, err)
	s, err := triggerSampleFromFlags("", "", "", "", "")
	require.NoError(t, err)
	assert.Nil(t, s)
}

const multiTriggerWorkflow = `name: reviewer
inputs:
  pr_number: {type: integer, default: 0}
triggers:
  - name: opened
    integration:
      integration: github
      events: [pull_request.opened]
    inputs:
      pr_number: "{{ trigger.payload.data.pull_request.number }}"
  - name: labeled
    integration:
      integration: github
      events: [pull_request.labeled]
    filter: "trigger.payload.data.label == 're-review'"
    inputs:
      pr_number: "{{ trigger.payload.data.pull_request.number }}"
  - name: commented
    integration:
      integration: github
      events: [issue_comment.*]
    inputs:
      pr_number: "{{ trigger.payload.data.issue.number }}"
entry: [note]
nodes:
  - id: note
    type: save_message
    args: {role: assistant, content: hello}
`

// One sample event belongs to the triggers that listen for it: the others are
// not run against it (each says why), so a multi-trigger workflow validates
// with a real event instead of always failing on the triggers it cannot reach.
func TestWorkflowValidate_TriggerPayloadRunsOnlyTheTriggersItCanReach(t *testing.T) {
	dir := writeWorkflowsDir(t, map[string]string{"reviewer.yaml": multiTriggerWorkflow})
	file := filepath.Join(dir, "reviewer.yaml")
	layout, err := readWorkflowLayout(dir)
	require.NoError(t, err)

	sample, err := triggerSampleFromFlags(`{"event":"pull_request.opened","data":{"pull_request":{"number":7}}}`, "", "", "", "")
	require.NoError(t, err)
	res := validateWorkflowFileIn(file, dir, layout)
	dryRunTriggers(&res, file, sample)
	assert.True(t, res.Valid, "%v", res.Errors)
	require.Len(t, res.TriggerRuns, 3)
	assert.Equal(t, "", res.TriggerRuns[0].Skipped)
	assert.Equal(t, map[string]any{"pr_number": int64(7)}, res.TriggerRuns[0].Inputs, "a match reports the inputs it maps")
	assert.Contains(t, res.TriggerRuns[1].Skipped, "pull_request.labeled, not pull_request.opened")
	assert.Contains(t, res.TriggerRuns[2].Skipped, "issue_comment.*")

	// A wildcard event pattern reaches its events.
	sample, _ = triggerSampleFromFlags(`{"event":"issue_comment.created","data":{"issue":{"number":3}}}`, "", "", "", "")
	res = validateWorkflowFileIn(file, dir, layout)
	dryRunTriggers(&res, file, sample)
	assert.True(t, res.Valid, "%v", res.Errors)
	assert.Equal(t, "", res.TriggerRuns[2].Skipped)

	// --trigger picks one; a name the workflow does not declare is an error.
	sample, _ = triggerSampleFromFlags(`{"data":{"label":"re-review","pull_request":{"number":7}}}`, "", "", "", "labeled")
	res = validateWorkflowFileIn(file, dir, layout)
	dryRunTriggers(&res, file, sample)
	assert.True(t, res.Valid, "%v", res.Errors)
	assert.Equal(t, "", res.TriggerRuns[1].Skipped)
	assert.Contains(t, res.TriggerRuns[0].Skipped, "--trigger names labeled")

	sample, _ = triggerSampleFromFlags(`{}`, "", "", "", "nope")
	res = validateWorkflowFileIn(file, dir, layout)
	dryRunTriggers(&res, file, sample)
	assert.False(t, res.Valid)
	assert.Contains(t, strings.Join(res.Errors, "\n"), `--trigger "nope": the workflow declares no such trigger`)

	_, err = triggerSampleFromFlags("", "", "", "", "opened")
	require.Error(t, err, "--trigger needs --trigger-payload")
}

// The summary's warning count includes the lint warnings the results list.
func TestWorkflowValidate_WarningCountIncludesLintWarnings(t *testing.T) {
	r := validateResult{Valid: true, Warnings: []string{"w"}, Lint: []lint.Finding{
		{Rule: lint.RuleScenarioNone, Severity: lint.SeverityWarning},
		{Rule: lint.RuleEdgeNoDefault, Severity: lint.SeverityWarning},
		{Rule: lint.RuleSecretLiteral, Severity: lint.SeverityError},
	}}
	assert.Equal(t, 2, lintWarnings(r.Lint))
	out := captureStdout(t, func() { printValidateSummary([]validateResult{r}) })
	assert.Contains(t, out, "Warnings: 3")
}
