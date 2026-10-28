package wfyaml

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowConcurrencyRoundTrip(t *testing.T) {
	data := []byte("name: bounded\nconcurrency: {max_running: 3}\nentry: [a]\nnodes: [{id: a, type: compact}]\n")
	wf, err := ParseWorkflow(data)
	require.NoError(t, err)
	encoded, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "max_running: 3")
	again, err := ParseWorkflow(encoded)
	require.NoError(t, err)
	require.Equal(t, wf, again)
}

func TestWorkflowConcurrencyRejectsInvalidPolicy(t *testing.T) {
	for _, policy := range []string{"{}", "{max_running: 0}", "{max_running: -1}", "{max_running: 1.5}", "{max_running: 4294967296}", "{max_running: 2, key: x}", "{max_running: 2, supersede: latest}", "{max_running: 2, max_running: 3}"} {
		t.Run(policy, func(t *testing.T) {
			_, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.Error(t, err)
		})
	}
}

func TestWorkflowConcurrencyAbsentRemainsAbsent(t *testing.T) {
	wf, err := ParseWorkflow([]byte("name: plain\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
	require.NoError(t, err)
	encoded, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "concurrency")
}
