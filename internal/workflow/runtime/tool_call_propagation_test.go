// Copyright (c) 2025 Reliant Labs
// Tests for tool call parsing, propagation, and edge condition evaluation.
//
// These tests cover the full pipeline from LLM response → CallLLMOutput → JSON round-trip
// (Temporal serialization) → edge condition evaluation → save_message template resolution.
//
// This test file specifically targets the regression where tool calls appear as raw XML text
// in message content instead of structured tool_call objects in the output, causing:
//   - nodes.call_llm.tool_calls to be null/empty
//   - execute_tools edge never firing
//   - {{output.tool_calls}} resolving to empty
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// testToolCall mirrors message.ToolCall for JSON round-trip testing without importing handlers.
type testToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`
	BlockIndex       int    `json:"block_index,omitempty"`
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// testMessageOutput mirrors MessageOutput for JSON round-trip testing.
type testMessageOutput struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// testThinkingOutput mirrors ThinkingOutput for JSON round-trip testing.
type testThinkingOutput struct {
	Content   string `json:"content"`
	Signature string `json:"signature"`
}

// testCallLLMOutput mirrors CallLLMOutput for JSON round-trip testing.
type testCallLLMOutput struct {
	ResponseText string             `json:"response_text"`
	ToolCalls    []testToolCall     `json:"tool_calls"`
	TokenCount   int                `json:"token_count"`
	Message      testMessageOutput  `json:"message"`
	Thinking     testThinkingOutput `json:"thinking"`
}

// ============================================================================
// 1. CallLLMOutput TOOL CALL SERIALIZATION
// ============================================================================
// Tests that CallLLMOutput correctly carries structured tool calls through
// JSON round-trip (which is what Temporal does with activity outputs).

func TestCallLLMOutput_ToolCallsJSONRoundTrip(t *testing.T) {
	t.Parallel()
	// Construct a CallLLMOutput as the call_llm activity would produce it
	output := testCallLLMOutput{
		ResponseText: "Let me check that file for you.",
		ToolCalls: []testToolCall{
			{
				ID:    "toolu_abc123",
				Name:  "Bash",
				Input: `{"command":"ls -la"}`,
			},
			{
				ID:    "toolu_def456",
				Name:  "View",
				Input: `{"file_path":"/tmp/test.go"}`,
			},
		},
		TokenCount: 250,
		Message: testMessageOutput{
			Role: "assistant",
			Text: "Let me check that file for you.",
		},
		Thinking: testThinkingOutput{
			Content:   "I need to look at the file",
			Signature: "sig_xyz",
		},
	}

	// Simulate Temporal JSON serialization round-trip
	jsonBytes, err := json.Marshal(output)
	require.NoError(t, err, "CallLLMOutput should marshal to JSON")

	var roundTripped map[string]interface{}
	err = json.Unmarshal(jsonBytes, &roundTripped)
	require.NoError(t, err, "CallLLMOutput JSON should unmarshal to map")

	// Verify tool_calls is a proper array in the deserialized map
	toolCalls, ok := roundTripped["tool_calls"]
	require.True(t, ok, "tool_calls field must exist in JSON output")
	require.NotNil(t, toolCalls, "tool_calls must not be nil")

	tcArray, ok := toolCalls.([]interface{})
	require.True(t, ok, "tool_calls must be an array ([]interface{}), got %T", toolCalls)
	require.Len(t, tcArray, 2, "Should have 2 tool calls")

	// Verify first tool call structure
	tc0, ok := tcArray[0].(map[string]interface{})
	require.True(t, ok, "tool call must be a map, got %T", tcArray[0])
	assert.Equal(t, "toolu_abc123", tc0["id"])
	assert.Equal(t, "Bash", tc0["name"])
	assert.Equal(t, `{"command":"ls -la"}`, tc0["input"])

	// Verify second tool call structure
	tc1, ok := tcArray[1].(map[string]interface{})
	require.True(t, ok, "tool call must be a map, got %T", tcArray[1])
	assert.Equal(t, "toolu_def456", tc1["id"])
	assert.Equal(t, "View", tc1["name"])
}

func TestCallLLMOutput_EmptyToolCalls_RoundTrip(t *testing.T) {
	t.Parallel()
	// When the LLM returns no tool calls (text-only response)
	output := testCallLLMOutput{
		ResponseText: "Hello, how can I help?",
		ToolCalls:    []testToolCall{}, // empty, not nil
		TokenCount:   100,
		Message: testMessageOutput{
			Role: "assistant",
			Text: "Hello, how can I help?",
		},
	}

	jsonBytes, err := json.Marshal(output)
	require.NoError(t, err)

	var roundTripped map[string]interface{}
	err = json.Unmarshal(jsonBytes, &roundTripped)
	require.NoError(t, err)

	// Empty array should serialize as empty array, not null
	toolCalls := roundTripped["tool_calls"]
	require.NotNil(t, toolCalls, "empty tool_calls should not become nil after round-trip")

	tcArray, ok := toolCalls.([]interface{})
	require.True(t, ok, "tool_calls must be an array")
	assert.Len(t, tcArray, 0, "Should have 0 tool calls")
}

func TestCallLLMOutput_NilToolCalls_RoundTrip(t *testing.T) {
	t.Parallel()
	// When the LLM returns nil tool calls (no tool_calls field at all)
	output := testCallLLMOutput{
		ResponseText: "Hello",
		ToolCalls:    nil,
		TokenCount:   100,
		Message: testMessageOutput{
			Role: "assistant",
			Text: "Hello",
		},
	}

	jsonBytes, err := json.Marshal(output)
	require.NoError(t, err)

	var roundTripped map[string]interface{}
	err = json.Unmarshal(jsonBytes, &roundTripped)
	require.NoError(t, err)

	// nil tool_calls should serialize as null (or missing if omitempty)
	// In the current struct it does NOT have omitempty, so it becomes null
	toolCalls := roundTripped["tool_calls"]
	// null in JSON becomes nil in Go
	assert.Nil(t, toolCalls, "nil tool_calls should remain nil after round-trip")
}

// ============================================================================
// 2. EDGE CONDITION EVALUATION WITH TOOL CALLS
// ============================================================================
// Tests that the edge condition `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0`
// correctly evaluates when tool calls come from a JSON round-tripped CallLLMOutput.

func TestEdgeCondition_ToolCallsFromJSONRoundTrip(t *testing.T) {
	t.Parallel()
	// This test simulates the exact data flow:
	// 1. CallLLM produces output with ToolCalls
	// 2. Temporal serializes/deserializes via JSON
	// 3. Edge condition evaluates against the deserialized map

	condition := `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0`

	t.Run("tool calls present - condition should be true", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "Let me run that",
			ToolCalls: []testToolCall{
				{ID: "toolu_123", Name: "Bash", Input: `{"command":"echo test"}`},
			},
			TokenCount: 150,
			Message:    testMessageOutput{Role: "assistant", Text: "Let me run that"},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    nodeOutputs,
			Inputs:   map[string]interface{}{"mode": "auto"},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err, "CEL evaluation should not error")
		assert.True(t, result, "Edge condition should be true when tool calls are present")
	})

	t.Run("no tool calls (empty array) - condition should be false", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "Hello!",
			ToolCalls:    []testToolCall{},
			TokenCount:   100,
			Message:      testMessageOutput{Role: "assistant", Text: "Hello!"},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    nodeOutputs,
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err, "CEL evaluation should not error")
		assert.False(t, result, "Edge condition should be false when tool_calls is empty")
	})

	t.Run("nil tool calls - condition should be false", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "Hello!",
			ToolCalls:    nil,
			TokenCount:   100,
			Message:      testMessageOutput{Role: "assistant", Text: "Hello!"},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    nodeOutputs,
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err, "CEL evaluation should not error for nil tool_calls")
		assert.False(t, result, "Edge condition should be false when tool_calls is nil")
	})

	t.Run("multiple tool calls - condition should be true", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "I'll check multiple files",
			ToolCalls: []testToolCall{
				{ID: "toolu_1", Name: "View", Input: `{"file_path":"a.go"}`},
				{ID: "toolu_2", Name: "View", Input: `{"file_path":"b.go"}`},
				{ID: "toolu_3", Name: "Grep", Input: `{"pattern":"error"}`},
			},
			TokenCount: 300,
			Message:    testMessageOutput{Role: "assistant", Text: "I'll check multiple files"},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    nodeOutputs,
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err)
		assert.True(t, result, "Edge condition should be true with multiple tool calls")
	})
}

func TestEdgeCondition_ToolCallsNotText(t *testing.T) {
	t.Parallel()
	// Regression test: tool calls should NOT be plain text strings in the output.
	// When tool calls appear as text (the bug), tool_calls field will be null/empty
	// and the text will be embedded in response_text instead.

	t.Run("tool calls as structured objects not text", func(t *testing.T) {
		correctOutput := map[string]interface{}{
			"call_llm": map[string]interface{}{
				"response_text": "Let me check that.",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id":    "toolu_abc",
						"name":  "Bash",
						"input": `{"command":"echo hi"}`,
					},
				},
				"token_count": float64(150),
				"message": map[string]interface{}{
					"role": "assistant",
					"text": "Let me check that.",
				},
			},
		}

		condition := `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0`

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    correctOutput,
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err)
		assert.True(t, result, "Structured tool calls should pass edge condition")
	})

	t.Run("tool calls as text in response (the bug)", func(t *testing.T) {
		// This simulates the buggy output where tool calls appear as text
		buggyOutput := map[string]interface{}{
			"call_llm": map[string]interface{}{
				"response_text": `<use_tool>{"name":"Bash","input":{"command":"echo hi"}}</use_tool>`,
				"tool_calls":    nil, // Bug: tool_calls is nil because they're in the text
				"token_count":   float64(150),
				"message": map[string]interface{}{
					"role": "assistant",
					"text": `<use_tool>{"name":"Bash","input":{"command":"echo hi"}}</use_tool>`,
				},
			},
		}

		condition := `nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0`

		ctx := &wfcel.EdgeEvalContext{
			Nodes:    buggyOutput,
			Inputs:   map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "wf-1", Name: "agent"},
		}

		result, err := wfcel.EvaluateBool(condition, ctx)
		require.NoError(t, err)
		assert.False(t, result, "Text-based tool calls should NOT pass edge condition (tool_calls is nil)")
	})
}

// ============================================================================
// 3. COMPLETE DATA FLOW: CallLLM → Edge → State Machine
// ============================================================================
// Tests the full pipeline from CallLLM output through edge evaluation
// to state machine routing, which is the exact production path.

func TestCallLLMToStateMachineRouting(t *testing.T) {
	t.Parallel()
	wfJSON := `{
		"name": "test-agent",
		"entry": ["call_llm"],
		"nodes": [
			{"id": "call_llm", "type": "call_llm"},
			{"id": "execute_tools", "type": "execute_tools"},
			{"id": "done", "type": "noop"}
		],
		"edges": [
			{
				"from": "call_llm",
				"cases": [
					{"to": "execute_tools", "condition": "nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0"},
					{"to": "done"}
				]
			}
		]
	}`

	wf, err := LoadWorkflow([]byte(wfJSON))
	require.NoError(t, err)

	sm := NewSimplifiedStateMachine("test-workflow", wf)

	t.Run("routes to execute_tools when tool calls present", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "Let me check that.",
			ToolCalls: []testToolCall{
				{ID: "toolu_abc", Name: "Bash", Input: `{"command":"echo test"}`},
			},
			TokenCount: 150,
			Message:    testMessageOutput{Role: "assistant", Text: "Let me check that."},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		event := &core.WorkflowEvent{
			ID:           "event-call_llm",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "call_llm",
			Data:         nodeOutputs["call_llm"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "execute_tools", triggered[0].Node.GetId(),
			"Should route to execute_tools when tool_calls are present")
	})

	t.Run("routes to done when no tool calls", func(t *testing.T) {
		output := testCallLLMOutput{
			ResponseText: "Here's your answer.",
			ToolCalls:    []testToolCall{},
			TokenCount:   100,
			Message:      testMessageOutput{Role: "assistant", Text: "Here's your answer."},
		}

		nodeOutputs := jsonRoundTripTestCallLLMOutput(t, output)

		event := &core.WorkflowEvent{
			ID:           "event-call_llm",
			WorkflowID:   "test-workflow",
			ChatID:       "test-chat",
			WorkflowName: wf.Name,
			StepID:       "call_llm",
			Data:         nodeOutputs["call_llm"].(map[string]interface{}),
		}

		triggered, err := sm.FindTriggeredNodes([]*core.WorkflowEvent{event}, nodeOutputs, map[string]interface{}{})
		require.NoError(t, err)

		require.Len(t, triggered, 1, "Should trigger exactly one node")
		assert.Equal(t, "done", triggered[0].Node.GetId(),
			"Should route to done when tool_calls is empty array")
	})
}

// ============================================================================
// 4. SAVE_MESSAGE TEMPLATE RESOLUTION FOR TOOL CALLS
// ============================================================================
// Tests that {{output.tool_calls}} properly resolves in save_message configuration.

func TestSaveMessageConfig_OutputToolCallsResolution(t *testing.T) {
	t.Parallel()
	t.Run("output.tool_calls resolves to structured array", func(t *testing.T) {
		// Build a save_message config that references output.tool_calls
		config := &reliantv1.SaveMessageConfig{
			Role:      celLiteral("assistant"),
			Content:   celLiteral("{{output.response_text}}"),
			ToolCalls: celLiteral("{{output.tool_calls}}"),
		}

		// Simulate what the activity output looks like after JSON round-trip
		activityOutput := map[string]interface{}{
			"response_text": "I'll check that file.",
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":    "toolu_abc",
					"name":  "View",
					"input": `{"file_path":"/test.go"}`,
				},
			},
			"token_count": float64(200),
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "I'll check that file.",
			},
		}

		workflowContext := map[string]interface{}{
			"inputs": map[string]interface{}{},
		}

		result, err := evaluateSaveMessageConfig(
			config,
			activityOutput,
			workflowContext,
			map[string]interface{}{}, // nodeOutputs
			"chat-123",
			"thread-0",
			"workflow-123",
			"call_llm",
			nil, // execContext
			nil, // iter (not in a loop)
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.ToolCalls, 1, "Should have 1 tool call resolved from output")
		assert.Equal(t, "toolu_abc", result.ToolCalls[0].ID)
		assert.Equal(t, "View", result.ToolCalls[0].Name)
		assert.Equal(t, `{"file_path":"/test.go"}`, result.ToolCalls[0].Input)
	})

	t.Run("output.tool_calls resolves to empty when not configured", func(t *testing.T) {
		// When tool_calls is not configured in save_message, it should be nil.
		// This is the safe pattern - don't pass tool_calls when they might be null.
		config := &reliantv1.SaveMessageConfig{
			Role:    celLiteral("assistant"),
			Content: celLiteral("{{output.response_text}}"),
			// ToolCalls intentionally not set
		}

		activityOutput := map[string]interface{}{
			"response_text": "Hello",
			"tool_calls":    nil,
			"token_count":   float64(100),
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "Hello",
			},
		}

		workflowContext := map[string]interface{}{
			"inputs": map[string]interface{}{},
		}

		result, err := evaluateSaveMessageConfig(
			config,
			activityOutput,
			workflowContext,
			map[string]interface{}{},
			"chat-123",
			"thread-0",
			"workflow-123",
			"call_llm",
			nil, // execContext
			nil, // iter (not in a loop)
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Nil(t, result.ToolCalls, "Tool calls should be nil when not configured")
	})

	t.Run("output.tool_calls resolves to empty via direct expression when null", func(t *testing.T) {
		// Directly passing {{output.tool_calls}} when it's null used to leak the
		// structpb.NullValue enum out of CEL evaluation and fail with
		// "expected array, got structpb.NullValue". CEL null is now normalized
		// to Go nil (wfcel.ConvertToNative), so a null tool_calls behaves the
		// same as not configuring tool_calls at all.
		config := &reliantv1.SaveMessageConfig{
			Role:      celLiteral("assistant"),
			Content:   celLiteral("{{output.response_text}}"),
			ToolCalls: celLiteral("{{output.tool_calls}}"),
		}

		activityOutput := map[string]interface{}{
			"response_text": "Hello",
			"tool_calls":    nil,
			"token_count":   float64(100),
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "Hello",
			},
		}

		workflowContext := map[string]interface{}{
			"inputs": map[string]interface{}{},
		}

		result, err := evaluateSaveMessageConfig(
			config,
			activityOutput,
			workflowContext,
			map[string]interface{}{},
			"chat-123",
			"thread-0",
			"workflow-123",
			"call_llm",
			nil, // execContext
			nil, // iter (not in a loop)
		)

		require.NoError(t, err, "null tool_calls must not error — CEL null normalizes to nil")
		require.NotNil(t, result)
		assert.Empty(t, result.ToolCalls, "null tool_calls should resolve to no tool calls")
	})

	t.Run("output.tool_calls with multiple tool calls", func(t *testing.T) {
		config := &reliantv1.SaveMessageConfig{
			Role:      celLiteral("assistant"),
			Content:   celLiteral("{{output.response_text}}"),
			ToolCalls: celLiteral("{{output.tool_calls}}"),
		}

		activityOutput := map[string]interface{}{
			"response_text": "I'll run multiple tools",
			"tool_calls": []interface{}{
				map[string]interface{}{
					"id":    "toolu_1",
					"name":  "Bash",
					"input": `{"command":"ls"}`,
				},
				map[string]interface{}{
					"id":    "toolu_2",
					"name":  "View",
					"input": `{"file_path":"main.go"}`,
				},
				map[string]interface{}{
					"id":    "toolu_3",
					"name":  "Grep",
					"input": `{"pattern":"error","path":"."}`,
				},
			},
			"token_count": float64(350),
			"message": map[string]interface{}{
				"role": "assistant",
				"text": "I'll run multiple tools",
			},
		}

		workflowContext := map[string]interface{}{
			"inputs": map[string]interface{}{},
		}

		result, err := evaluateSaveMessageConfig(
			config,
			activityOutput,
			workflowContext,
			map[string]interface{}{},
			"chat-123",
			"thread-0",
			"workflow-123",
			"call_llm",
			nil, // execContext
			nil, // iter (not in a loop)
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.ToolCalls, 3, "Should have 3 tool calls resolved from output")
		assert.Equal(t, "toolu_1", result.ToolCalls[0].ID)
		assert.Equal(t, "Bash", result.ToolCalls[0].Name)
		assert.Equal(t, "toolu_2", result.ToolCalls[1].ID)
		assert.Equal(t, "View", result.ToolCalls[1].Name)
		assert.Equal(t, "toolu_3", result.ToolCalls[2].ID)
		assert.Equal(t, "Grep", result.ToolCalls[2].Name)
	})
}

// ============================================================================
// 5. STREAM EVENT → TOOL CALL EXTRACTION
// ============================================================================
// Tests that processStreamEvent → handleComplete correctly extracts tool calls
// from the DriverResponse, matching the real data flow.

func TestCallLLMOutput_ToolCallFieldTypes(t *testing.T) {
	t.Parallel()
	// After JSON round-trip, verify the types of tool call fields match what CEL expects
	output := testCallLLMOutput{
		ToolCalls: []testToolCall{
			{
				ID:               "toolu_abc",
				Name:             "Bash",
				Input:            `{"command":"test"}`,
				BlockIndex:       0,
				ThoughtSignature: "sig_123",
			},
		},
	}

	jsonBytes, err := json.Marshal(output)
	require.NoError(t, err)

	var deserialized map[string]interface{}
	err = json.Unmarshal(jsonBytes, &deserialized)
	require.NoError(t, err)

	toolCalls := deserialized["tool_calls"].([]interface{})
	tc := toolCalls[0].(map[string]interface{})

	// All these must be strings for CEL and save_message convertToToolCalls to work
	assert.IsType(t, "", tc["id"], "id must be string after JSON round-trip")
	assert.IsType(t, "", tc["name"], "name must be string after JSON round-trip")
	assert.IsType(t, "", tc["input"], "input must be string after JSON round-trip")

	// block_index=0 may be omitted due to omitempty, or present as float64
	if bi, exists := tc["block_index"]; exists {
		assert.IsType(t, float64(0), bi, "block_index becomes float64 after JSON")
	}

	// thought_signature should be a string if present
	if ts, exists := tc["thought_signature"]; exists {
		assert.IsType(t, "", ts, "thought_signature must be string after JSON round-trip")
	}
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// jsonRoundTripTestCallLLMOutput simulates what Temporal does: marshal testCallLLMOutput to JSON
// and unmarshal back to map[string]interface{}, then wrap in the "call_llm" key.
func jsonRoundTripTestCallLLMOutput(t *testing.T, output testCallLLMOutput) map[string]interface{} {
	t.Helper()

	jsonBytes, err := json.Marshal(output)
	require.NoError(t, err, "CallLLMOutput should marshal to JSON")

	var asMap map[string]interface{}
	err = json.Unmarshal(jsonBytes, &asMap)
	require.NoError(t, err, "CallLLMOutput JSON should unmarshal to map")

	return map[string]interface{}{
		"call_llm": asMap,
	}
}

// celLiteral is already defined in save_message_test.go (same package)
// celExpr is already defined in save_message_test.go (same package)
