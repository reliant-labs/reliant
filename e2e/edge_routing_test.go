// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdgeRouting_ConditionalBranching exercises CEL-based conditional edge routing.
// A workflow with a call_llm node branches to different save_message nodes based on
// whether the LLM response includes tool calls or not.
func TestEdgeRouting_ConditionalBranching(t *testing.T) {
	t.Parallel()
	// Workflow: step_a (call_llm) -> branch based on tool_calls presence
	//   - If tool_calls present  -> branch_tool_use (save_message "Took the TOOL_USE branch")
	//   - If no tool_calls       -> branch_end_turn (save_message "Took the END_TURN branch")
	workflowYAML := `
name: edge_routing_test
entry: [step_a]

nodes:
  - id: step_a
    type: call_llm
    args:
      model:
        id: mock
      system_prompt: "You are a test assistant"

  - id: branch_end_turn
    type: save_message
    role: assistant
    content: "Took the END_TURN branch"

  - id: branch_tool_use
    type: save_message
    role: assistant
    content: "Took the TOOL_USE branch"

edges:
  - from: step_a
    cases:
      - to: branch_tool_use
        condition: nodes.step_a.tool_calls != null && size(nodes.step_a.tool_calls) > 0
        label: has_tool_calls
    default: branch_end_turn
`

	t.Run("takes_end_turn_branch", func(t *testing.T) {
		h := NewTestHarness(t)
		defer h.Cleanup()

		// MockLLM returns a plain text response (no tool calls) -> finish_reason=end_turn
		h.MockLLM.SetResponse("Simple text response from LLM")

		h.WriteWorkflowFile(t, "edge_routing_test.yaml", workflowYAML)

		chatID := h.StartWorkflowViaGRPC(t, "edge_routing_test", map[string]interface{}{}, "trigger edge routing")

		t.Cleanup(func() {
			if t.Failed() {
				h.LogWorkflowDiagnostics(t, chatID)
			}
		})

		h.WaitForWorkflowComplete(t, chatID)

		messages := h.GetMessages(t, chatID)
		require.NotEmpty(t, messages, "should have at least one message")

		// Find assistant messages and check that the END_TURN branch was taken
		var foundEndTurn, foundToolUse bool
		for _, msg := range messages {
			if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				continue
			}
			blocks := h.GetContentBlocks(t, msg.ID)
			for _, block := range blocks {
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
					if strings.Contains(*block.Content, "Took the END_TURN branch") {
						foundEndTurn = true
					}
					if strings.Contains(*block.Content, "Took the TOOL_USE branch") {
						foundToolUse = true
					}
				}
			}
		}

		assert.True(t, foundEndTurn, "expected END_TURN branch message to be saved")
		assert.False(t, foundToolUse, "expected TOOL_USE branch NOT to be taken")
	})

	t.Run("takes_tool_use_branch", func(t *testing.T) {
		h := NewTestHarness(t)
		defer h.Cleanup()

		// MockLLM returns a response with tool calls -> finish_reason=tool_use
		h.MockLLM.SetResponseWithToolCalls("I need to run a tool", MockToolCall{
			Name:  "Bash",
			Input: map[string]interface{}{"command": "echo test"},
		})

		// Mock tool execution so the workflow doesn't fail on execute_tools
		h.MockTools.On("Bash", MockToolResponse{
			Result:  "test\n",
			Success: true,
		})

		h.WriteWorkflowFile(t, "edge_routing_test.yaml", workflowYAML)

		chatID := h.StartWorkflowViaGRPC(t, "edge_routing_test", map[string]interface{}{}, "trigger edge routing with tools")

		t.Cleanup(func() {
			if t.Failed() {
				h.LogWorkflowDiagnostics(t, chatID)
			}
		})

		h.WaitForWorkflowComplete(t, chatID)

		messages := h.GetMessages(t, chatID)
		require.NotEmpty(t, messages, "should have at least one message")

		// Find assistant messages and check that the TOOL_USE branch was taken
		var foundEndTurn, foundToolUse bool
		for _, msg := range messages {
			if msg.Role != reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				continue
			}
			blocks := h.GetContentBlocks(t, msg.ID)
			for _, block := range blocks {
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT && block.Content != nil {
					if strings.Contains(*block.Content, "Took the END_TURN branch") {
						foundEndTurn = true
					}
					if strings.Contains(*block.Content, "Took the TOOL_USE branch") {
						foundToolUse = true
					}
				}
			}
		}

		assert.True(t, foundToolUse, "expected TOOL_USE branch message to be saved")
		assert.False(t, foundEndTurn, "expected END_TURN branch NOT to be taken")
	})
}
