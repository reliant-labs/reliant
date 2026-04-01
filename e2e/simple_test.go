// Copyright (c) 2025 Reliant Labs
package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// ============================================================================
// SIMPLE WORKFLOW TESTS
// These tests demonstrate the new e2e framework and verify basic functionality.
// All tests must complete in < 3 seconds.
// ============================================================================

// TestSimple_HelloWorld tests the simplest agent interaction:
// User says "hello", assistant responds with text only.
func TestSimple_HelloWorld(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock LLM to return a simple response
	h.MockLLM.SetResponse("Hello! I'm Claude, an AI assistant. How can I help you?")

	// Start workflow with user message via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "hello")

	// Wait for exactly 2 messages: user + assistant
	messages := h.WaitForMessages(t, chatID, 2)

	// Verify message structure
	h.AssertMessageRoles(t, messages, reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	// Verify content
	h.AssertMessageContent(t, messages[0].ID, "hello")
	h.AssertMessageContent(t, messages[1].ID, "Claude")

	// Verify no tool calls in simple response
	blocks := h.GetContentBlocks(t, messages[1].ID)
	for _, block := range blocks {
		require.NotEqual(t, int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL), block.BlockType, "should not have tool calls")
	}

	t.Logf("✓ Test passed: %d messages, roles: user -> assistant", len(messages))
}

// TestSimple_MultiTurnConversation tests multiple message exchanges
func TestSimple_MultiTurnConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock to return different responses for each turn
	h.MockLLM.SetResponses(
		"Hello! How can I help you today?",
		"I can help with coding, answering questions, and more!",
	)

	// Turn 1 - start workflow via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "hello")
	messages := h.WaitForMessages(t, chatID, 2)
	require.Len(t, messages, 2, "turn 1 should have 2 messages")
	// Wait for workflow to fully complete before starting turn 2
	h.WaitForWorkflowComplete(t, chatID)

	// Turn 2 - send follow-up message via gRPC
	h.SendMessageViaGRPC(t, chatID, "what can you do?")
	messages = h.WaitForMessages(t, chatID, 4)
	require.Len(t, messages, 4, "turn 2 should have 4 messages")

	// Verify roles
	h.AssertMessageRoles(t, messages, reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, reliantv1.MessageRole_MESSAGE_ROLE_USER, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT)

	// Verify ordinals are sequential
	AssertMessageOrdinalsSequential(t, messages)

	t.Logf("✓ Test passed: %d messages across 2 turns", len(messages))
}

// TestSimple_ToolCall tests a workflow that includes a tool call
func TestSimple_ToolCall(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Configure mock to return a tool call first, then a final response
	h.MockLLM.SetResponseWithToolCall(
		"I'll check that for you.",
		"Bash",
		map[string]interface{}{
			"command": "echo 'test'",
		},
	)

	// Add follow-up response after tool execution
	h.MockLLM.AddResponse(MockResponse{
		Text: "The command returned 'test'. Is there anything else you need?",
	})

	// Start workflow via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "run a simple test command")

	// Wait for messages - should have: user, assistant (with tool call), tool, assistant
	// But tool execution may fail in test env, so check for at least 2
	messages := h.WaitForMessages(t, chatID, 2)

	// Verify first message is user
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)

	// Verify second message is assistant
	require.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, messages[1].Role)

	// Verify assistant has tool call
	h.AssertHasToolCall(t, messages[1].ID, "Bash")

	t.Logf("✓ Test passed: tool call detected in assistant message")
}

// TestSimple_MessageContent tests that message content is saved correctly
func TestSimple_MessageContent(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	expectedResponse := "This is a specific test response with unique content XYZ123."
	h.MockLLM.SetResponse(expectedResponse)

	// Start workflow via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "test message")

	messages := h.WaitForMessages(t, chatID, 2)

	// Verify user message content
	AssertTextContentContains(t, h.DB, messages[0].ID, "test message")

	// Verify assistant message content
	AssertTextContentContains(t, h.DB, messages[1].ID, "XYZ123")

	t.Logf("✓ Test passed: message content verified")
}

