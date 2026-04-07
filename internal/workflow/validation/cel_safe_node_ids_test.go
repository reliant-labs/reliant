package validation

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticAnalysis_RejectsHyphenatedNodeIDs(t *testing.T) {
	workflowYAML := `
name: test_invalid_node_ids
entry: [router_1]
nodes:
  - id: router-1
    type: run
    command: "echo hi"
  - id: router_1
    type: run
    command: "echo ok"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := StaticAnalysis(wf, nil)
	require.True(t, result.HasErrors(), "expected structural validation to fail")

	errors := result.Errors()
	require.NotEmpty(t, errors)
	assert.Contains(t, result.Error(), "invalid node ID 'router-1': must start with a letter and contain only letters, digits, or underscores")
}
