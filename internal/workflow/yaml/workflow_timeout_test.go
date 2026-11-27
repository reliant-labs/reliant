// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The run timeout survives the builder's save (it marshals the parsed proto).
func TestWorkflowTimeout_RoundTrip(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: bounded
timeout: 3h
entry: [a]
nodes:
  - id: a
    type: compact
`))
	require.NoError(t, err)
	assert.Equal(t, "3h", wf.GetTimeout().GetLiteral())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "timeout: 3h\n")

	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.Equal(t, "3h", again.GetTimeout().GetLiteral(), "round trip lost timeout:\n%s", out)
}

func TestWorkflowTimeout_OmittedWhenUnset(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: unbounded
entry: [a]
nodes:
  - id: a
    type: compact
`))
	require.NoError(t, err)
	assert.Nil(t, wf.GetTimeout())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(out), "timeout"), "unset timeout emitted:\n%s", out)
}
