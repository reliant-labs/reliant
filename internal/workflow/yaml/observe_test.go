package wfyaml

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const observeBlockYAML = `
    observe:
      attributes:
        trigger: "{{ output.trigger }}"
      events:
        - name: decision
          when: output.cut == false
          attributes:
            name: cut
            value: "{{ output.cut ? 'cut' : 'no_cut' }}"
            reason: "{{ output.hold }}"
        - name: landed
          each: "{{ output.prs }}"
          attributes: { pr: "{{ item.number }}" }
      metrics:
        - name: rc_train.ticks
          kind: counter
          attributes: { decision: "{{ output.cut ? 'cut' : 'no_cut' }}" }
        - name: node.duration
          kind: histogram
          unit: ms
          value: "{{ node.duration_ms }}"
          when: node.type == 'run'
`

func wantObserveBlock() *reliantv1.NodeObserve {
	return &reliantv1.NodeObserve{
		Attributes: map[string]string{"trigger": "{{ output.trigger }}"},
		Events: []*reliantv1.ObserveEvent{
			{Name: "decision", When: "output.cut == false", Attributes: map[string]string{
				"name": "cut", "value": "{{ output.cut ? 'cut' : 'no_cut' }}", "reason": "{{ output.hold }}",
			}},
			{Name: "landed", Each: "{{ output.prs }}", Attributes: map[string]string{"pr": "{{ item.number }}"}},
		},
		Metrics: []*reliantv1.ObserveMetric{
			{Name: "rc_train.ticks", Kind: "counter", Attributes: map[string]string{"decision": "{{ output.cut ? 'cut' : 'no_cut' }}"}},
			{Name: "node.duration", Kind: "histogram", Unit: "ms", Value: "{{ node.duration_ms }}", When: "node.type == 'run'"},
		},
	}
}

// observe: is a base field of EVERY node kind. The parser recognises a node
// key in three hand-kept places (the base switch, the generated base-field
// list and the activity allow-list), and missing one refuses the key on half
// the kinds: exactly how tolerate_failure came to be refused on action,
// call_llm and execute_tools nodes. So this walks every kind the parser
// binds, not a sample, and a new kind is covered without editing the test.
func TestObserve_EveryNodeKindParsesAndRoundTrips(t *testing.T) {
	t.Parallel()

	kinds := make([]string, 0, len(generatedNodeBindingsByType))
	for kind := range generatedNodeBindingsByType {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for kind := range model.DiscoverNodeMetas() {
		assert.Contains(t, kinds, kind, "node kind %q has no YAML binding to test", kind)
	}

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("name: observe-%s\nentry: [n]\nnodes:\n  - id: n\n    type: %s\n%s", kind, kind, observeBlockYAML)
			wf, err := ParseWorkflow([]byte(src))
			require.NoError(t, err, "observe: must parse on a %s node", kind)
			require.Len(t, wf.GetNodes(), 1)
			assert.True(t, proto.Equal(wantObserveBlock(), wf.GetNodes()[0].GetObserve()), "observe on %s: got %v", kind, wf.GetNodes()[0].GetObserve())

			out, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			again, err := ParseWorkflow(out)
			require.NoError(t, err, "re-parse of:\n%s", out)
			assert.True(t, proto.Equal(wf, again), "round trip of a %s node changed it:\n%s", kind, out)
		})
	}
}

func TestObserve_WorkflowLevelDefaultsRoundTrip(t *testing.T) {
	t.Parallel()

	src := "name: defaults\nentry: [n]\nobserve:\n  metrics:\n    - name: node.duration\n      kind: histogram\n      unit: ms\n      value: \"{{ node.duration_ms }}\"\n      attributes: { node_type: \"{{ node.type }}\" }\n" +
		"nodes:\n  - id: n\n    type: compute\n    outputs: { a: \"{{ 1 }}\" }\n    observe: {}\n"
	wf, err := ParseWorkflow([]byte(src))
	require.NoError(t, err)
	require.Len(t, wf.GetObserve().GetMetrics(), 1)
	assert.Equal(t, "{{ node.type }}", wf.GetObserve().GetMetrics()[0].GetAttributes()["node_type"])
	require.NotNil(t, wf.GetNodes()[0].GetObserve(), "an empty observe: {} is kept")

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err, "re-parse of:\n%s", out)
	assert.True(t, proto.Equal(wf, again), "round trip changed it:\n%s", out)
}

// A literal that YAML would read as another type stays a string after a
// round trip, so a value is never re-typed by being written back.
func TestObserve_LiteralValuesStayStrings(t *testing.T) {
	t.Parallel()

	wf := &reliantv1.Workflow{Name: "lit", Entry: []string{"n"}, Nodes: []*reliantv1.Node{{
		Id: "n", Type: model.NodeTypeCompute,
		Args:    &reliantv1.Node_Compute{Compute: &reliantv1.ComputeArgs{}},
		Observe: &reliantv1.NodeObserve{Attributes: map[string]string{"flag": "true", "n": "14", "empty": "null"}},
	}}}
	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err, "re-parse of:\n%s", out)
	assert.Equal(t, wf.GetNodes()[0].GetObserve().GetAttributes(), again.GetNodes()[0].GetObserve().GetAttributes())
}

func TestObserve_ShapeIsAParseError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, block, want string
		unknown           bool
	}{
		{"unknown block key", "observe: { spans: [] }", `"spans"`, true},
		{"unknown event key", "observe: { events: [{ name: d, label: x }] }", `"label"`, true},
		{"unknown metric key", "observe: { metrics: [{ name: m, kind: counter, bucket: 1 }] }", `"bucket"`, true},
		{"bad kind", "observe: { metrics: [{ name: m, kind: summary }] }", "counter, histogram or gauge", false},
		{"histogram without value", "observe: { metrics: [{ name: m, kind: histogram }] }", "needs a value", false},
		{"bad attribute key", `observe: { attributes: { "Bad Key": x } }`, "must match", false},
		{"bad event name", "observe: { events: [{ name: Decision }] }", "must match", false},
		{"missing event name", "observe: { events: [{ attributes: { a: b } }] }", "event name", false},
		{"duplicate metric", "observe: { metrics: [{ name: m, kind: counter }, { name: m, kind: counter }] }", "declared twice", false},
		{"too many metric attributes", "observe: { metrics: [{ name: m, kind: counter, attributes: { a: x, b: x, c: x, d: x, e: x } }] }", "at most 4", false},
		{"non-scalar attribute", "observe: { attributes: { a: [x] } }", "must be a string", false},
		{"not a mapping", "observe: [a]", "must be a mapping", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := "name: bad\nentry: [n]\nnodes:\n  - id: n\n    type: run\n    command: echo\n    " + tc.block + "\n"
			_, err := ParseWorkflow([]byte(src))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			var unknown *UnknownFieldError
			assert.Equal(t, tc.unknown, errors.As(err, &unknown), "an unknown key inside observe carries the version-skew hint: %v", err)
		})
	}

	var events strings.Builder
	for i := 0; i <= model.ObserveMaxEvents; i++ {
		fmt.Fprintf(&events, "{ name: e%d },", i)
	}
	_, err := ParseWorkflow([]byte("name: bad\nentry: [n]\nobserve: { events: [" + events.String() + "] }\nnodes:\n  - id: n\n    type: run\n    command: echo\n"))
	require.Error(t, err, "the workflow-level block has the same limits")
	assert.Contains(t, err.Error(), "at most 16")
}
