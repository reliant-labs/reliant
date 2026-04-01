// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// FORK MODE SUB-WORKFLOW CONTEXT INHERITANCE TEST
//
// This test verifies that sub-workflows defined with `thread.mode: fork`
// correctly inherit messages from the parent thread.
//
// The bug: When a sub-workflow is defined with `thread.mode: fork`, it should
// inherit messages from the parent thread, but in chat 95f53804-9fb8-4c25-bbdf-f1ccc9a1c451,
// the forked thread did NOT include the original user message.
//
// This test exercises the REAL code path:
// - Real workflow engine
// - Real database
// - Real thread service
// - Real context window resolution
//
// Test flow:
// 1. Create a workflow with a sub-workflow that has `thread.mode: fork`
// 2. Start the workflow with a user message
// 3. Verify the parent thread has the user message
// 4. Verify the forked sub-workflow thread includes the parent user message
// ============================================================================

// TestForkSubWorkflow_InheritsParentContext tests that fork mode sub-workflows
// inherit messages from the parent thread.
//
// This test uses the builtin one-ring workflow which has fork mode sub-workflows.
// This test should FAIL with the current code if the bug exists.
func TestForkSubWorkflow_InheritsParentContext(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock LLM to return responses for the one-ring steps
	// We need enough responses for: research, plan
	h.MockLLM.SetResponses(
		"Research findings: Context gathered",
		"Plan created: Step by step approach",
	)

	// Start workflow via gRPC with a user message
	// Use the one-ring workflow which has fork mode sub-workflows
	userMessage := "Build a hello world feature. This is the parent user message that should be inherited by fork mode."
	chatID := h.StartWorkflowViaGRPC(t, "builtin://one-ring", map[string]interface{}{
		// Note: Must use []interface{} not []string - structpb.NewValue doesn't support []string
		"steps": []interface{}{"research", "plan"}, // Only run research and plan (both use fork mode)
		"model": map[string]interface{}{"id": "mock"},
		"yield": false, // Don't yield between steps - let it run to completion
	}, userMessage)

	// Wait for workflow to complete
	h.WaitForWorkflowComplete(t, chatID)

	// Get workflow history for debugging
	history := h.GetWorkflowHistory(t, chatID)
	history.PrintActivities()
	t.Logf("\n=== Workflow execution completed ===")

	// ========================================================================
	// ASSERTIONS
	// ========================================================================

	ctx := context.Background()

	// Get the chat to find the root thread
	_, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err, "failed to get chat")

	// Get workflow to find root thread
	workflow, err := h.DB.GetWorkflow(ctx, chatID)
	require.NoError(t, err, "failed to get workflow")
	require.NotEmpty(t, workflow.Thread, "workflow should have a thread")

	rootThreadID := workflow.Thread
	t.Logf("Root thread ID: %s", rootThreadID)

	// Get messages in the root thread
	_, err = h.DB.GetThread(ctx, rootThreadID)
	require.NoError(t, err, "failed to get root thread")

	// Get root thread's context window
	rootCW, err := h.DB.GetLatestContextWindow(ctx, rootThreadID)
	require.NoError(t, err, "failed to get root context window")

	// Resolve messages in root thread
	threadSvc := threads.NewService(h.DB)
	rootMessages, err := threadSvc.ResolveMessagesFromCW(ctx, rootCW.ID)
	require.NoError(t, err, "failed to resolve root messages")

	// Assert: Root thread should have the user message
	require.NotEmpty(t, rootMessages, "root thread should have messages")

	var rootUserMsg *db.Message
	for _, msg := range rootMessages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
			rootUserMsg = msg
			break
		}
	}
	require.NotNil(t, rootUserMsg, "root thread should have a user message")

	// Get the user message content
	rootUserContent := h.GetMessageText(t, rootUserMsg.ID)
	require.Contains(t, rootUserContent, "parent user message",
		"root thread user message should contain expected text")

	t.Logf("✓ Root thread has user message (ordinal=%d): %q", rootUserMsg.Ordinal, rootUserContent)

	// Find the forked sub-workflow thread
	// The sub-workflow should have created a child thread with fork metadata
	threads, err := h.DB.ListThreadsByConversation(ctx, chatID)
	require.NoError(t, err, "failed to list threads")
	t.Logf("Found %d thread(s):", len(threads))
	for i, thread := range threads {
		t.Logf("  [%d] ID=%s, ParentThreadID=%v, ForkAtOrdinal=%v",
			i, thread.ID, thread.ParentThreadID, thread.ForkAtOrdinal)
	}
	require.GreaterOrEqual(t, len(threads), 2, "should have at least 2 threads (root + forked)")

	var forkedThread *db.Thread
	for _, thread := range threads {
		if thread.ID != rootThreadID && thread.ParentThreadID != nil && *thread.ParentThreadID == rootThreadID {
			forkedThread = thread
			break
		}
	}
	require.NotNil(t, forkedThread, "should have found a forked child thread")

	t.Logf("✓ Found forked thread ID: %s", forkedThread.ID)
	t.Logf("  - ParentThreadID: %v", *forkedThread.ParentThreadID)
	t.Logf("  - ForkAtOrdinal: %v", *forkedThread.ForkAtOrdinal)
	t.Logf("  - ForkAtContextWindowID: %v", *forkedThread.ForkAtContextWindowID)

	// Get forked thread's context window
	forkedCW, err := h.DB.GetLatestContextWindow(ctx, forkedThread.ID)
	require.NoError(t, err, "failed to get forked context window")

	t.Logf("✓ Forked thread context window:")
	t.Logf("  - ID: %s", forkedCW.ID)
	t.Logf("  - ParentContextWindowID: %v", forkedCW.ParentContextWindowID)
	t.Logf("  - ForkAtOrdinal: %v", forkedCW.ForkAtOrdinal)

	// Resolve messages in forked thread
	// THIS IS THE CRITICAL TEST: The forked thread should include parent messages
	forkedMessages, err := threadSvc.ResolveMessagesFromCW(ctx, forkedCW.ID)
	require.NoError(t, err, "failed to resolve forked thread messages")

	t.Logf("✓ Forked thread has %d messages (including inherited)", len(forkedMessages))
	for i, msg := range forkedMessages {
		content := h.GetMessageText(t, msg.ID)
		t.Logf("  [%d] Role=%v, Ordinal=%d, ContextWindowID=%s, Content=%q",
			i, msg.Role, msg.Ordinal, msg.ContextWindowID, truncate(content, 60))
	}

	// Assert: Forked thread should include the parent user message
	var inheritedUserMsg *db.Message
	for _, msg := range forkedMessages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER && msg.ContextWindowID == rootCW.ID {
			inheritedUserMsg = msg
			break
		}
	}

	// THIS ASSERTION SHOULD FAIL IF THE BUG EXISTS
	require.NotNil(t, inheritedUserMsg,
		"CRITICAL: forked thread should inherit the parent user message from root context window %s", rootCW.ID)

	// Verify the inherited message is the same as the root message
	require.Equal(t, rootUserMsg.ID, inheritedUserMsg.ID,
		"inherited message should be the same message object as in root")
	require.Equal(t, rootUserMsg.Ordinal, inheritedUserMsg.Ordinal,
		"inherited message should have the same ordinal")

	inheritedContent := h.GetMessageText(t, inheritedUserMsg.ID)
	require.Contains(t, inheritedContent, "parent user message",
		"inherited user message should contain expected text")

	t.Logf("✓ SUCCESS: Forked thread correctly inherited parent user message (ordinal=%d)", inheritedUserMsg.Ordinal)
}

