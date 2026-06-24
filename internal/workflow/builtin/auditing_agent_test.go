// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Import activities to trigger init() for schema registration
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
)

// TestAuditingAgentWorkflowInputs tests the input structure for auditing-agent.
func TestAuditingAgentWorkflowInputs(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("auditing-agent.yaml")
	require.NoError(t, err, "Should read auditing-agent.yaml")

	wf, err := v2.ParseWorkflowProtoBytes(data)
	require.NoError(t, err, "Should parse auditing-agent.yaml")

	t.Run("has top-level model input", func(t *testing.T) {
		modelInput, ok := wf.GetInputs()["model"]
		require.True(t, ok, "Should have 'model' input")
		assert.Equal(t, "model", modelInput.GetType())
		modelConfig := modelInput.GetModelInput()
		require.NotNil(t, modelConfig, "Should have ModelInputConfig")
		assert.NotNil(t, modelConfig.GetDefault(), "Model should have a default with tags")
	})

	t.Run("has auditor group input", func(t *testing.T) {
		auditorInput, ok := wf.GetInputs()["auditor"]
		require.True(t, ok, "Should have 'auditor' input")
		assert.Equal(t, "group", auditorInput.GetType())
		groupConfig := auditorInput.GetGroupInput()
		require.NotNil(t, groupConfig, "Should be a GroupInput")
		auditorModel := groupConfig.GetInputs()["model"]
		require.NotNil(t, auditorModel, "Auditor group should have model input")
		assert.Equal(t, "model", auditorModel.GetType())
		auditorPrompt := groupConfig.GetInputs()["system_prompt"]
		require.NotNil(t, auditorPrompt, "Auditor group should have system_prompt input")
		assert.Equal(t, "string", auditorPrompt.GetType())
	})
}

// TestAuditCheckNodeHasModel verifies audit_check references the top-level auditor inputs.
func TestAuditCheckNodeHasModel(t *testing.T) {
	data, err := builtin.BuiltinWorkflowsFS.ReadFile("auditing-agent.yaml")
	require.NoError(t, err)

	wf, err := v2.ParseWorkflowProtoBytes(data)
	require.NoError(t, err)

	// Find the audited_loop node
	var auditedLoopArgs *reliantv1.LoopArgs
	for _, node := range wf.GetNodes() {
		if node.GetId() == "audited_loop" {
			auditedLoopArgs = model.GetLoopArgs(node)
			require.NotNil(t, auditedLoopArgs, "audited_loop should have loop args")
			break
		}
	}
	require.NotNil(t, auditedLoopArgs)
	require.NotNil(t, auditedLoopArgs.GetInline())

	// Find audit_check in the inline workflow
	var auditCheckNode *reliantv1.Node
	for _, node := range auditedLoopArgs.GetInline().GetNodes() {
		if node.GetId() == "audit_check" {
			auditCheckNode = node
			break
		}
	}
	require.NotNil(t, auditCheckNode, "Should find audit_check node")

	t.Run("audit_check is call_llm with model from inputs", func(t *testing.T) {
		assert.Equal(t, "call_llm", auditCheckNode.GetType())
		callLLMArgs := model.GetCallLLMArgs(auditCheckNode)
		require.NotNil(t, callLLMArgs, "audit_check should have CallLLM args")
		assert.NotEmpty(t, callLLMArgs.GetModel().GetExpr(), "model should be a CEL expression")
		assert.NotEmpty(t, callLLMArgs.GetSystemPrompt().GetExpr(), "system_prompt should be a CEL expression")
	})

	t.Run("audit_check has no presets", func(t *testing.T) {
		// CallLLM nodes don't have presets (only workflow/loop nodes do)
		// This is enforced by the type system since CallLLMArgs doesn't have a presets field
	})
}

// TestPresetsOnCallLLMRejected verifies that v2 validation rejects
// presets on call_llm nodes (presets only work on workflow/loop nodes).
func TestPresetsOnCallLLMRejected(t *testing.T) {
	yamlData := []byte(`
name: test-invalid
apiVersion: "0.0.5"
entry: [invalid_node]
nodes:
  - id: invalid_node
    type: call_llm
    presets:
      default: some_preset
    args:
      messages:
        - role: user
          content: "test"
`)

	_, err := v2.ParseWorkflowProtoBytes(yamlData)
	require.Error(t, err, "Should reject presets on call_llm node")
	assert.Contains(t, err.Error(), "unknown key \"presets\"")
}