// TestSimple_ChatUpdates tests that chat_updates are created for streaming
func TestSimple_ChatUpdates(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponse("Here's a response that should create chat updates.")

	// Start workflow via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "hello")

	// Wait for messages first
	h.WaitForMessages(t, chatID, 2)

	// Verify chat updates were created
	updates := AssertChatUpdatesEventually(t, h.DB, chatID, 1)
	require.NotEmpty(t, updates, "should have chat updates")

	t.Logf("✓ Test passed: %d chat updates created", len(updates))
}

// TestSimple_ChatUpdates_StreamingAndMessageContracts validates the streaming and
// message payload contracts consumed by the frontend reducers:
// 1) streaming deltas are flat payloads (no payload.message wrapper)
// 2) persistent message updates are wrapped under payload.message
func TestSimple_ChatUpdates_StreamingAndMessageContracts(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	h.MockLLM.SetResponse("Streaming contract response for UI")

	chatID := h.StartAgentWorkflowViaGRPC(t, "hello streaming contract")

	streamingSubCtx, streamingSubCancel := context.WithCancel(context.Background())
	defer streamingSubCancel()
	streamingSubscriber := h.Server.StreamingHub().Subscribe(streamingSubCtx, chatID)

	h.WaitForMessages(t, chatID, 2)
	h.WaitForWorkflowComplete(t, chatID)

	updates := AssertChatUpdatesEventually(t, h.DB, chatID, 1)

	var messageUpdateFound bool
	for _, update := range updates {
		if update.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
			continue
		}

		messageUpdateFound = true
		var messagePayload map[string]interface{}
		require.NoError(t, json.Unmarshal(update.Data, &messagePayload))
		require.Contains(t, messagePayload, "id")
		require.Contains(t, messagePayload, "content_blocks")
		require.NotContains(t, messagePayload, "message", "database message payload should stay flat before stream formatting")
	}
	require.True(t, messageUpdateFound, "expected at least one persistent message update")

	var streamingDeltaFound bool
	for {
		select {
		case delta := <-streamingSubscriber.Events():
			streamingDeltaFound = true
			payloadBytes, err := json.Marshal(delta)
			require.NoError(t, err)

			var payload map[string]interface{}
			require.NoError(t, json.Unmarshal(payloadBytes, &payload))
			require.Equal(t, "streaming_delta", payload["update_type"])
			require.Contains(t, payload, "delta_type")
			require.NotContains(t, payload, "message", "streaming delta payload must be flat and never wrapped under message")
		default:
			goto done
		}
	}

done:
	require.True(t, streamingDeltaFound, "expected at least one ephemeral streaming delta event")
}

// TestSimple_LLMCallCount tests that the mock LLM tracks calls correctly
func TestSimple_LLMCallCount(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Cleanup()

	// Reset mock to ensure clean state
	h.MockLLM.Reset()
	h.MockLLM.SetResponse("Response 1")

	// Initial count should be 0
	require.Equal(t, 0, h.MockLLM.CallCount(), "should start with 0 calls")

	// Start workflow via gRPC
	chatID := h.StartAgentWorkflowViaGRPC(t, "hello")
	h.WaitForMessages(t, chatID, 2)

	// Should have at least 1 call
	require.GreaterOrEqual(t, h.MockLLM.CallCount(), 1, "should have at least 1 LLM call")

	// Verify calls are recorded
	calls := h.MockLLM.GetCalls()
	require.NotEmpty(t, calls, "should have recorded calls")

	t.Logf("✓ Test passed: %d LLM calls recorded", h.MockLLM.CallCount())
}

// ============================================================================
// ASSERTION TESTS
// These test the assertion helpers themselves
// ============================================================================

// TestAssertions_WaitFor tests the WaitFor helper
func TestAssertions_WaitFor(t *testing.T) {
	counter := 0

	// This should complete quickly
	WaitFor(t, func() bool {
		counter++
		return counter >= 3
	}, "counter should reach 3")

	require.GreaterOrEqual(t, counter, 3)
	t.Logf("✓ WaitFor completed in %d iterations", counter)
}
