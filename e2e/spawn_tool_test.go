// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// SPAWN TOOL E2E TEST
//
// Tests that the spawn tool — where an agent LLM returns a tool call to
// "spawn" — creates a child workflow that runs in its own thread.
//
// Flow:
// 1. Parent agent receives user prompt
// 2. LLM returns a spawn tool call with preset + prompt
// 3. Engine detects spawn tool call, creates child thread/workflow
// 4. Child workflow runs (with its own LLM call)
// 5. Parent receives spawn result and produces final response
// ============================================================================

func TestSpawnTool_CreatesChildWorkflow(t *testing.T) {
	t.Parallel()
	h := NewTestHarness(t)
	defer h.Cleanup()

	// ---------------------------------------------------------------
	// Mock LLM setup
	//
	// Call 1 (parent, turn 1): returns a spawn tool call.
	//   The engine intercepts "spawn" tool calls and creates a child
	//   workflow instead of routing through the normal tool executor.
	//
	// Call 2 (child): the spawned child agent calls the LLM once and
	//   gets a plain text response.
	//
	// Call 3 (parent, turn 2): after the spawn completes, the parent
	//   agent gets called again with the spawn result and returns a
	//   final text response.
	// ---------------------------------------------------------------
	h.MockLLM.SetResponseWithToolCall(
		"I'll spawn a researcher to look into this.",
		"spawn",
		map[string]interface{}{
			"preset": "general",
			"prompt": "Research the topic and report back",
			"title":  "Researcher",
		},
	)

	// Child agent response
	h.MockLLM.AddResponse(MockResponse{
		Text: "Child agent: I have completed the research. Here are my findings.",
	})

	// Parent final response (after spawn result)
	h.MockLLM.AddResponse(MockResponse{
		Text: "Based on the research from the spawned agent, here is the summary.",
	})

	// Start the agent workflow with spawn_presets enabled so the spawn
	// tool is available to the LLM.
	chatID := h.StartAgentWorkflowViaGRPC(
		t,
		"Please research this topic for me",
		WithWorkflowParam("spawn_presets", []interface{}{"general", "researcher"}),
	)

	// Wait for the entire workflow (parent + child) to complete.
	h.WaitForWorkflowComplete(t, chatID)

	// Dump diagnostics on failure for debugging.
	t.Cleanup(func() {
		if t.Failed() {
			h.LogWorkflowDiagnostics(t, chatID)
		}
	})

	// ================================================================
	// ASSERTION 1: Parent workflow completed with messages
	// ================================================================
	ctx := context.Background()

	messages := h.GetMessages(t, chatID)
	require.GreaterOrEqual(t, len(messages), 2,
		"parent should have at least 2 messages (user + assistant)")

	// The first message should be the user prompt
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role,
		"first message should be user role")
	AssertTextContentContains(t, h.DB, messages[0].ID, "Please research this topic")

	// ================================================================
	// ASSERTION 2: The assistant message contains a spawn tool call
	// ================================================================
	var spawnAssistantMsg *reliantv1.MessageRole
	for _, msg := range messages {
		if msg.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			blocks := h.GetContentBlocks(t, msg.ID)
			for _, block := range blocks {
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
					if block.ToolName != nil && *block.ToolName == "spawn" {
						role := msg.Role
						spawnAssistantMsg = &role
						t.Logf("Found spawn tool call in assistant message %s", msg.ID)
					}
				}
			}
		}
	}
	require.NotNil(t, spawnAssistantMsg,
		"should have an assistant message with a spawn tool call")

	// ================================================================
	// ASSERTION 3: A child thread was created
	// ================================================================
	threads, err := h.DB.ListThreadsByConversation(ctx, chatID)
	require.NoError(t, err, "failed to list threads")

	t.Logf("Found %d thread(s) for conversation %s:", len(threads), chatID)
	for i, thread := range threads {
		t.Logf("  [%d] ID=%s ParentThreadID=%v", i, thread.ID, thread.ParentThreadID)
	}

	// Should have at least 2 threads: root + spawned child
	require.GreaterOrEqual(t, len(threads), 2,
		"should have at least 2 threads (root + spawned child)")

	// Find the root thread (via workflow)
	workflow, err := h.DB.GetWorkflow(ctx, chatID)
	require.NoError(t, err, "failed to get workflow")
	rootThreadID := workflow.Thread
	t.Logf("Root thread: %s", rootThreadID)

	// Find a child thread (any thread that is not the root)
	var childThread string
	for _, thread := range threads {
		if thread.ID != rootThreadID {
			childThread = thread.ID
			break
		}
	}
	require.NotEmpty(t, childThread, "should have found a child thread distinct from root")
	t.Logf("Child thread: %s", childThread)

	// ================================================================
	// ASSERTION 4: Child thread has messages
	// ================================================================
	childCW, err := h.DB.GetLatestContextWindow(ctx, childThread)
	require.NoError(t, err, "child thread should have a context window")
	require.NotNil(t, childCW, "child thread context window should not be nil")

	childMsgs, err := h.DB.GetMessagesByContextWindow(ctx, childCW.ID, nil)
	require.NoError(t, err, "failed to list child thread messages")
	t.Logf("Child thread has %d message(s)", len(childMsgs))
	require.NotEmpty(t, childMsgs, "child thread should have messages")

	// ================================================================
	// ASSERTION 5: LLM was called multiple times (parent + child)
	// ================================================================
	assert.GreaterOrEqual(t, h.MockLLM.CallCount(), 2,
		"LLM should be called at least twice (parent turn 1 + child)")

	t.Logf("SUCCESS: Spawn tool created child workflow")
	t.Logf("  - Parent messages: %d", len(messages))
	t.Logf("  - Threads: %d", len(threads))
	t.Logf("  - LLM calls: %d", h.MockLLM.CallCount())
}
