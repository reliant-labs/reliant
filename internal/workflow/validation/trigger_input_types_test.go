// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Bug 5b73a559: `reliant workflow validate` accepted `asap: 'true'` for an
// input declared `type: boolean`, and every firing then failed with "input
// 'asap' expects boolean, got string". These tests pin ONE rule, applied by
// the launch, the runtime and validation alike (InputValue): text is never
// read as a boolean or a number, and a boolean or number is not text either.

const releaseInputs = `name: release
inputs:
  asap:
    type: boolean
    default: true
  every:
    type: integer
    default: 10
  ratio:
    type: number
    default: 0.5
  merge_sha:
    type: string
    default: ""
  channel:
    type: enum
    enum: [stable, beta]
    default: stable
entry: [a]
nodes:
  - id: a
    type: approval
    args:
      title: Proceed?
triggers:
  - name: gh
    integration:
      integration: github
      events: [pull_request.labeled]
    inputs:
`

func releaseTrigger(t *testing.T, inputs string) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(releaseInputs + inputs))
	require.NoError(t, err)
	return wf
}

func TestTriggerInputLiteralsAreCheckedAsTheLaunchChecksThem(t *testing.T) {
	refused := map[string]struct {
		inputs string
		want   []string
	}{
		"quoted boolean": {"      asap: 'true'\n",
			[]string{"inputs.asap", "input 'asap' expects boolean, got string \"true\"", "write true without quotes"}},
		"quoted integer": {"      every: \"10\"\n",
			[]string{"inputs.every", "input 'every' expects integer, got string \"10\"", "write 10 without quotes"}},
		"text for a number": {"      ratio: half\n",
			[]string{"inputs.ratio", "input 'ratio' expects number, got string \"half\""}},
		"enum value it does not declare": {"      channel: nightly\n",
			[]string{"inputs.channel", "invalid enum value \"nightly\""}},
		"interpolated text for an integer": {"      every: \"PR {{ trigger.payload.data.pull_request.number }}\"\n",
			[]string{"inputs.every", "input 'every' is integer", "yields a string"}},
		"a string expression for a boolean": {"      asap: \"{{ trigger.payload.data.label }}\"\n",
			[]string{"inputs.asap", "input 'asap' is boolean", "yields a string"}},
		"a bare number for text": {"      merge_sha: 42\n",
			[]string{"inputs.merge_sha", "input 'merge_sha' is string", "yields an integer", "{{ string(42) }}"}},
		"an integer expression for text": {"      merge_sha: \"{{ size(trigger.payload.data.pull_request.labels) }}\"\n",
			[]string{"inputs.merge_sha", "yields an integer", "{{ string(size(trigger.payload.data.pull_request.labels)) }}"}},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			requireTriggerFinding(t, StaticAnalysisWithOptions(releaseTrigger(t, tc.inputs), nil), SeverityError, tc.want...)
		})
	}

	accepted := map[string]string{
		"bare boolean":             "      asap: true\n",
		"bare integer":             "      every: 10\n",
		"bare float":               "      ratio: 0.5\n",
		"an integer for a number":  "      ratio: 2\n",
		"text":                     "      merge_sha: abc123\n",
		"a quoted number for text": "      merge_sha: \"42\"\n",
		"declared enum value":      "      channel: beta\n",
		"boolean expression":       "      asap: \"{{ has(trigger.payload.data.pull_request.labels) && 'release:asap' in trigger.payload.data.pull_request.labels }}\"\n",
		"integer expression":       "      every: \"{{ trigger.payload.data.pull_request.number }}\"\n",
		"converted to text":        "      merge_sha: \"{{ string(trigger.payload.data.pull_request.number) }}\"\n",
		"interpolated text":        "      merge_sha: \"pr-{{ trigger.payload.data.pull_request.number }}\"\n",
	}
	for name, inputs := range accepted {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, triggerFindings(StaticAnalysisWithOptions(releaseTrigger(t, inputs), nil), SeverityError))
		})
	}
}

// The rule itself, as every caller applies it.
func TestInputValueNeverReadsTextAsABooleanOrNumber(t *testing.T) {
	boolean := &reliantv1.Input{Type: "boolean", Config: &reliantv1.Input_BooleanInput{BooleanInput: &reliantv1.BooleanInputConfig{}}}
	integer := &reliantv1.Input{Type: "integer", Config: &reliantv1.Input_IntegerInput{IntegerInput: &reliantv1.IntegerInputConfig{}}}
	text := &reliantv1.Input{Type: "string", Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{}}}

	for _, v := range []any{"true", "yes", "1", 1, 0.0} {
		_, ok := InputValue(v, boolean)
		assert.False(t, ok, "%#v is not a boolean", v)
	}
	got, ok := InputValue(true, boolean)
	assert.True(t, ok)
	assert.Equal(t, true, got)

	for _, v := range []any{"10", 1.5, true} {
		_, ok := InputValue(v, integer)
		assert.False(t, ok, "%#v is not an integer", v)
	}
	got, ok = InputValue(float64(10), integer)
	assert.True(t, ok)
	assert.Equal(t, int64(10), got, "a whole number is an integer")

	got, ok = InputValue("main", text)
	assert.True(t, ok)
	assert.Equal(t, "main", got)
	for _, v := range []any{true, int64(42), 0.5, map[string]any{"a": 1}} {
		_, ok := InputValue(v, text)
		assert.False(t, ok, "%#v is not text", v)
	}

	// And ValidateInputs, what the runtime runs, agrees.
	wf := &reliantv1.Workflow{Name: "release", Inputs: map[string]*reliantv1.Input{"asap": boolean}}
	result := ValidateInputs(wf, map[string]any{"asap": "true"})
	require.True(t, result.HasErrors())
	assert.Contains(t, result.Error(), "input 'asap' expects boolean, got string \"true\"")
	assert.False(t, ValidateInputs(wf, map[string]any{"asap": true}).HasErrors())
}
