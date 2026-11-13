// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestAttentionDirectProtoAdmission(t *testing.T) {
	for _, policy := range []*reliantv1.WorkflowAttention{
		{}, {Key: "key", OnFailure: "resolved"}, {Key: "key", OnStart: "recovering"},
		{Key: "{{ nodes.a }}"}, {Key: "{{ inputs.key"},
	} {
		result := StaticAnalysis(&reliantv1.Workflow{Name: "observer", Attention: policy}, nil)
		found := false
		for _, issue := range result.Errors() {
			if issue.Field == "attention" {
				found = true
			}
		}
		require.True(t, found, "invalid direct protobuf must fail attention validation: %v", policy)
	}
}

func TestAttentionValidAndAbsent(t *testing.T) {
	for _, policy := range []*reliantv1.WorkflowAttention{nil, {Key: "observer"}, {Key: "{{ inputs.key }}", OnStart: "recovering", RecoveryTimeout: "30m", OnSuccess: "resolved"}} {
		result := NewResult()
		validateAttentionTree(&reliantv1.Workflow{Attention: policy}, result)
		require.False(t, result.HasErrors(), "%v", result.Errors())
	}
}

func TestAttentionEmbeddedProtoAdmission(t *testing.T) {
	child := &reliantv1.Workflow{Attention: &reliantv1.WorkflowAttention{Key: "key", OnFailure: "resolved"}}
	for _, wf := range []*reliantv1.Workflow{
		{OnFailure: child},
		{Nodes: []*reliantv1.Node{{Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{Inline: child}}}}},
		{Nodes: []*reliantv1.Node{{Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{Inline: child}}}}},
	} {
		result := NewResult()
		validateAttentionTree(wf, result)
		require.True(t, result.HasErrors())
	}
}
