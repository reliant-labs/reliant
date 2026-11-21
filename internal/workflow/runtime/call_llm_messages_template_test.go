// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func callLLMNode(t *testing.T, args string) *reliantv1.Node {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte("name: w\nentry: [c]\nnodes:\n- id: c\n  type: call_llm\n  args:\n    model: flagship\n" + args))
	require.NoError(t, err)
	return wf.Nodes[0]
}

// AW-41: a call_llm node's messages[].content is a plain string, which
// ResolveCELFields leaves alone, so "{{ nodes.x.response_text }}" reached the
// model as written while validation checked it as a template. It is rendered
// like every other template a node carries; text with no "{{" is unchanged.
func TestEvaluateNodeConfig_RendersCallLLMMessages(t *testing.T) {
	n := callLLMNode(t, `    messages:
      - role: user
        content: |
          Agent said: {{ nodes.main.response_text }}
          {{ size(nodes.main.tool_calls) > 0 ? 'Tool calls pending' : 'No tool calls' }}
      - role: '{{ inputs.role }}'
        content: '{{ inputs.items }}'
      - role: user
        content: Plain text, sent as written.
`)
	nodes := map[string]interface{}{"main": map[string]interface{}{"response_text": "done", "tool_calls": []interface{}{}}}
	inputs := map[string]interface{}{"role": "assistant", "items": []interface{}{"a", "b"}}
	got, err := EvaluateNodeConfig(n, nodes, "id", "w", inputs, nil, nil, nil)
	require.NoError(t, err)

	msgs := got.GetCallLlm().GetMessages()
	require.Len(t, msgs, 3)
	assert.Equal(t, "Agent said: done\nNo tool calls\n", msgs[0].GetContent())
	assert.Equal(t, "assistant", msgs[1].GetRole())
	assert.Equal(t, `["a","b"]`, msgs[1].GetContent(), "a non-text value renders as JSON, as in a system_prompt")
	assert.Equal(t, "Plain text, sent as written.", msgs[2].GetContent())

	// The node the caller passed is not rewritten: a loop renders it afresh
	// on every iteration.
	assert.Contains(t, n.GetCallLlm().GetMessages()[0].GetContent(), "{{ nodes.main.response_text }}")

	args, err := model.NodeArgsAsMap(got)
	require.NoError(t, err)
	first := args["messages"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, "Agent said: done\nNo tool calls\n", first["content"], "the rendered text reaches the call_llm step's input")
}

// A message whose template cannot render fails the node, naming the message,
// as a system_prompt that cannot render does; it is never sent half-rendered.
func TestEvaluateNodeConfig_AMessageThatCannotRenderFailsTheNode(t *testing.T) {
	n := callLLMNode(t, "    messages:\n      - role: user\n        content: 'Review {{ nodes.missing.response_text }}'\n")
	_, err := EvaluateNodeConfig(n, map[string]interface{}{}, "id", "w", map[string]interface{}{}, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "messages[0].content")
}