// TestForkSubWorkflow_MultiLevel tests fork inheritance through multiple levels
// This tests A → B (fork) → C (fork) to ensure the chain works correctly
func TestForkSubWorkflow_MultiLevel(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// First create the level2 workflow that will be referenced
	level2WorkflowYAML := `
name: level2_fork_workflow
description: Level 2 workflow with fork mode for deeper nesting
entry: [nested_agent]
nodes:
  - id: nested_agent
    type: workflow
    ref: builtin://agent
    thread:
      mode: fork
      inject:
        role: user
        content: "Level 2 instruction"
`

	// Create the main workflow that references level2
	workflowYAML := `
name: multi_fork_test
description: Test workflow with multiple levels of fork mode sub-workflows
entry: [level1]
nodes:
  - id: level1
    type: workflow
    ref: level2_fork_workflow
    thread:
      mode: fork
      inject:
        role: user
        content: "Level 1 instruction"
`

	h.MockLLM.SetResponse("Response from deeply nested fork")
	h.WriteWorkflowFile(t, "level2_fork_workflow.yaml", level2WorkflowYAML)
	h.WriteWorkflowFile(t, "multi_fork_test.yaml", workflowYAML)

	userMessage := "Root message that should propagate through all forks"
	chatID := h.StartWorkflowViaGRPC(t, "multi_fork_test", map[string]interface{}{}, userMessage)
	h.WaitForWorkflowComplete(t, chatID)

	ctx := context.Background()
	_, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err)

	// Get all threads
	allThreads, err := h.DB.ListThreadsByConversation(ctx, chatID)
	require.NoError(t, err)
	t.Logf("Found %d threads total", len(allThreads))

	threadSvc := threads.NewService(h.DB)

	// Each forked thread should inherit the root user message
	for _, thread := range allThreads {
		cw, err := h.DB.GetLatestContextWindow(ctx, thread.ID)
		if err != nil {
			continue // Skip threads without messages
		}

		msgs, err := threadSvc.ResolveMessagesFromCW(ctx, cw.ID)
		require.NoError(t, err, "failed to resolve messages for thread %s", thread.ID)

		// Look for root user message
		hasRootMsg := false
		for _, msg := range msgs {
			if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_USER {
				content := h.GetMessageText(t, msg.ID)
				if contains(content, "Root message") {
					hasRootMsg = true
					break
				}
			}
		}

		if thread.ParentThreadID != nil {
			require.True(t, hasRootMsg,
				"forked thread %s should inherit root user message", thread.ID)
			t.Logf("✓ Thread %s correctly inherited root message", thread.ID)
		}
	}
}

