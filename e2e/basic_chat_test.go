// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

// TestBasicChat_ThreadNamespace tests that thread.id is accessible in CEL.
// This specifically tests the fix for the "undeclared reference to 'thread'" error.
// If this test fails, there's a CEL namespace configuration issue.
func TestBasicChat_ThreadNamespace(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// This test passes if the workflow starts without CEL errors.
	// The agent.yaml workflow uses {{thread.id}} in multiple places.
	h.MockLLM.SetResponse("Thread namespace works!")

	// Use gRPC helper to create project and chat via production code paths
	chatID := h.StartAgentWorkflowViaGRPC(t, "Test thread namespace")

	// If we get here without errors, thread.id was resolved correctly
	// Wait for at least 2 messages (user + assistant)
	messages := h.WaitForMessages(t, chatID, 2)
	require.GreaterOrEqual(t, len(messages), 2, "should have at least user + assistant messages")

	// Verify we have both user and assistant roles
	hasUser := false
	hasAssistant := false
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			hasUser = true
		}
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			hasAssistant = true
		}
	}
	require.True(t, hasUser, "should have user message")
	require.True(t, hasAssistant, "should have assistant message")

	t.Logf("✓ Thread namespace accessible: workflow executed without CEL errors (%d messages)", len(messages))
}

// TestBasicChat_WorkflowCompletes tests that the workflow completes successfully.
// This is a basic smoke test for the agent workflow.
func TestBasicChat_WorkflowCompletes(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Setup mock LLM with no tool calls (should complete after single response)
	h.MockLLM.SetResponse("Task completed successfully.")

	// Use gRPC helper to create project and chat via production code paths
	chatID := h.StartAgentWorkflowViaGRPC(t, "Complete this task")

	// Wait for workflow to complete
	h.WaitForWorkflowComplete(t, chatID)

	// Verify messages were saved
	messages := h.GetMessages(t, chatID)
	require.GreaterOrEqual(t, len(messages), 2, "should have at least user + assistant messages")

	t.Logf("✓ Workflow completed successfully with %d messages", len(messages))
}
