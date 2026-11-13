package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const finallyYAML = `name: leased
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
finally:
  sequence:
    - id: release
      type: save_message
      args:
        role: assistant
        content: "released ({{ run.status }})"
`

// The builder saves by marshalling the parsed proto, so finally must survive
// a round trip beside on_failure.
func TestFinally_RoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(finallyYAML))
	require.NoError(t, err)
	require.NotNil(t, wf.GetFinally())
	assert.Equal(t, []string{"release"}, wf.GetFinally().GetEntry())
	require.NotNil(t, wf.GetOnFailure())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	require.NotNil(t, again.GetFinally(), "round trip lost finally:\n%s", out)
	assert.Equal(t, "release", again.GetFinally().GetNodes()[0].GetId())
	assert.NotNil(t, again.GetOnFailure())
}

func TestFinally_OmittedWhenUnset(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: plain\nentry: [a]\nnodes:\n  - id: a\n    type: compact\n"))
	require.NoError(t, err)
	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "finally"), "unset finally emitted:\n%s", out)
}

// Its templates read run and failure, which exist only once the run has
// ended: they are not resolved when the definition loads.
func TestFinally_IsRuntimeEvaluated(t *testing.T) {
	assert.True(t, IsRuntimeEvaluatedTopLevelKey("finally"))
}
