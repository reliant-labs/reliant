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

func runNode(t *testing.T, args string) *reliantv1.Node {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte("name: w\nentry: [r]\nnodes:\n- id: r\n  type: run\n  args:\n    command: echo hi\n" + args))
	require.NoError(t, err)
	return wf.Nodes[0]
}

// A run node's writable_checkouts are rendered like its work_dir: a per-run
// directory a step before printed is what the sandbox keeps. They used to
// reach the daemon as the literal "{{ nodes.p.dir }}", which kept nothing, so
// a credentials:none step's hand-off was discarded (control-plane pr-regen).
func TestEvaluateNodeConfig_RendersRunWritableCheckouts(t *testing.T) {
	n := runNode(t, `    credentials: none
    work_dir: '{{ nodes.p.dir }}'
    writable_checkouts: ['{{ nodes.p.dir }}', reliant, '{{ nodes.p.extra }}', '{{ nodes.p.dir }}/out']
`)
	nodes := map[string]interface{}{"p": map[string]interface{}{"dir": "/x/run-1", "extra": []interface{}{"/x/a", "/x/b"}}}
	got, err := EvaluateNodeConfig(n, nodes, "id", "w", map[string]interface{}{}, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"/x/run-1", "reliant", "/x/a", "/x/b", "/x/run-1/out"}, got.GetRun().GetWritableCheckouts())
	args, err := model.NodeArgsAsMap(got)
	require.NoError(t, err)
	assert.Equal(t, "/x/run-1", args["work_dir"], "work_dir reaches the run step's input")
	assert.Equal(t, []interface{}{"/x/run-1", "reliant", "/x/a", "/x/b", "/x/run-1/out"}, args["writable_checkouts"])
}

// One that renders empty fails the node rather than run a command that would
// keep nothing it was meant to hand on.
func TestEvaluateNodeConfig_AnEmptyWritableCheckoutFailsTheNode(t *testing.T) {
	n := runNode(t, "    credentials: none\n    writable_checkouts: ['{{ nodes.p.dir }}']\n")
	_, err := EvaluateNodeConfig(n, map[string]interface{}{"p": map[string]interface{}{"dir": ""}}, "id", "w", map[string]interface{}{}, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rendered empty")
}