// TestForkSubWorkflow_ForkAtOrdinal tests that fork respects the ordinal boundary
func TestForkSubWorkflow_ForkAtOrdinal(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// This workflow will create messages with different ordinals
	// and then fork. The fork should only inherit up to the fork point.
	workflowYAML := `
name: fork_ordinal_test
description: Test that fork mode respects ordinal boundaries
entry: [first_agent]
nodes:
  - id: first_agent
    type: workflow
    ref: builtin://agent
    thread:
      mode: inherit

  - id: second_agent
    type: workflow
    ref: builtin://agent
    thread:
      mode: fork
      inject:
        role: user
        content: "Forked agent instruction"

edges:
  - from: first_agent
    default: second_agent
`

	h.MockLLM.SetResponses(
		"First agent response",
		"Second agent response after fork",
	)
	h.WriteWorkflowFile(t, "fork_ordinal_test.yaml", workflowYAML)

	userMessage := "Original user message before fork"
	chatID := h.StartWorkflowViaGRPC(t, "fork_ordinal_test", map[string]interface{}{}, userMessage)

	// Give workflow more time to execute - fork workflows are more complex
	time.Sleep(5 * time.Second)

	// Debug: Print workflow activities before waiting
	history := h.GetWorkflowHistory(t, chatID)
	history.PrintActivities()

	h.WaitForWorkflowComplete(t, chatID)

	ctx := context.Background()
	_, err := h.DB.GetChat(ctx, chatID)
	require.NoError(t, err)

	// Get workflow to find root thread
	workflow, err := h.DB.GetWorkflow(ctx, chatID)
	require.NoError(t, err)
	require.NotEmpty(t, workflow.Thread, "workflow should have a thread")

	rootThreadID := workflow.Thread
	threadSvc := threads.NewService(h.DB)

	// Get root thread messages
	rootCW, err := h.DB.GetLatestContextWindow(ctx, rootThreadID)
	require.NoError(t, err)
	rootMessages, err := threadSvc.ResolveMessagesFromCW(ctx, rootCW.ID)
	require.NoError(t, err)

	t.Logf("Root thread has %d messages", len(rootMessages))

	// Find forked thread
	allThreads, err := h.DB.ListThreadsByConversation(ctx, chatID)
	require.NoError(t, err)

	var forkedThread *db.Thread
	for _, thread := range allThreads {
		if thread.ID != rootThreadID && thread.ParentThreadID != nil {
			forkedThread = thread
			break
		}
	}
	require.NotNil(t, forkedThread, "should have forked thread")

	// Get forked thread messages
	forkedCW, err := h.DB.GetLatestContextWindow(ctx, forkedThread.ID)
	require.NoError(t, err)
	forkedMessages, err := threadSvc.ResolveMessagesFromCW(ctx, forkedCW.ID)
	require.NoError(t, err)

	t.Logf("Forked thread has %d messages (including inherited)", len(forkedMessages))
	t.Logf("Fork at ordinal: %d", *forkedThread.ForkAtOrdinal)

	// Verify forked thread includes messages up to ForkAtOrdinal
	for _, msg := range forkedMessages {
		if msg.ContextWindowID == rootCW.ID {
			// This message was inherited from parent
			require.LessOrEqual(t, msg.Ordinal, *forkedThread.ForkAtOrdinal,
				"inherited message ordinal %d should be <= fork ordinal %d",
				msg.Ordinal, *forkedThread.ForkAtOrdinal)
		}
	}

	t.Logf("✓ Fork correctly respects ordinal boundary")
}

// ============================================================================
// HELPERS
// ============================================================================

// truncate truncates a string to maxLen characters
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(substr) == 0 ||
			findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
