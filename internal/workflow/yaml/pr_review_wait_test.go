// Copyright (c) 2025 Reliant Labs
package wfyaml_test

import (
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAwaitPRReviewTypedArguments(t *testing.T) {
	workflow, err := wfyaml.ParseWorkflow([]byte(`name: author-review
entry: wait
nodes:
  - id: wait
    type: await_pr_review
    repo: org/repo
    pr: 5
    expected_head: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`))
	require.NoError(t, err)
	require.NotNil(t, workflow.Nodes[0].GetAwaitPrReview())
	require.Equal(t, int32(29), int32(workflow.Nodes[0].ProtoReflect().Descriptor().Fields().ByName("await_pr_review").Number()))
}
