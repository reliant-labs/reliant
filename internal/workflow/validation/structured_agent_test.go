package validation

import (
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStructuredAgentV3Validation tests that the v3 structured-agent validates correctly
func TestStructuredAgentV3Validation(t *testing.T) {
	t.Parallel()
	// Load the structured-agent workflow
	data, err := os.ReadFile("../builtin/structured-agent.yaml")
	require.NoError(t, err, "should read structured-agent.yaml")

	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err, "should parse structured-agent workflow")

	// Validate it
	result := StaticAnalysis(wf, nil)

	// Check for the specific error about missing outputs
	hasOutputsError := false
	for _, e := range result.Errors() {
		t.Logf("Validation error: %s - %s", e.Path, e.Message)
		if e.Message == "loop condition references 'outputs.*' but inline workflow has no 'outputs' section" {
			hasOutputsError = true
		}
	}

	assert.False(t, hasOutputsError, "should NOT have error about missing outputs section - the inline workflow DOES have outputs")

	// Verify the workflow has the outputs section by finding the agent_loop node
	loopNode := model.FindNode(wf, "agent_loop")
	require.NotNil(t, loopNode, "should find agent_loop node")

	loopArgs := loopNode.GetLoop()
	require.NotNil(t, loopArgs, "agent_loop should be a loop node")

	inline := loopArgs.GetInline()
	require.NotNil(t, inline, "agent_loop should have inline workflow")
	assert.NotEmpty(t, inline.GetOutputs(), "inline workflow should have outputs section")

	t.Logf("✓ Inline workflow has %d outputs", len(inline.GetOutputs()))
	for k := range inline.GetOutputs() {
		t.Logf("  - %s", k)
	}
}
