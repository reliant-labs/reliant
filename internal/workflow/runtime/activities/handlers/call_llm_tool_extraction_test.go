// Copyright (c) 2025 Reliant Labs
// Tests for tool call extraction from LLM driver responses.
//
// These tests verify that the handleComplete stream event handler correctly
// extracts structured ToolCall objects from the DriverResponse, ensuring
// tool calls end up as structured objects in CallLLMOutput.ToolCalls rather
// than as raw text in the response content.
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TOOL CALL EXTRACTION FROM DRIVER RESPONSE
// ============================================================================
// These tests verify that processStreamEvent → handleComplete correctly
// extracts tool calls from the DriverResponse into streamProcessingState.

func TestHandleComplete_ExtractsToolCalls(t *testing.T) {
	activity := &CallLLMActivity{}

	t.Run("extracts single tool call from response", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"Let me check that."},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "Let me check that.",
				ToolCalls: []message.ToolCall{
					{
						ID:    "toolu_abc123",
						Name:  "Bash",
						Input: `{"command":"ls -la"}`,
					},
				},
				Usage: llm.TokenUsage{TokenCount: 250},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)

		require.Len(t, state.toolCalls, 1, "Should extract 1 tool call")
		assert.Equal(t, "toolu_abc123", state.toolCalls[0].ID)
		assert.Equal(t, "Bash", state.toolCalls[0].Name)
		assert.Equal(t, `{"command":"ls -la"}`, state.toolCalls[0].Input)
	})

	t.Run("captures upstream correlation ids from driver response", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"Done."},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content:            "Done.",
				Usage:              llm.TokenUsage{TokenCount: 42},
				UpstreamRequestID:  "req_abc",
				UpstreamProxymanID: "flow_123",
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)
		assert.Equal(t, "req_abc", state.upstreamRequestID)
		assert.Equal(t, "flow_123", state.upstreamProxymanID)
	})

	t.Run("captures provider cost from driver response", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"Done."},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "Done.",
				Usage: llm.TokenUsage{
					TokenCount: 42,
					Cost:       0.0123,
				},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)
		assert.Equal(t, 0.0123, state.cost)
	})

	t.Run("extracts multiple tool calls from response", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"I'll check multiple files."},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "I'll check multiple files.",
				ToolCalls: []message.ToolCall{
					{ID: "toolu_1", Name: "View", Input: `{"file_path":"a.go"}`},
					{ID: "toolu_2", Name: "View", Input: `{"file_path":"b.go"}`},
					{ID: "toolu_3", Name: "Grep", Input: `{"pattern":"error"}`},
				},
				Usage: llm.TokenUsage{TokenCount: 400},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)

		require.Len(t, state.toolCalls, 3, "Should extract 3 tool calls")
		assert.Equal(t, "toolu_1", state.toolCalls[0].ID)
		assert.Equal(t, "toolu_2", state.toolCalls[1].ID)
		assert.Equal(t, "toolu_3", state.toolCalls[2].ID)
	})

	t.Run("no tool calls when response has none", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"Hello, how can I help?"},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "Hello, how can I help?",
				Usage:   llm.TokenUsage{TokenCount: 100},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)

		assert.Empty(t, state.toolCalls, "Should have no tool calls")
	})

	t.Run("preserves thought signatures in tool calls", func(t *testing.T) {
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"Let me think about this."},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "Let me think about this.",
				ToolCalls: []message.ToolCall{
					{
						ID:               "toolu_abc",
						Name:             "Bash",
						Input:            `{"command":"echo test"}`,
						ThoughtSignature: "sig_thought_123",
					},
				},
				Usage: llm.TokenUsage{TokenCount: 200},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)

		require.Len(t, state.toolCalls, 1)
		assert.Equal(t, "sig_thought_123", state.toolCalls[0].ThoughtSignature)
	})

	t.Run("overrides streamed text with authoritative response content", func(t *testing.T) {
		// Some drivers stream raw text that includes function call patterns
		// The EventComplete has the clean authoritative text
		state := &streamProcessingState{
			blockStates: NewBlockStreamState(),
			textParts:   []string{"raw streaming text with to=functions.view {json} embedded"},
		}

		event := llm.DriverEvent{
			Type: llm.EventComplete,
			Response: &llm.DriverResponse{
				Content: "Clean authoritative text", // The clean version
				ToolCalls: []message.ToolCall{
					{ID: "toolu_1", Name: "View", Input: `{"file_path":"test.go"}`},
				},
				Usage: llm.TokenUsage{TokenCount: 200},
			},
		}

		err := activity.processStreamEvent(context.TODO(), "chat-1", "thread-0", event, state)
		require.NoError(t, err)

		// Text should be the clean version, not the raw streamed version
		require.Len(t, state.textParts, 1)
		assert.Equal(t, "Clean authoritative text", state.textParts[0],
			"handleComplete should override streamed text with authoritative content")

		// Tool calls should still be extracted
		require.Len(t, state.toolCalls, 1)
		assert.Equal(t, "toolu_1", state.toolCalls[0].ID)
	})
}

