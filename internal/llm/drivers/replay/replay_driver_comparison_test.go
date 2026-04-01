// Copyright (c) 2025 Reliant Labs
package replay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplayDriverComparison tests that replay driver output matches expected driver response format
func TestReplayDriverComparison(t *testing.T) {
	// Define expected driver responses for different scenarios
	testCases := []struct {
		name             string
		messageContent   json.RawMessage
		expectedResponse *llm.DriverResponse
		description      string
	}{
		{
			name: "TextOnlyMessage",
			messageContent: json.RawMessage(`[
				{"type":"text","data":{"text":"Hello, I can help you with that."}},
				{"type":"finish","data":{"reason":"stop"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content:      "Hello, I can help you with that.",
				ToolCalls:    nil,
				FinishReason: message.FinishReasonEndTurn,
			},
			description: "Simple text response without tool calls",
		},
		{
			name: "TextWithSingleToolCall",
			messageContent: json.RawMessage(`[
				{"type":"text","data":{"text":"Let me search for that information."}},
				{"type":"tool_call","data":{"id":"call_123","name":"search","input":"{\"query\":\"test query\"}","type":"function","finished":true}},
				{"type":"finish","data":{"reason":"tool_use"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content: "Let me search for that information.",
				ToolCalls: []message.ToolCall{
					{
						ID:       "call_123",
						Name:     "search",
						Input:    `{"query":"test query"}`,
						Type:     "function",
						Finished: true,
					},
				},
				FinishReason: message.FinishReasonToolUse,
			},
			description: "Text with a single tool call",
		},
		{
			name: "MultipleToolCalls",
			messageContent: json.RawMessage(`[
				{"type":"text","data":{"text":"I'll help you with multiple tasks."}},
				{"type":"tool_call","data":{"id":"call_1","name":"read_file","input":"{\"path\":\"/tmp/test.txt\"}","type":"function","finished":true}},
				{"type":"tool_call","data":{"id":"call_2","name":"write_file","input":"{\"path\":\"/tmp/output.txt\",\"content\":\"test\"}","type":"function","finished":true}},
				{"type":"finish","data":{"reason":"tool_use"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content: "I'll help you with multiple tasks.",
				ToolCalls: []message.ToolCall{
					{
						ID:       "call_1",
						Name:     "read_file",
						Input:    `{"path":"/tmp/test.txt"}`,
						Type:     "function",
						Finished: true,
					},
					{
						ID:       "call_2",
						Name:     "write_file",
						Input:    `{"path":"/tmp/output.txt","content":"test"}`,
						Type:     "function",
						Finished: true,
					},
				},
				FinishReason: message.FinishReasonToolUse,
			},
			description: "Multiple tool calls in one response",
		},
		{
			name: "ToolCallOnly",
			messageContent: json.RawMessage(`[
				{"type":"tool_call","data":{"id":"call_456","name":"execute","input":"{\"command\":\"ls -la\"}","type":"function","finished":true}},
				{"type":"finish","data":{"reason":"tool_use"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content: "",
				ToolCalls: []message.ToolCall{
					{
						ID:       "call_456",
						Name:     "execute",
						Input:    `{"command":"ls -la"}`,
						Type:     "function",
						Finished: true,
					},
				},
				FinishReason: message.FinishReasonToolUse,
			},
			description: "Tool call without any text",
		},
		{
			name: "WithReasoningContent",
			messageContent: json.RawMessage(`[
				{"type":"reasoning","data":{"thinking":"I need to analyze this request carefully."}},
				{"type":"text","data":{"text":"Based on my analysis, here's the answer."}},
				{"type":"finish","data":{"reason":"stop"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content:      "Based on my analysis, here's the answer.",
				ToolCalls:    nil,
				FinishReason: message.FinishReasonEndTurn,
			},
			description: "Message with reasoning (should be excluded from content)",
		},
		{
			name: "ToolResultInMessage",
			messageContent: json.RawMessage(`[
				{"type":"text","data":{"text":"Processing the results..."}},
				{"type":"tool_result","data":{"id":"call_123","name":"search","output":"{\"results\":[]}","error":""}},
				{"type":"text","data":{"text":"The search returned no results."}},
				{"type":"finish","data":{"reason":"stop"}}
			]`),
			expectedResponse: &llm.DriverResponse{
				Content:      "Processing the results...The search returned no results.",
				ToolCalls:    nil,
				FinishReason: message.FinishReasonEndTurn,
			},
			description: "Tool results should not be included as tool calls",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a test replay file
			testData := &ComprehensiveReplayData{
				RootSessionID: "test-session",
				Title:         tc.name,
				Sessions: map[string]*ReplaySession{
					"test-session": {
						ID:    "test-session",
						Title: tc.name,
						Messages: []ComprehensiveMessage{
							{
								ID:        "msg-1",
								SessionID: "test-session",
								Role:      "user",
								Content:   json.RawMessage(`[{"type":"text","data":{"text":"Test question"}},{"type":"finish","data":{"reason":"stop"}}]`),
								CreatedAt: time.Now().Add(-2 * time.Minute),
							},
							{
								ID:        "msg-2",
								SessionID: "test-session",
								Role:      "assistant",
								Content:   tc.messageContent,
								Model:     "test-model",
								CreatedAt: time.Now().Add(-1 * time.Minute),
							},
						},
						CreatedAt:    time.Now().Add(-2 * time.Minute),
						UpdatedAt:    time.Now(),
						MessageCount: 2,
					},
				},
				MessageOrder: []string{"msg-1", "msg-2"},
				CreatedAt:    time.Now().Add(-2 * time.Minute),
				ExtractedAt:  time.Now(),
			}

			// Create temp file
			tmpDir := t.TempDir()
			replayFile := filepath.Join(tmpDir, "test_comparison.json")

			data, err := json.MarshalIndent(testData, "", "  ")
			require.NoError(t, err)

			err = os.WriteFile(replayFile, data, 0644)
			require.NoError(t, err)

			// Create replay driver
			driver, err := NewComprehensiveReplayDriver(replayFile)
			require.NoError(t, err)

			// Get response from replay driver
			ctx := context.Background()
			actualResponse, err := driver.SendMessages(ctx, nil, nil, nil)
			require.NoError(t, err, "Failed to get response for test case: %s", tc.description)

			// Compare responses
			assert.Equal(t, tc.expectedResponse.Content, actualResponse.Content,
				"Content mismatch for %s: %s", tc.name, tc.description)

			assert.Equal(t, len(tc.expectedResponse.ToolCalls), len(actualResponse.ToolCalls),
				"Tool call count mismatch for %s: %s", tc.name, tc.description)

			// Compare individual tool calls
			for i, expectedCall := range tc.expectedResponse.ToolCalls {
				if i < len(actualResponse.ToolCalls) {
					actualCall := actualResponse.ToolCalls[i]
					assert.Equal(t, expectedCall.ID, actualCall.ID,
						"Tool call ID mismatch at index %d for %s", i, tc.name)
					assert.Equal(t, expectedCall.Name, actualCall.Name,
						"Tool call Name mismatch at index %d for %s", i, tc.name)
					assert.JSONEq(t, expectedCall.Input, actualCall.Input,
						"Tool call Input mismatch at index %d for %s", i, tc.name)
					assert.Equal(t, expectedCall.Type, actualCall.Type,
						"Tool call Type mismatch at index %d for %s", i, tc.name)
					assert.Equal(t, expectedCall.Finished, actualCall.Finished,
						"Tool call Finished mismatch at index %d for %s", i, tc.name)
				}
			}

			assert.Equal(t, tc.expectedResponse.FinishReason, actualResponse.FinishReason,
				"FinishReason mismatch for %s: %s", tc.name, tc.description)
		})
	}
}

// TestReplayDriverStreamingComparison tests that streaming output matches non-streaming
func TestReplayDriverStreamingComparison(t *testing.T) {
	// Create test data with tool calls
	testData := &ComprehensiveReplayData{
		RootSessionID: "stream-test",
		Title:         "Streaming Test",
		Sessions: map[string]*ReplaySession{
			"stream-test": {
				ID:    "stream-test",
				Title: "Streaming Test",
				Messages: []ComprehensiveMessage{
					{
						ID:        "msg-1",
						SessionID: "stream-test",
						Role:      "user",
						Content:   json.RawMessage(`[{"type":"text","data":{"text":"Help me"}},{"type":"finish","data":{"reason":"stop"}}]`),
						CreatedAt: time.Now().Add(-2 * time.Minute),
					},
					{
						ID:        "msg-2",
						SessionID: "stream-test",
						Role:      "assistant",
						Content: json.RawMessage(`[
							{"type":"text","data":{"text":"I'll search for that."}},
							{"type":"tool_call","data":{"id":"tc1","name":"search","input":"{\"q\":\"test\"}","type":"function","finished":true}},
							{"type":"finish","data":{"reason":"tool_use"}}
						]`),
						Model:     "test-model",
						CreatedAt: time.Now().Add(-1 * time.Minute),
					},
				},
				CreatedAt:    time.Now().Add(-2 * time.Minute),
				UpdatedAt:    time.Now(),
				MessageCount: 2,
			},
		},
		MessageOrder: []string{"msg-1", "msg-2"},
		CreatedAt:    time.Now().Add(-2 * time.Minute),
		ExtractedAt:  time.Now(),
	}

	// Create temp file
	tmpDir := t.TempDir()
	replayFile := filepath.Join(tmpDir, "stream_test.json")

	data, err := json.MarshalIndent(testData, "", "  ")
	require.NoError(t, err)

	err = os.WriteFile(replayFile, data, 0644)
	require.NoError(t, err)

	// Create replay driver
	driver, err := NewComprehensiveReplayDriver(replayFile)
	require.NoError(t, err)

	ctx := context.Background()

	// Get non-streaming response
	nonStreamResp, err := driver.SendMessages(ctx, nil, nil, nil)
	require.NoError(t, err)

	// Reset driver for streaming test
	driver.Reset()

	// Get streaming response
	ch := driver.StreamResponse(ctx, nil, nil, nil)

	var streamedContent string
	var finalResponse *llm.DriverResponse
	var events []llm.EventType

	for event := range ch {
		events = append(events, event.Type)
		switch event.Type {
		case llm.EventContentDelta:
			streamedContent += event.Content
		case llm.EventToolUseStart:
			assert.NotNil(t, event.ToolCall, "Tool use start event should have tool call")
		case llm.EventToolUseDelta:
			// Tool call deltas should be handled
		case llm.EventComplete:
			finalResponse = event.Response
		case llm.EventError:
			t.Fatalf("Streaming error: %v", event.Error)
		}
	}

	// Verify we got the expected events
	assert.Contains(t, events, llm.EventContentStart, "Should have content start event")
	assert.Contains(t, events, llm.EventComplete, "Should have complete event")

	// Compare streamed vs non-streamed responses
	require.NotNil(t, finalResponse, "Should have final response from streaming")
	assert.Equal(t, nonStreamResp.Content, finalResponse.Content, "Content should match")
	assert.Equal(t, len(nonStreamResp.ToolCalls), len(finalResponse.ToolCalls), "Tool calls should match")
	assert.Equal(t, nonStreamResp.FinishReason, finalResponse.FinishReason, "Finish reason should match")
}
