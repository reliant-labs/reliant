package runtime

import (
	"strings"
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
              Authorization: "Bearer {{ secrets.TRADER_KEY }} for {{ iter.item }}"
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
	out, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "secret-render", input,
		map[string]interface{}{"iteration": 0, "index": 1, "item": "b", "key": "b"}, map[string]interface{}{}, nil)
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
	assert.Equal(t, "Bearer "+marker+" for b", headers["Authorization"].GetStringValue())
	assert.Equal(t, "application/json", headers["Accept"].GetStringValue())
	assert.Equal(t, "https://api.example.com/b", out.GetAction().GetWith()["url"].GetStringValue())

	raw, err := protojson.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secrets.", "the template text never survives rendering")
}

func TestRunNodeRendersAMarkerAndTheEvaluatedEnvironment(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[1]
	require.Len(t, node.GetSecretRefs(), 1, "a run node is handed every declared secret")

	out := renderNode(t, node, map[string]interface{}{"trader_env": "trader/prod"})

	assert.Equal(t, "deploy --item trader/prod --key "+stepsecrets.Marker("TRADER_KEY"), out.GetRun().GetCommand().GetLiteral())
	assert.Equal(t, "trader/prod", out.GetSecretRefs()[0].GetEnvironment().GetLiteral())
}

// A value that merely looks like a secret reference, arriving as data (an
// input or an iteration item), is data: it renders as itself and is never a
// marker the activity would substitute.
func TestDataThatLooksLikeASecretReferenceNeverBecomesAMarker(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[1]
	out := renderNode(t, node, map[string]interface{}{"trader_env": "{{ secrets.TRADER_KEY }} " + stepsecrets.Marker("TRADER_KEY")})

	command := out.GetRun().GetCommand().GetLiteral()
	assert.Equal(t, 1, strings.Count(command, stepsecrets.Marker("TRADER_KEY")), "only the author's reference is a marker: %q", command)
}

func TestSecretEnvironmentThatCannotRenderFailsTheNode(t *testing.T) {
	node := parseRenderWorkflow(t).GetNodes()[1]
	_, err := EvaluateNodeConfig(node, map[string]interface{}{}, "wf-1", "secret-render",
		map[string]interface{}{}, map[string]interface{}{}, map[string]interface{}{}, nil)
	require.Error(t, err, "an environment that reads a missing input must not become an empty reference")
}