// ============================================================================
// CALL LLM OUTPUT CONSTRUCTION
// ============================================================================
// Tests that the output built from streamProcessingState has properly
// structured tool calls that survive JSON serialization.

func TestCallLLMOutput_ToolCallsSurviveSerialization(t *testing.T) {
	// Build output the same way streamLLMResponse does (lines 563-576)
	toolCalls := []*reliantv1.ToolCallMsg{
		{
			Id:    "toolu_abc123",
			Name:  "Bash",
			Input: `{"command":"echo hello"}`,
		},
		{
			Id:    "toolu_def456",
			Name:  "View",
			Input: `{"file_path":"/test.go"}`,
		},
	}

	output := CallLLMOutput{
		ResponseText: "Let me run those.",
		ToolCalls:    toolCalls,
		TokenCount:   250,
		Cost:         0.0456,
		Message: &MessageOutput{
			Role: "assistant",
			Text: "Let me run those.",
		},
	}

	// Serialize → deserialize (Temporal round-trip)
	jsonBytes, err := json.Marshal(&output)
	require.NoError(t, err)

	var deserialized map[string]interface{}
	err = json.Unmarshal(jsonBytes, &deserialized)
	require.NoError(t, err)

	// Verify tool_calls survived as structured array
	tc, ok := deserialized["tool_calls"]
	require.True(t, ok, "tool_calls field must exist")
	require.NotNil(t, tc, "tool_calls must not be nil")

	tcArray, ok := tc.([]interface{})
	require.True(t, ok, "tool_calls must be []interface{}")
	require.Len(t, tcArray, 2)

	// Verify each tool call has the right fields
	for i, raw := range tcArray {
		tcMap, ok := raw.(map[string]interface{})
		require.True(t, ok, "tool call %d must be map", i)

		_, hasID := tcMap["id"].(string)
		require.True(t, hasID, "tool call %d must have string id", i)

		_, hasName := tcMap["name"].(string)
		require.True(t, hasName, "tool call %d must have string name", i)

		_, hasInput := tcMap["input"].(string)
		require.True(t, hasInput, "tool call %d must have string input", i)
	}

	// Also verify it can be deserialized back to the typed output
	var typedOutput CallLLMOutput
	err = json.Unmarshal(jsonBytes, &typedOutput)
	require.NoError(t, err)
	require.Len(t, typedOutput.ToolCalls, 2)
	assert.Equal(t, "toolu_abc123", typedOutput.ToolCalls[0].GetId())
	assert.Equal(t, "Bash", typedOutput.ToolCalls[0].GetName())
	assert.Equal(t, "toolu_def456", typedOutput.ToolCalls[1].GetId())
	assert.Equal(t, "View", typedOutput.ToolCalls[1].GetName())
	assert.Equal(t, 0.0456, typedOutput.Cost)
}

func TestCallLLMOutput_ToolCallsNotEmbeddedInText(t *testing.T) {
	// Regression test: when tool calls are properly extracted, they should NOT
	// appear as XML/text in the response_text field.

	t.Run("response_text should not contain tool call XML", func(t *testing.T) {
		output := CallLLMOutput{
			ResponseText: "Let me check that file.",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "toolu_abc", Name: "View", Input: `{"file_path":"test.go"}`},
			},
			TokenCount: 200,
			Message:    &MessageOutput{Role: "assistant", Text: "Let me check that file."},
		}

		// response_text should be clean text, no embedded tool calls
		assert.NotContains(t, output.ResponseText, "<tool_call>",
			"response_text should not contain tool call XML tags")
		assert.NotContains(t, output.ResponseText, "toolu_abc",
			"response_text should not contain tool call IDs")
		assert.NotContains(t, output.ResponseText, `"name":"View"`,
			"response_text should not contain tool call JSON")

		// ToolCalls should be the structured array
		require.Len(t, output.ToolCalls, 1)
		assert.Equal(t, "toolu_abc", output.ToolCalls[0].GetId())
	})
}

// ============================================================================
// BASH COMMAND TRIMMING IN TOOL CALLS
// ============================================================================
// Tests that trimBashWorkspaceCD correctly trims redundant cd prefixes.

func TestTrimBashWorkspaceCD(t *testing.T) {
	t.Run("trims workspace cd prefix", func(t *testing.T) {
		input := `{"command":"cd /workspace/project && ls -la"}`
		result := trimBashWorkspaceCD(input, "/workspace/project")
		assert.Equal(t, `{"command":"ls -la"}`, result)
	})

	t.Run("no trim when no workspace prefix", func(t *testing.T) {
		input := `{"command":"ls -la"}`
		result := trimBashWorkspaceCD(input, "/workspace/project")
		assert.Equal(t, `{"command":"ls -la"}`, result)
	})

	t.Run("no trim when workspace dir is empty", func(t *testing.T) {
		input := `{"command":"cd /somewhere && ls -la"}`
		result := trimBashWorkspaceCD(input, "")
		assert.Equal(t, `{"command":"cd /somewhere && ls -la"}`, result)
	})
}
