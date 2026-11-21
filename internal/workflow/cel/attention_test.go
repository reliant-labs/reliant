// Copyright (c) 2025 Reliant Labs
package wfcel

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestResolveCELFieldsPreservesRawAttentionPolicy(t *testing.T) {
	original := &reliantv1.Workflow{Attention: &reliantv1.WorkflowAttention{
		Key: "repo:{{ inputs.repo }}", OnStart: "recovering", RecoveryTimeout: "30m", OnSuccess: "resolved",
	}}
	// Any evaluation here fails: the launch runtime must evaluate the key
	// exactly once, not generic CEL resolution followed by another evaluation.
	resolved, err := ResolveCELFields(original, newMockEvaluator(nil))
	require.NoError(t, err)
	require.True(t, proto.Equal(original, resolved))
	require.NotSame(t, original.GetAttention(), resolved.(*reliantv1.Workflow).GetAttention())
}
