package runtime

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLoadWorkflow_ProtoJSONRoundTrip_PreservesSaveMessageNode(t *testing.T) {
	yamlData := []byte(`
name: save-message-regression
apiVersion: v2
entry: [max_turns_notification]
nodes:
  - id: max_turns_notification
    type: save_message
    args:
      role: assistant
      content: "Reached max turns"
`)

	parsed, err := wfyaml.ParseWorkflow(yamlData)
	require.NoError(t, err)
	require.Len(t, parsed.GetNodes(), 1)
	require.NotNil(t, parsed.GetNodes()[0].GetSaveMessageNode())

	workflowJSON, err := protojson.Marshal(parsed)
	require.NoError(t, err)

	roundTripped, err := LoadWorkflow(workflowJSON)
	require.NoError(t, err)
	require.Len(t, roundTripped.GetNodes(), 1)

	node := roundTripped.GetNodes()[0]
	require.Equal(t, "max_turns_notification", node.GetId())
	require.Equal(t, "save_message", node.GetType())
	require.NotNil(t, node.GetSaveMessageNode())
	require.Equal(t, "assistant", node.GetSaveMessageNode().GetRole().GetLiteral())
}

func TestLoadWorkflow_LegacyJSONFallback_StillSupportsArgsShape(t *testing.T) {
	legacyJSON := []byte(`{
  "name": "legacy-shape",
  "entry": ["save"],
  "nodes": [
    {
      "id": "save",
      "type": "save_message",
      "args": {
        "role": "assistant",
        "content": "legacy"
      }
    }
  ]
}`)

	wf, err := LoadWorkflow(legacyJSON)
	require.NoError(t, err)
	require.Len(t, wf.GetNodes(), 1)
	require.NotNil(t, wf.GetNodes()[0].GetSaveMessageNode())
}
