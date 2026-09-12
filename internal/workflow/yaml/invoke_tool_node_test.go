package wfyaml

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseInvokeToolNode is the end-to-end authoring check: this is exactly
// what a human writes in a workflow, and it must land in the proto as an
// invoke_tool node with its params intact.
//
// It also pins the two forms a param can take. `prompt` is a literal;
// `save_to` carries a {{expr}} template referencing an upstream node. Both are
// plain map values here — the template is resolved later, by the same CEL pass
// that resolves every other node's arguments.
func TestParseInvokeToolNode(t *testing.T) {
	source := []byte(`
name: hero-image
nodes:
  - id: plan
    type: call_llm
    args:
      model:
        tags: [flagship]
  - id: hero
    type: invoke_tool
    tool: generate_image
    params:
      prompt: "{{nodes.plan.response_text}}"
      size: "1536x1024"
      save_to: "assets/hero.png"
edges:
  - from: plan
    to: hero
`)

	workflow, err := ParseWorkflow(source)
	require.NoError(t, err)

	var heroNode = findNode(t, workflow.GetNodes(), "hero")
	assert.Equal(t, model.NodeTypeInvokeTool, heroNode.GetType())

	args := heroNode.GetInvokeTool()
	require.NotNil(t, args, "the invoke_tool oneof arm must be populated")
	assert.Equal(t, "generate_image", model.CelStringRaw(args.GetTool()))

	params := args.GetParams()
	require.Len(t, params, 3)
	assert.Equal(t, "{{nodes.plan.response_text}}", params["prompt"].GetStringValue(),
		"a templated param must survive parsing unresolved; CEL resolves it at dispatch")
	assert.Equal(t, "1536x1024", params["size"].GetStringValue())
	assert.Equal(t, "assets/hero.png", params["save_to"].GetStringValue())
}

// TestParseInvokeToolNodeWithoutParams pins the zero-configuration path. A
// tool must be fully functional with no params bound at the call site —
// everything can come from bindings or the tool's own defaults.
func TestParseInvokeToolNodeWithoutParams(t *testing.T) {
	source := []byte(`
name: minimal
nodes:
  - id: pic
    type: invoke_tool
    tool: generate_image
`)

	workflow, err := ParseWorkflow(source)
	require.NoError(t, err)

	args := findNode(t, workflow.GetNodes(), "pic").GetInvokeTool()
	require.NotNil(t, args)
	assert.Equal(t, "generate_image", model.CelStringRaw(args.GetTool()))
	assert.Empty(t, args.GetParams())
}

func findNode(t *testing.T, nodes []*reliantv1.Node, id string) *reliantv1.Node {
	t.Helper()
	for _, node := range nodes {
		if node.GetId() == id {
			return node
		}
	}
	t.Fatalf("node %q not found", id)
	return nil
}
