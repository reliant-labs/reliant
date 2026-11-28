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
	for _, policy := range []string{"{}", "{max_running: 0}", "{max_running: -1}", "{max_running: 1.5}", "{max_running: 4294967296}", "{max_running: 2, key: x}", "{max_running: 2, supersede: latest}", "{max_running: 2, max_running: 3}", "{max_running: 2, waiting: newest}", "{max_running: 2, waiting: fifo, key: '{{ trigger.kind }}'}", "{max_running: 2, waiting: latest, key: ''}", "{waiting: latest}", "{per_member: 0}", "{per_member: pool}", "{per_member: 2, per_member: 3}"} {
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

func TestWorkflowConcurrencyPriorityAndPerKeyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		policy   string
		priority string
		perKey   uint32
	}{
		{"{max_running: 2, priority: \"'main-red' in trigger.payload.labels ? 10 : 0\"}", "'main-red' in trigger.payload.labels ? 10 : 0", 0},
		{"{max_running: 2, priority: 5}", "5", 0},
		{"{max_running: 4, max_running_per_key: 1, key: '{{ trigger.payload.pull_request.number }}'}", "", 1},
		{"{max_running_per_key: 1, key: '{{ trigger.payload.pull_request.number }}'}", "", 1},
		{"{max_running: 4, max_running_per_key: 2, waiting: latest, key: '{{ trigger.payload.pull_request.number }}', priority: '1'}", "1", 2},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + tc.policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.NoError(t, err)
			require.Equal(t, tc.priority, wf.GetConcurrency().GetPriority())
			require.Equal(t, tc.perKey, wf.GetConcurrency().GetMaxRunningPerKey())
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			again, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, wf, again)
		})
	}
}

func TestWorkflowConcurrencyRejectsInvalidPriorityAndPerKey(t *testing.T) {
	for _, policy := range []string{
		"{max_running_per_key: 1}", // per-key cap without the key it caps
		"{max_running: 2, max_running_per_key: 0, key: '{{ trigger.kind }}'}", // zero cap
		"{max_running: 2, max_running_per_key: x, key: '{{ trigger.kind }}'}",
		"{max_running: 2, priority: ''}",
		"{max_running: 2, priority: [1]}",
		"{priority: 1}", // priority alone bounds nothing
	} {
		t.Run(policy, func(t *testing.T) {
			_, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.Error(t, err)
		})
	}
}

func TestWorkflowConcurrencyPerMemberRoundTrip(t *testing.T) {
	for _, policy := range []string{"{per_member: 4}", "{per_member: 4, max_running: 12}", "{per_member: 4, waiting: latest}"} {
		t.Run(policy, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte("name: pooled\nconcurrency: " + policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.NoError(t, err)
			require.EqualValues(t, 4, wf.GetConcurrency().GetPerMember())
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			require.Contains(t, string(encoded), "per_member: 4")
			again, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, wf, again)
		})
	}
}

func TestWorkflowConcurrencyCancelRunningRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   reliantv1.WorkflowConcurrencyCancelRunning
	}{
		{"{max_running_per_key: 1, key: '{{ trigger.payload.data.pull_request.number }}', waiting: latest, cancel_running: on_new_head}",
			reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD},
		{"{max_running: 6, max_running_per_key: 1, key: '{{ trigger.payload.data.number }}', cancel_running: on_new_key_event}",
			reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT},
		// never needs no key: it cancels nothing.
		{"{max_running: 2, cancel_running: never}", reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_NEVER},
		{"{max_running: 2}", reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_UNSPECIFIED},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			wf, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + tc.policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.NoError(t, err)
			require.Equal(t, tc.want, wf.GetConcurrency().GetCancelRunning())
			encoded, err := MarshalWorkflow(wf)
			require.NoError(t, err)
			if tc.want == reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_UNSPECIFIED {
				require.NotContains(t, string(encoded), "cancel_running", "unset stays unset")
			}
			again, err := ParseWorkflow(encoded)
			require.NoError(t, err)
			require.Equal(t, wf, again)
		})
	}
}

func TestWorkflowConcurrencyCancelRunningNeedsOneRunPerKey(t *testing.T) {
	for _, policy := range []string{
		"{max_running: 1, cancel_running: on_new_key_event}",                                             // no key
		"{max_running: 1, waiting: latest, key: '{{ trigger.kind }}', cancel_running: on_new_key_event}", // key, but no per-key cap
		"{max_running_per_key: 2, key: '{{ trigger.kind }}', cancel_running: on_new_head}",               // more than one run per key
		"{max_running_per_key: 1, key: '{{ trigger.kind }}', cancel_running: always}",                    // not a value
		"{max_running_per_key: 1, key: '{{ trigger.kind }}', cancel_running: [on_new_head]}",
	} {
		t.Run(policy, func(t *testing.T) {
			_, err := ParseWorkflow([]byte("name: bounded\nconcurrency: " + policy + "\nentry: [a]\nnodes: [{id: a, type: compact}]\n"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "cancel_running")
		})
	}
}
