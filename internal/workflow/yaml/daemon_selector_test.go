package wfyaml

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// daemonSelectorWorkflow is a workflow whose workflow-level AND node-level
// selector are both `daemon: <sel>`.
func daemonSelectorWorkflow(sel string) string {
	return "name: pin\ndaemon: " + sel + "\nentry: [a]\nnodes:\n- id: a\n  type: run\n  daemon: " + sel + "\n  args: {command: make test}\n"
}

// A selector parses into DaemonSelectorProto, marshals back in the form
// given, and parses again to the same proto, at the workflow and the node.
func TestDaemonSelectorRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		sel  string
		want *reliantv1.DaemonSelectorProto
		// yaml is how the selector is written back; a pool is never the
		// compact string, which would read back as a machine name.
		yaml string
	}{
		{"pool", "{pool: gate}", &reliantv1.DaemonSelectorProto{Pool: "gate"}, "pool: gate"},
		{"labels", "{labels: {role: gate, os: linux}}", &reliantv1.DaemonSelectorProto{Labels: map[string]string{"role": "gate", "os": "linux"}}, "labels:"},
		{"labels narrow a type", "{type: cloud, labels: {gpu: \"true\"}}", &reliantv1.DaemonSelectorProto{Type: "cloud", Labels: map[string]string{"gpu": "true"}}, "type: cloud"},
		{"name shorthand", "box-1", &reliantv1.DaemonSelectorProto{Name: "box-1"}, "daemon: box-1"},
		{"type shorthand", "local", &reliantv1.DaemonSelectorProto{Type: "local"}, "daemon: local"},
		{"id", "{id: 6f1c}", &reliantv1.DaemonSelectorProto{Id: "6f1c"}, "id: 6f1c"},
		// Invalid together (validation rejects it), but the round trip must
		// not drop the pool by writing the compact type form.
		{"pool with a type stays a mapping", "{type: local, pool: gate}", &reliantv1.DaemonSelectorProto{Type: "local", Pool: "gate"}, "pool: gate"},
		{"pool with a name stays a mapping", "{name: box-1, pool: gate}", &reliantv1.DaemonSelectorProto{Name: "box-1", Pool: "gate"}, "pool: gate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte(daemonSelectorWorkflow(tc.sel)))
			require.NoError(t, err)
			assert.True(t, proto.Equal(tc.want, wf.GetDaemon().GetLiteral()), "workflow: %v", wf.GetDaemon())
			assert.True(t, proto.Equal(tc.want, wf.GetNodes()[0].GetDaemon().GetLiteral()), "node: %v", wf.GetNodes()[0].GetDaemon())

			out, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			assert.Contains(t, string(out), tc.yaml)

			again, err := ParseWorkflow(out)
			require.NoError(t, err, "%s", out)
			assert.True(t, proto.Equal(tc.want, again.GetDaemon().GetLiteral()), "workflow after round trip:\n%s", out)
			assert.True(t, proto.Equal(tc.want, again.GetNodes()[0].GetDaemon().GetLiteral()), "node after round trip:\n%s", out)
		})
	}
}

// The pool is written as {pool: gate}, never as the bare string "gate".
func TestDaemonPoolMarshalsAsAMapping(t *testing.T) {
	node, err := marshalDaemonSelector(&reliantv1.DaemonSelectorProto{Pool: "gate"})
	require.NoError(t, err)
	require.Equal(t, yaml.MappingNode, node.Kind)
	require.Len(t, node.Content, 2)
	assert.Equal(t, "pool", node.Content[0].Value)
	assert.Equal(t, "gate", node.Content[1].Value)
}

// A blank pool is refused at parse time: the proto cannot tell `pool: ""`
// from no pool, so it would otherwise become an empty selector.
func TestDaemonPoolBlankIsRejected(t *testing.T) {
	for _, sel := range []string{`{pool: ""}`, `{pool: "  "}`, `{pool: }`, `{pool: [a]}`, `{pool: {a: b}}`} {
		t.Run(sel, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(daemonSelectorWorkflow(sel)))
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), "daemon pool must name one of your machine pools"), err.Error())
		})
	}
}
