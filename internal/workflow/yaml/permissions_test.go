package wfyaml

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowPermissionsRoundTrip(t *testing.T) {
	src := "name: w\nentry: [a]\npermissions: [approvals:decide]\nnodes:\n- id: a\n  type: save_message\n  args: {role: assistant, content: hi}\n"
	wf, err := ParseWorkflow([]byte(src))
	require.NoError(t, err)
	require.Equal(t, []string{"approvals:decide"}, wf.GetPermissions())
	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	require.Contains(t, string(out), "permissions: ['approvals:decide']")
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	require.Equal(t, wf.GetPermissions(), again.GetPermissions())
}

func TestWorkflowPermissionsRejectBadShapes(t *testing.T) {
	for _, perms := range []string{"approvals:decide", "[approvals:decide, approvals:decide]", "['{{ inputs.p }}']", "['']"} {
		_, err := ParseWorkflow([]byte("name: w\nentry: [a]\npermissions: " + perms + "\nnodes:\n- id: a\n  type: save_message\n  args: {role: assistant, content: hi}\n"))
		require.Error(t, err, perms)
	}
}
