package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

const secretRenderWorkflow = `
name: secret-render
entry: [each]
inputs:
  trader_env: {type: string, default: "trader/staging"}
secrets:
  TRADER_KEY:
    environment: "{{ inputs.trader_env }}"
nodes:
  - id: each
    type: loop
    parallel: true
    items: "{{ ['a', 'b'] }}"
    inline:
      entry: [call]
      nodes:
        - id: call
          type: action
          uses: http/request@1
          with:
            url: "https://api.example.com/{{ iter.item }}"
            method: POST
            headers:
              Accept: application/json
              X-API-Key: "{{ secrets.TRADER_KEY }}"
              Authorization: "Bearer {{ secrets.TRADER_KEY }}"
              X-Item: "{{ iter.item }}"
  - id: ship
    type: run
    command: "deploy --item {{ inputs.trader_env }} --key {{ secrets.TRADER_KEY }}"
`

func parseRenderWorkflow(t *testing.T) *reliantv1.Workflow {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(secretRenderWorkflow))
	require.NoError(t, err)
	return wf
}

func renderNode(t *testing.T, node *reliantv1.Node, input map[string]interface{}) *reliantv1.Node {
	t.Helper()
	return renderNodeWithItem(t, node, input, "b")
}

func renderNodeWithItem(t *testing.T, node *reliantv1.Node, input map[string]interface{}, item string) *reliantv1.Node {
	t.Helper()
	out, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "secret-render", input,
		map[string]interface{}{"iteration": 0, "index": 1, "item": item, "key": item}, map[string]interface{}{}, nil)
	require.NoError(t, err)
	return out
}

// A secret is a marker in the rendered node, never a value: the node reaches
// the activity (and Temporal history) with the author's text and the marker,
// and its secret_refs carry the environment evaluated for THIS iteration.
func TestActionNodeInALoopRendersMarkersAndTheEvaluatedEnvironment(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[0].GetLoop().GetInline().GetNodes()[0]
	require.Len(t, node.GetSecretRefs(), 1)

	out := renderNode(t, node, map[string]interface{}{"trader_env": "trader/prod"})

	require.Len(t, out.GetSecretRefs(), 1)
	assert.Equal(t, "TRADER_KEY", out.GetSecretRefs()[0].GetName())
	assert.Equal(t, "trader/prod", out.GetSecretRefs()[0].GetEnvironment().GetLiteral(), "environment is evaluated at render time")

	headers := out.GetAction().GetWith()["headers"].GetStructValue().GetFields()
	marker := stepsecrets.Marker("TRADER_KEY")
	assert.Equal(t, marker, headers["X-API-Key"].GetStringValue())
	assert.Equal(t, "Bearer "+marker, headers["Authorization"].GetStringValue())
	assert.Equal(t, "b", headers["X-Item"].GetStringValue())
	assert.Equal(t, "application/json", headers["Accept"].GetStringValue())
	assert.Equal(t, "https://api.example.com/b", out.GetAction().GetWith()["url"].GetStringValue())

	raw, err := protojson.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secrets.", "the template text never survives rendering")
}

// A run step never receives a secret: its node carries no refs, and a hole in
// its command (one that got past validation) fails rendering rather than reach
// the machine.
func TestRunNodeGetsNoSecretAndAHoleInItsCommandFails(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[1]
	assert.Empty(t, node.GetSecretRefs(), "a run node is handed no secret")
	_, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "secret-render",
		map[string]interface{}{"trader_env": "trader/prod"}, map[string]interface{}{}, map[string]interface{}{}, nil)
	require.Error(t, err)
}

// A value that merely looks like a secret reference, arriving as data (an
// input or an iteration item), is data: it renders as itself and is never a
// marker that could fill a credential slot.
func TestDataThatLooksLikeASecretReferenceNeverBecomesAMarker(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[0].GetLoop().GetInline().GetNodes()[0]
	forged := "{{ secrets.TRADER_KEY }} " + stepsecrets.Marker("TRADER_KEY")
	out := renderNodeWithItem(t, node, map[string]interface{}{"trader_env": "trader/prod"}, forged)

	headers := out.GetAction().GetWith()["headers"].GetStructValue().GetFields()
	assert.False(t, stepsecrets.HasMarker(headers["X-Item"].GetStringValue()), "data never becomes a marker: %q", headers["X-Item"].GetStringValue())
	assert.False(t, stepsecrets.HasMarker(out.GetAction().GetWith()["url"].GetStringValue()))
	assert.True(t, stepsecrets.HasMarker(headers["Authorization"].GetStringValue()), "the author's own hole still is one")
}

func TestSecretEnvironmentThatCannotRenderFailsTheNode(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[0].GetLoop().GetInline().GetNodes()[0]
	_, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "secret-render",
		map[string]interface{}{}, map[string]interface{}{"iteration": 0, "index": 0, "item": "a", "key": "a"}, map[string]interface{}{}, nil)
	require.Error(t, err, "an environment that reads a missing input must not become an empty reference")
}
