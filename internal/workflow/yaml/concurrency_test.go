package wfyaml

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

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
	for _, policy := range []string{"{}", "{max_running: 0}", "{max_running: -1}", "{max_running: 1.5}", "{max_running: 4294967296}", "{max_running: 2, key: x}", "{max_running: 2, supersede: latest}", "{max_running: 2, max_running: 3}", "{max_running: 2, waiting: newest}", "{max_running: 2, waiting: fifo, key: '{{ trigger.kind }}'}", "{max_running: 2, waiting: latest, key: ''}", "{waiting: latest}"} {
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

func TestWorkflowConcurrencyWaitingLatestRoundTrip(t *testing.T) {
	for _, policy := range []string{
		"{max_running: 1, waiting: latest, key: '{{ trigger.payload.attributes.repository }}'}",
		"{max_running: 1, waiting: latest}",
		"{max_running: 2, waiting: fifo}",
	} {
		t.Run(policy, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.NoError(t, err)
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			again, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, wf, again)
		})
	}
	wf, err := ParseWorkflow([]byte("name: bounded\nconcurrency: {max_running: 1, waiting: latest, key: '{{ trigger.payload.attributes.repository }}'}\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
	require.NoError(t, err)
	require.Equal(t, reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST, wf.GetConcurrency().GetWaiting())
	require.Equal(t, "{{ trigger.payload.attributes.repository }}", wf.GetConcurrency().GetKey())
}
