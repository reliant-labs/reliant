// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/require"
)

// A template that does not scan must fail the node. It used to be sent on as
// literal text: the model or shell received the source of the template.
const malformedTemplateYAML = `
name: malformed-template
entry: [a]
nodes:
  - id: a
    type: call_llm
    args:
      system_prompt: "hello {{ inputs.topic"
`

func TestMalformedTemplate_FailsTheNodeInsteadOfPassingLiteralText(t *testing.T) {
	res := runYAMLScenario(t, malformedTemplateYAML, &scenario.Scenario{
		Name:   "malformed",
		Events: []scenario.SimulatedEvent{{Node: "a", Output: map[string]interface{}{"response_text": "x"}}},
	})
	require.Equal(t, "error", res.Execution.Outcome)
	require.NotNil(t, res.Execution.Error)
	require.Contains(t, res.Execution.Error.Message, `no closing "}}"`)
}

const bracesInLiteralsYAML = `
name: braces-in-literals
inputs:
  payload: {type: string, default: "{\"a\":1}"}
entry: [a]
outputs:
  is_json: "{{ inputs.payload.startsWith('{') }}"
  folded: >-
    {{ inputs.payload
       + '}' }}
nodes:
  - id: a
    type: save_message
    args:
      role: assistant
      content: "json? {{ inputs.payload.startsWith('{') }}"
`

func TestBracesInStringLiteralsAndNewlinesEvaluate(t *testing.T) {
	res := runYAMLScenario(t, bracesInLiteralsYAML, &scenario.Scenario{Name: "braces"})
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Equal(t, true, res.Execution.WorkflowOutputs["is_json"])
	require.Equal(t, "{\"a\":1}}", res.Execution.WorkflowOutputs["folded"])
	require.Equal(t, []string{"json? true"}, savedContent(res, "a"))
}
