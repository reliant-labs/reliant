package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func modelSelectorRaw(selector *reliantv1.CelModelSelector) string {
	if selector == nil {
		return ""
	}
	if expr := model.CelModelSelectorExpr(selector); expr != "" {
		return expr
	}
	literal := model.CelModelSelectorValue(selector)
	if literal == nil {
		return ""
	}
	return literal.Id
}

// TestWorkflowRoundTrip_CallLLMNode verifies call_llm args survive round-trip conversion.
func TestWorkflowRoundTrip_CallLLMNode(t *testing.T) {
	yamlWorkflow := `
name: test-workflow
description: Test workflow with call_llm node

inputs:
  model:
    type: model
  tools:
    type: tools

entry: [llm_node]
nodes:
  - id: llm_node
    type: call_llm
    model: "{{inputs.model}}"
    system_prompt: |
      You are a test assistant.
      This is a multi-line prompt.
    tools_config:
      filter: "{{inputs.tools + ['spawn:builtin://agent']}}"
`

	protoWf, err := parseDraftDefinitionV2([]byte(yamlWorkflow))
	require.NoError(t, err)
	require.Len(t, protoWf.Nodes, 1)

	node1 := protoWf.Nodes[0]
	callLLMArgs := model.GetCallLLMArgs(node1)
	require.NotNil(t, callLLMArgs)
	assert.Equal(t, "{{inputs.model}}", modelSelectorRaw(callLLMArgs.Model))
	assert.Contains(t, model.CelStringRaw(callLLMArgs.SystemPrompt), "You are a test assistant")
	require.NotNil(t, callLLMArgs.GetToolsConfig())
	assert.Equal(t, "{{inputs.tools + ['spawn:builtin://agent']}}", model.CelStringListExpr(callLLMArgs.GetToolsConfig().GetFilter()))

	yamlBytes, err := rpcWorkflowToYAML(protoWf)
	require.NoError(t, err)

	protoWf2, err := parseDraftDefinitionV2(yamlBytes)
	require.NoError(t, err)
	require.Len(t, protoWf2.Nodes, 1)

	node2 := protoWf2.Nodes[0]
	callLLMArgs2 := model.GetCallLLMArgs(node2)
	require.NotNil(t, callLLMArgs2)
	assert.Equal(t, "{{inputs.model}}", modelSelectorRaw(callLLMArgs2.Model))
	assert.Contains(t, model.CelStringRaw(callLLMArgs2.SystemPrompt), "You are a test assistant")
	require.NotNil(t, callLLMArgs2.GetToolsConfig())
	assert.Equal(t, "{{inputs.tools + ['spawn:builtin://agent']}}", model.CelStringListExpr(callLLMArgs2.GetToolsConfig().GetFilter()))
}

func TestWorkflowRoundTrip_NestedLoopNode(t *testing.T) {
	yamlWorkflow := `
name: loop-test
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "iter.iteration < 3"
    inline:
      entry: [llm]
      nodes:
        - id: llm
          type: call_llm
          model: "claude-3-5-sonnet-20241022"
          system_prompt: "You are a helpful assistant."
`

	protoWf, err := parseDraftDefinitionV2([]byte(yamlWorkflow))
	require.NoError(t, err)
	require.Len(t, protoWf.Nodes, 1)

	loopArgs := model.GetLoopArgs(protoWf.Nodes[0])
	require.NotNil(t, loopArgs)
	require.NotNil(t, loopArgs.Inline)
	require.Len(t, loopArgs.Inline.Nodes, 1)

	inlineLLM := loopArgs.Inline.Nodes[0]
	inlineCallLLMArgs := model.GetCallLLMArgs(inlineLLM)
	require.NotNil(t, inlineCallLLMArgs)
	assert.Equal(t, "claude-3-5-sonnet-20241022", modelSelectorRaw(inlineCallLLMArgs.Model))
	assert.Equal(t, "You are a helpful assistant.", model.CelStringRaw(inlineCallLLMArgs.SystemPrompt))
}

func TestParseDraftDefinitionV2_LegacyTopLevelActivityArgs(t *testing.T) {
	yamlWorkflow := `
name: legacy-call-llm
entry: [llm_node]
nodes:
  - id: llm_node
    type: call_llm
    model: "{{inputs.model}}"
    system_prompt: "legacy top-level"
`

	protoWf, err := parseDraftDefinitionV2([]byte(yamlWorkflow))
	require.NoError(t, err)
	require.Len(t, protoWf.Nodes, 1)

	callLLMArgs := model.GetCallLLMArgs(protoWf.Nodes[0])
	require.NotNil(t, callLLMArgs)
	assert.Equal(t, "{{inputs.model}}", modelSelectorRaw(callLLMArgs.Model))
	assert.Equal(t, "legacy top-level", model.CelStringRaw(callLLMArgs.SystemPrompt))
}

func TestParseDraftDefinitionV2_ExplicitArgsTakePrecedenceOverLegacyTopLevel(t *testing.T) {
	yamlWorkflow := `
name: explicit-args-wins
entry: [llm_node]
nodes:
  - id: llm_node
    type: call_llm
    model: "{{inputs.legacy_model}}"
    args:
      model: "{{inputs.typed_model}}"
      system_prompt: "typed args"
`

	protoWf, err := parseDraftDefinitionV2([]byte(yamlWorkflow))
	require.NoError(t, err)
	require.Len(t, protoWf.Nodes, 1)

	callLLMArgs := model.GetCallLLMArgs(protoWf.Nodes[0])
	require.NotNil(t, callLLMArgs)
	assert.Equal(t, "{{inputs.typed_model}}", modelSelectorRaw(callLLMArgs.Model))
	assert.Equal(t, "typed args", model.CelStringRaw(callLLMArgs.SystemPrompt))
}
