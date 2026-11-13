// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const attentionWorkflow = "name: incident\nentry: [a]\nnodes: [{id: a, type: compact}]\n"

func TestAttentionRoundTrip(t *testing.T) {
	for _, policy := range []string{
		"{key: observer}",
		"{key: 'repo:{{ inputs.repo }}:pr:{{ inputs.pr }}', on_start: recovering, recovery_timeout: 30m, on_success: resolved, on_failure: action_required}",
		"{key: 'true', on_start: action_required, on_success: action_required}",
	} {
		wf, err := ParseWorkflow([]byte(attentionWorkflow + "attention: " + policy + "\n"))
		require.NoError(t, err)
		encoded, err := MarshalWorkflow(wf)
		require.NoError(t, err)
		got, err := ParseWorkflow(encoded)
		require.NoError(t, err)
		require.True(t, proto.Equal(wf, got), "%s", encoded)
	}
}

func TestAttentionAbsentCompatibility(t *testing.T) {
	require.True(t, IsRuntimeEvaluatedTopLevelKey("attention"))
	wf, err := ParseWorkflow([]byte(attentionWorkflow))
	require.NoError(t, err)
	require.Nil(t, wf.GetAttention())
	encoded, err := MarshalWorkflow(wf)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "attention")
}

func TestAttentionRejectsMalformedYAML(t *testing.T) {
	for _, policy := range []string{
		"null", "false", "text", "[]", "{}", "{key: null}", "{key: true}", "{key: 123}", "{key: []}",
		"{key: ''}", "{key: '   '}", "{key: a, unknown: b}", "{key: a, key: b}", "{key: a, on_start: ''}",
		"{key: a, on_start: null}", "{key: a, on_start: resolved}", "{key: a, on_failure: resolved}",
		"{key: a, on_success: recovering}", "{key: a, on_start: recovering}",
		"{key: a, on_start: recovering, recovery_timeout: 25h}", "{key: a, recovery_timeout: 1m}",
		"{key: '{{ inputs.key'}", "{key: '{{ nodes.a }}'}", "{key: '{{ 123 }}'}",
		"{key: a, on_success: resolved, on_success: resolved}", "{1: a}",
	} {
		t.Run(policy, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(attentionWorkflow + "attention: " + policy + "\n"))
			require.Error(t, err)
		})
	}
	_, err := ParseWorkflow([]byte(attentionWorkflow + "attention: {key: a}\nattention: {key: b}\n"))
	require.Error(t, err)
}

func TestAttentionMarshalRejectsInvalidProto(t *testing.T) {
	_, err := MarshalWorkflow(&reliantv1.Workflow{Attention: &reliantv1.WorkflowAttention{Key: "key", OnFailure: "resolved"}})
	require.ErrorContains(t, err, "attention.on_failure")
}
