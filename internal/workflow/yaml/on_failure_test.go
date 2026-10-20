package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const onFailureYAML = `name: guarded
entry: [work]
nodes:
  - id: work
    type: compact
on_failure:
  sequence:
    - id: note
      type: save_message
      args:
        role: assistant
        content: "failed at {{ failure.node_id }}"
`

// The handler is a whole inline graph, so the builder, which saves by
// marshalling the parsed proto, must carry it through unchanged.
func TestOnFailure_RoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(onFailureYAML))
	require.NoError(t, err)
	require.NotNil(t, wf.GetOnFailure())
	require.Len(t, wf.GetOnFailure().GetNodes(), 1)
	assert.Equal(t, "note", wf.GetOnFailure().GetNodes()[0].GetId())
	// sequence: sugar compiles inside the handler like anywhere else.
	assert.Equal(t, []string{"note"}, wf.GetOnFailure().GetEntry())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "on_failure:")

	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	require.NotNil(t, again.GetOnFailure(), "round trip lost on_failure:\n%s", out)
	assert.Equal(t, "note", again.GetOnFailure().GetNodes()[0].GetId())
}

func TestOnFailure_OmittedWhenUnset(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: plain\nentry: [a]\nnodes:\n  - id: a\n    type: compact\n"))
	require.NoError(t, err)
	assert.Nil(t, wf.GetOnFailure())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "on_failure"), "unset on_failure emitted:\n%s", out)
}
