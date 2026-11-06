// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
)

// control-plane cp-merge, 2026-10-30: the reference, the validator and the
// scenario runner all said a `type: workflow` node's outputs are at
// nodes.<id>.outputs, but the runtime published them only flat. A workflow
// reading nodes.regen.?outputs.?result passed validation and every scenario,
// and read "error" on every live run. Both shapes now hold.

const workflowNodeOutputsRootYAML = `
name: agent
entry: [regen, inline]
daemon:
  id: daemon-1
nodes:
  - id: regen
    type: workflow
    ref: regen-child
  - id: inline
    type: workflow
    inline:
      entry: [c]
      nodes:
        - id: c
          type: compute
          outputs:
            result: "{{ 'inline-ok' }}"
      outputs:
        result: "{{ nodes.c.result }}"
outputs:
  ref_nested: "{{ nodes.regen.outputs.result }}"
  ref_flat: "{{ nodes.regen.result }}"
  ref_optional: "{{ nodes.regen.?outputs.?result.orValue('error') }}"
  inline_nested: "{{ nodes.inline.outputs.result }}"
  inline_flat: "{{ nodes.inline.result }}"
`

const workflowNodeOutputsChildYAML = `
name: regen-child
entry: [c]
nodes:
  - id: c
    type: compute
    outputs:
      result: "{{ 'no_drift' }}"
outputs:
  result: "{{ nodes.c.result }}"
`

func (s *MachineBlipRunSuite) TestWorkflowNodeOutputsAreNestedAndFlat() {
	e, _, input := s.setupYAML("chat-wf-outputs", workflowNodeOutputsRootYAML, true,
		func(context.Context, map[string]interface{}, int) (map[string]interface{}, error) {
			return gatePassed(), nil
		})
	e.workflowYAMLs["regen-child"] = workflowNodeOutputsChildYAML

	e.env.ExecuteWorkflow(DynamicWorkflow, input)

	s.Require().True(e.env.IsWorkflowCompleted())
	s.Require().NoError(e.env.GetWorkflowError())
	var result WorkflowResult
	s.Require().NoError(e.env.GetWorkflowResult(&result))
	s.Equal("no_drift", result.Outputs["ref_nested"])
	s.Equal("no_drift", result.Outputs["ref_flat"])
	s.Equal("no_drift", result.Outputs["ref_optional"], "the shape cp-merge read")
	s.Equal("inline-ok", result.Outputs["inline_nested"])
	s.Equal("inline-ok", result.Outputs["inline_flat"])
}
