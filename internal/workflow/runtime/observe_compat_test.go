package runtime

import (
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// observedWorkflowYAML uses observe: at both levels, the way a definition
// written once workflows adopt it will.
const observedWorkflowYAML = `
name: observed
entry: [plan]
observe:
  metrics:
    - name: node.duration
      kind: histogram
      unit: ms
      value: "{{ node.duration_ms }}"
nodes:
  - id: plan
    type: compute
    outputs:
      cut: "{{ false }}"
    observe:
      events:
        - name: decision
          attributes: { name: cut, value: "{{ output.cut ? 'cut' : 'no_cut' }}" }
  - id: land
    type: run
    command: "echo ok"
    observe:
      attributes: { exit: "{{ output.exit_code }}" }
edges:
  - from: plan
    default: land
`

// observedDefinitionJSON is the definition as a run stores it (protojson).
func observedDefinitionJSON(t *testing.T) []byte {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(observedWorkflowYAML))
	require.NoError(t, err)
	require.NotNil(t, wf.GetObserve())
	data, err := protojson.Marshal(wf)
	require.NoError(t, err)
	return data
}

func TestLoadWorkflow_ReadsObserve(t *testing.T) {
	t.Parallel()
	wf, err := LoadWorkflow(observedDefinitionJSON(t))
	require.NoError(t, err)
	assert.Equal(t, "node.duration", wf.GetObserve().GetMetrics()[0].GetName())
	assert.Equal(t, "decision", wf.GetNodes()[0].GetObserve().GetEvents()[0].GetName())
}

// A definition written by a newer server (a rolling deploy, or a rollback
// after a workflow adopted a new key) carries fields this build does not
// define. Replaying its run must still load it: the unknown fields are
// dropped and every known one reads exactly as before.
func TestLoadWorkflow_ToleratesFieldsThisBuildPredates(t *testing.T) {
	t.Parallel()

	var def map[string]any
	require.NoError(t, json.Unmarshal(observedDefinitionJSON(t), &def))
	def["observeV2"] = map[string]any{"sampling": 0.5}
	nodes := def["nodes"].([]any)
	nodes[1].(map[string]any)["recordEverything"] = true
	nodes[1].(map[string]any)["run"].(map[string]any)["sandboxProfile"] = "strict"
	data, err := json.Marshal(def)
	require.NoError(t, err)

	require.Error(t, protojson.Unmarshal(data, &reliantv1.Workflow{}), "the fixture must carry fields this build does not define")

	wf, err := LoadWorkflow(data)
	require.NoError(t, err, "a definition with fields from a newer build must load")
	want, err := LoadWorkflow(observedDefinitionJSON(t))
	require.NoError(t, err)
	assert.True(t, proto.Equal(want, wf), "the known fields read exactly as without the unknown ones")

	_, err = LoadWorkflow([]byte(`{"name": "broken", "nodes": [`))
	assert.Error(t, err, "malformed JSON is still refused")
	_, err = LoadWorkflow([]byte(`{"name": "typed", "nodes": [{"id": "a", "tolerateFailure": "sometimes"}], "observeV2": {}}`))
	assert.Error(t, err, "a known field of the wrong type is still refused")
}

// preObserveTypes returns Node and Workflow as a build that predates
// observe: defines them: workflow_v2.proto with the observe fields and
// messages removed. It is how a released worker sees a definition written
// after workflows adopt observe.
func preObserveTypes(t *testing.T) (node, workflow protoreflect.MessageType) {
	t.Helper()
	fdp := protodesc.ToFileDescriptorProto(reliantv1.File_reliant_v1_workflow_v2_proto)
	fdp = proto.Clone(fdp).(*descriptorpb.FileDescriptorProto)
	var kept []*descriptorpb.DescriptorProto
	for _, m := range fdp.GetMessageType() {
		switch m.GetName() {
		case "NodeObserve", "ObserveEvent", "ObserveMetric":
			continue
		case "Node", "Workflow":
			var fields []*descriptorpb.FieldDescriptorProto
			for _, f := range m.GetField() {
				if f.GetName() != "observe" {
					fields = append(fields, f)
				}
			}
			require.Len(t, fields, len(m.GetField())-1, "%s must lose exactly its observe field", m.GetName())
			m.Field = fields
		}
		kept = append(kept, m)
	}
	fdp.MessageType = kept
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	require.NoError(t, err)
	return dynamicpb.NewMessageType(fd.Messages().ByName("Node")),
		dynamicpb.NewMessageType(fd.Messages().ByName("Workflow"))
}

// The compatibility contract of design workflow-observe.md section 11, against
// a worker that predates observe:.
//
//   - An activity's node arrives as protojson decoded with DiscardUnknown
//     (types.ActivityInput.UnmarshalJSON), which every released worker does:
//     the old worker drops observe and runs the node unchanged.
//   - Workflow code loads the definition with LoadWorkflow. Before this
//     release that read was strict, so a pre-release worker refuses an
//     observed definition: that is why workflows adopt observe only once this
//     release is the floor. From this release it reads tolerantly, so the
//     next additive key does not need the same wait.
func TestObserve_WorkerThatPredatesItIgnoresIt(t *testing.T) {
	t.Parallel()
	oldNode, oldWorkflow := preObserveTypes(t)

	// The activity input, as the new workflow code sends it for the land node.
	full, err := LoadWorkflow(observedDefinitionJSON(t))
	require.NoError(t, err)
	land := full.GetNodes()[1]
	require.NotNil(t, land.GetObserve())
	input, err := json.Marshal(&types.ActivityInput{Node: land})
	require.NoError(t, err)
	var wire struct {
		Node json.RawMessage `json:"node"`
	}
	require.NoError(t, json.Unmarshal(input, &wire))
	require.Contains(t, string(wire.Node), `"observe"`, "the new node carries observe on the wire")

	// The activity decode options every released worker uses.
	var decoded types.ActivityInput
	require.NoError(t, json.Unmarshal(input, &decoded))
	require.True(t, proto.Equal(land, decoded.Node), "this build decodes the node whole")
	oldDecoded := oldNode.New().Interface()
	require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(wire.Node, oldDecoded),
		"a worker that predates observe decodes the activity's node")
	landWithout := proto.Clone(land).(*reliantv1.Node)
	landWithout.Observe = nil
	assertSameMessage(t, landWithout, oldDecoded, "the old worker sees the node exactly as without observe")

	// Workflow code: the definition.
	def := observedDefinitionJSON(t)
	assert.Error(t, protojson.Unmarshal(def, oldWorkflow.New().Interface()),
		"a strict pre-release LoadWorkflow refuses an observed definition: adoption waits for this release")
	oldLoaded := oldWorkflow.New().Interface()
	require.NoError(t, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(def, oldLoaded),
		"LoadWorkflow's tolerant read accepts a definition with fields its build predates")
	fullWithout := proto.Clone(full).(*reliantv1.Workflow)
	fullWithout.Observe = nil
	for _, n := range fullWithout.GetNodes() {
		n.Observe = nil
	}
	assertSameMessage(t, fullWithout, oldLoaded, "the old definition is the new one without observe")
}

// assertSameMessage compares a generated message with a dynamic one of the
// same full name by their wire bytes.
func assertSameMessage(t *testing.T, want proto.Message, got proto.Message, msg string) {
	t.Helper()
	opts := proto.MarshalOptions{Deterministic: true}
	wantBytes, err := opts.Marshal(want)
	require.NoError(t, err)
	gotBytes, err := opts.Marshal(got)
	require.NoError(t, err)
	assert.Equal(t, wantBytes, gotBytes, msg)
}
