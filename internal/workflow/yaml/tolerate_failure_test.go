// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tolerateFailureYAML = `
name: optional-reader
entry: [reader]
nodes:
  - id: reader
    type: compute
    tolerate_failure: %s
    outputs:
      n: "{{ 1 }}"
`

// tolerate_failure is a node-level boolean (reliant#2752): it parses, survives
// a round trip, and refuses anything that is not true or false.
func TestTolerateFailure_ParsesAndRoundTrips(t *testing.T) {
	t.Parallel()

	wf, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", "true", 1)))
	require.NoError(t, err)
	require.Len(t, wf.GetNodes(), 1)
	assert.True(t, wf.GetNodes()[0].GetTolerateFailure())

	out, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	assert.Contains(t, string(out), "tolerate_failure: true")
	again, err := ParseWorkflow(out)
	require.NoError(t, err)
	assert.True(t, again.GetNodes()[0].GetTolerateFailure())

	off, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", "false", 1)))
	require.NoError(t, err)
	assert.False(t, off.GetNodes()[0].GetTolerateFailure())
	out, err = MarshalWorkflow(off)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "tolerate_failure", "false is the default and is not written")

	for _, bad := range []string{`"yes"`, "maybe", "[true]"} {
		_, err := ParseWorkflow([]byte(strings.Replace(tolerateFailureYAML, "%s", bad, 1)))
		assert.Error(t, err, "tolerate_failure: %s must be refused", bad)
	}
}
