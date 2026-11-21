// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolvePreservesAttention(t *testing.T) {
	resolved, err := Resolve("observer", Sources{User: func(string) ([]byte, error) {
		return []byte("name: observer\nattention: {key: 'pr:{{ inputs.pr }}', on_start: recovering, recovery_timeout: 30m, on_success: resolved}\nentry: [a]\nnodes: [{id: a, type: compact}]\n"), nil
	}})
	require.NoError(t, err)
	require.Equal(t, "pr:{{ inputs.pr }}", resolved.Workflow.GetAttention().GetKey())
	require.Equal(t, "recovering", resolved.Workflow.GetAttention().GetOnStart())
	require.Equal(t, "30m", resolved.Workflow.GetAttention().GetRecoveryTimeout())
	require.Equal(t, "resolved", resolved.Workflow.GetAttention().GetOnSuccess())
}
