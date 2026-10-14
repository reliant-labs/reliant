// Copyright (c) 2025 Reliant Labs
package runner

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/stretchr/testify/require"
)

// A `type: workflow` node with an `inline:` body is a workflow of its own: the
// templates inside the body belong to the BODY's scope (its inputs, its nodes),
// and must be evaluated when the body runs, not when the outer node starts.
//
// The outer node used to resolve every CelX field of its whole proto message,
// including the nested inline Workflow, against the OUTER scope. A body node
// reading a sibling's output (`nodes.first.response_text`) therefore failed
// before the body ran, with the outer namespace's "no such key". The Autopilot
// prototype hit this and had to move its agent loop into a separate ref'd
// workflow.
const inlineWorkflowLazyBodyYAML = `
name: inline-lazy-body
inputs:
  topic:
    type: string
    default: "cats"
entry: [sub]
nodes:
  - id: sub
    type: workflow
    inline:
      entry: [first]
      nodes:
        - id: first
          type: call_llm
          args:
            system_prompt: "topic={{inputs.topic}}"
        - id: second
          type: call_llm
          args:
            system_prompt: "first said: {{nodes.first.response_text}}"
      edges:
        - from: first
          default: second
      outputs:
        said: "{{nodes.first.response_text}}"
`

func TestInlineWorkflowBody_TemplatesEvaluateInTheBodyScope(t *testing.T) {
	sc := &scenario.Scenario{
		Name: "inline_lazy_body",
		Events: []scenario.SimulatedEvent{
			{Node: "sub.first", Output: map[string]interface{}{"response_text": "hello"}},
		},
	}
	res := runYAMLScenario(t, inlineWorkflowLazyBodyYAML, sc)
	t.Logf("status=%s outcome=%s err=%+v", res.Status, res.Execution.Outcome, res.Execution.Error)
	require.Equal(t, "completed", res.Execution.Outcome, "%+v", res.Execution.Error)
	require.Equal(t, "hello", res.Execution.NodeOutputs["sub"]["said"])
}
