package wfyaml

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseActionNode(t *testing.T) {
	workflow, err := ParseWorkflow([]byte(`
name: ping
nodes:
  - id: call
    type: action
    uses: http/request@1
    with:
      url: "https://example.com/{{inputs.path}}"
      method: POST
      body: { a: 1 }
`))
	require.NoError(t, err)
	node := findNode(t, workflow.GetNodes(), "call")
	assert.Equal(t, model.NodeTypeAction, node.GetType())
	args := node.GetAction()
	require.NotNil(t, args, "the action oneof arm must be populated")
	assert.Equal(t, "http/request@1", model.CelStringRaw(args.GetUses()))
	assert.Equal(t, "POST", args.GetWith()["method"].GetStringValue())
	assert.Equal(t, "https://example.com/{{inputs.path}}", args.GetWith()["url"].GetStringValue())
	assert.NotNil(t, args.GetWith()["body"].GetStructValue())
}
