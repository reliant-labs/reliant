// Copyright (c) 2025 Reliant Labs
//
// These tests prove the root cause of the PENDING SaveMessage (id=53) bug in
// TestAgent_SingleToolCall. The chain of failures:
//
//  1. Temporal's ProtoJSON converter encodes activity outputs with camelCase keys
//     (e.g., "toolResults", "toolCallId").
//  2. normalizeOutput merges schema defaults (snake_case keys with empty zero-values)
//     into the output map, resulting in BOTH "tool_results" (empty []) and "toolResults"
//     (actual data) existing simultaneously.
//  3. CEL expression `output.tool_results` finds the snake_case key with empty value,
//     not the camelCase key with actual data.
//  4. The SaveMessage activity is scheduled with role="tool" but empty tool_results.
//  5. threads.SaveMessage validation rejects: "tool_results is required for tool messages".
//  6. This error is classified as transient (retryable), so Temporal keeps retrying
//     until the workflow times out — hence the PENDING status.
//
// SECONDARY BUG (same root cause): The first SaveMessage (call_llm) also loses data.
// The tool_calls field from CallLLM output uses camelCase key "toolCalls", but the
// schema defaults provide "tool_calls" = [] (empty). CEL finds the empty default,
// so the assistant message is saved WITHOUT tool_calls. This doesn't cause a failure
// (empty tool_calls is valid for assistant messages) but means tool calls are silently
// lost from the saved message.
package proto_key_bug_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities" // triggers init() for schema registration
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestProtoJSONOutputKeysAreCamelCase proves that protojson (Temporal's default
// ProtoJSON converter) serializes ExecuteToolsOutput with camelCase field names.
func TestProtoJSONOutputKeysAreCamelCase(t *testing.T) {
	output := reliantv1.ExecuteToolsOutput{
		ToolResults: []*reliantv1.ToolResultMsg{
			{ToolCallId: "tc-1", Name: "Bash", Content: "hello world"},
		},
		ThreadTokenCount: 100,
		TotalResultChars: 11,
		Message:          &reliantv1.MessageOutput{Role: "tool", Text: ""},
	}

	// protojson with default options (what Temporal's ProtoJSONPayloadConverter uses)
	data, err := protojson.MarshalOptions{}.Marshal(&output)
	require.NoError(t, err)

	// Decode into map (what flexibleProtoJSONConverter does on the workflow side)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))

	t.Logf("protojson output keys: %v", mapKeys(decoded))
	t.Logf("raw JSON: %s", string(data))

	// protojson default uses camelCase
	assert.Contains(t, decoded, "toolResults", "protojson should produce camelCase 'toolResults'")
	assert.Contains(t, decoded, "threadTokenCount", "protojson should produce camelCase 'threadTokenCount'")

	// The snake_case keys that CEL expressions reference do NOT exist
	assert.NotContains(t, decoded, "tool_results", "snake_case key should NOT exist in protojson output")
	assert.NotContains(t, decoded, "thread_token_count", "snake_case key should NOT exist in protojson output")

	// Verify nested ToolResultMsg keys are also camelCase
	toolResults, ok := decoded["toolResults"].([]interface{})
	require.True(t, ok, "toolResults should be an array")
	require.Len(t, toolResults, 1)

	tr, ok := toolResults[0].(map[string]interface{})
	require.True(t, ok, "tool result should be a map")

	t.Logf("nested ToolResultMsg keys: %v", mapKeys(tr))

	// tool_call_id becomes toolCallId in protojson
	assert.Contains(t, tr, "toolCallId", "nested field should be camelCase")
	assert.NotContains(t, tr, "tool_call_id", "snake_case should NOT exist in nested proto object")
}

// TestSchemaDefaultsUseSnakeCaseKeys proves that GetOutputDefaults returns
// snake_case keys (from proto struct JSON tags), creating a key mismatch with
// the camelCase keys from protojson encoding.
func TestSchemaDefaultsUseSnakeCaseKeys(t *testing.T) {
	defaults := schema.GetOutputDefaults("ExecuteTools")
	require.NotNil(t, defaults, "ExecuteTools should have registered output defaults")

	t.Logf("schema default keys: %v", mapKeys(defaults))

	// Schema defaults come from JSON struct tags which are snake_case
	assert.Contains(t, defaults, "tool_results", "schema should have snake_case 'tool_results'")
	assert.Contains(t, defaults, "thread_token_count", "schema should have snake_case 'thread_token_count'")

	// Defaults are empty slices (zero value for repeated proto fields)
	assert.Empty(t, defaults["tool_results"], "default for tool_results should be empty")
}

// TestNormalizeOutputCreatesKeyMismatch proves that after normalizeOutput merges
// schema defaults with protojson output, the map contains BOTH snake_case keys
// (with nil defaults) AND camelCase keys (with actual data). CEL expressions
// that reference snake_case keys find the nil default instead of the real data.
func TestNormalizeOutputCreatesKeyMismatch(t *testing.T) {
	// Simulate protojson-decoded ExecuteTools output (camelCase keys)
	protoOutput := reliantv1.ExecuteToolsOutput{
		ToolResults: []*reliantv1.ToolResultMsg{
			{ToolCallId: "tc-1", Name: "Bash", Content: "hello world"},
		},
		ThreadTokenCount: 100,
		Message:          &reliantv1.MessageOutput{Role: "tool", Text: ""},
	}

	// Step 1: protojson encode (what Temporal's ProtoJSON converter does)
	data, err := protojson.MarshalOptions{}.Marshal(&protoOutput)
	require.NoError(t, err)

	// Step 2: json.Unmarshal into map (what flexibleProtoJSONConverter does)
	var rawOutput map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &rawOutput))

	// Step 3: normalizeOutput (reproducing what step_executor does)
	defaults := schema.GetOutputDefaults("ExecuteTools")
	require.NotNil(t, defaults)

	normalized := make(map[string]interface{})
	for field, defaultValue := range defaults {
		normalized[field] = defaultValue
	}
	for field, value := range rawOutput {
		normalized[field] = value
	}

	t.Logf("all keys after normalization: %v", mapKeys(normalized))

	// BUG: Both keys exist — snake_case has nil, camelCase has data
	snakeVal := normalized["tool_results"]
	camelVal := normalized["toolResults"]

	t.Logf("normalized['tool_results']  = %v (type: %T)", snakeVal, snakeVal)
	t.Logf("normalized['toolResults']   = %v (type: %T)", camelVal, camelVal)

	// The snake_case key from defaults is an empty slice (zero value), not the actual data
	assert.Empty(t, snakeVal, "BUG: snake_case key holds empty default, not the actual data")

	// The camelCase key from protojson holds the actual data
	assert.NotNil(t, camelVal, "camelCase key holds the actual tool results data")

	// CEL expression `output.tool_results` finds the nil default, not the real data.
	// This means the second SaveMessage (for tool results) gets scheduled with empty
	// tool_results, and then fails validation: "tool_results is required for tool messages".
}

// TestConvertToToolResultsFailsWithCamelCaseKeys proves that even if the correct
// array data were used, convertToToolResults would fail because the nested maps
// have camelCase keys (toolCallId) but it expects snake_case (tool_call_id).
func TestConvertToToolResultsFailsWithCamelCaseKeys(t *testing.T) {
	// Simulate a ToolResultMsg decoded from protojson via json.Unmarshal
	camelCaseMaps := []map[string]interface{}{
		{
			"toolCallId": "tc-1",
			"name":       "Bash",
			"content":    "hello world",
			"isError":    false,
		},
	}

	results, err := convertToToolResults(camelCaseMaps)
	// BUG: This fails because convertToToolResults looks for "tool_call_id" not "toolCallId"
	assert.Error(t, err, "should fail: convertToToolResults expects snake_case keys")
	assert.Nil(t, results)
	if err != nil {
		t.Logf("convertToToolResults error: %v", err)
		assert.Contains(t, err.Error(), "tool_call_id", "error should mention the missing snake_case key")
	}
}

// TestConvertToToolResultsWorksWithSnakeCaseKeys proves the happy path works
// when keys are snake_case (as expected by the converter).
func TestConvertToToolResultsWorksWithSnakeCaseKeys(t *testing.T) {
	snakeCaseMaps := []map[string]interface{}{
		{
			"tool_call_id": "tc-1",
			"name":         "Bash",
			"content":      "hello world",
			"is_error":     false,
		},
	}

	results, err := convertToToolResults(snakeCaseMaps)
	assert.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "tc-1", results[0].ToolCallID)
	assert.Equal(t, "hello world", results[0].Content)
}

// TestCallLLMToolCallsWorkBecauseSingleWordKeys shows why the first SaveMessage
// succeeds: ToolCallMsg field names are single words (id, name, input) which are
// the same in both camelCase and snake_case.
func TestCallLLMToolCallsWorkBecauseSingleWordKeys(t *testing.T) {
	output := reliantv1.CallLLMOutput{
		ToolCalls: []*reliantv1.ToolCallMsg{
			{Id: "tc-1", Name: "Bash", Input: `{"command":"echo test"}`},
		},
		Message: &reliantv1.MessageOutput{Role: "assistant", Text: "I'll run that."},
	}

	data, err := protojson.MarshalOptions{}.Marshal(&output)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))

	// Top-level: toolCalls (camelCase) — but schema defaults provide tool_calls
	// So CEL finds tool_calls via defaults (but it's nil! unless overwritten)
	toolCalls, ok := decoded["toolCalls"].([]interface{})
	require.True(t, ok)
	require.Len(t, toolCalls, 1)

	tc, ok := toolCalls[0].(map[string]interface{})
	require.True(t, ok)

	t.Logf("ToolCallMsg keys: %v", mapKeys(tc))

	// ToolCallMsg fields: id, name, input — all single words, same in camelCase and snake_case
	assert.Contains(t, tc, "id", "single-word field 'id' is same in both cases")
	assert.Contains(t, tc, "name", "single-word field 'name' is same in both cases")
	assert.Contains(t, tc, "input", "single-word field 'input' is same in both cases")

	// convertToToolCalls should work because it looks for "id", "name", "input"
	result, err := convertToToolCalls([]map[string]interface{}{tc})
	assert.NoError(t, err, "convertToToolCalls works because keys are single words")
	require.Len(t, result, 1)
	assert.Equal(t, "tc-1", result[0].ID)
}

// TestCallLLMSchemaDefaultsShadowProtojsonData shows that even for CallLLM,
// the schema defaults shadow the actual protojson data for multi-word keys.
// The first SaveMessage works by accident: tool_calls gets the nil default,
// but since the nil tool_calls is acceptable for assistant messages (unlike
// tool messages), the save succeeds — just without tool_calls data.
func TestCallLLMSchemaDefaultsShadowProtojsonData(t *testing.T) {
	defaults := schema.GetOutputDefaults("CallLLM")
	require.NotNil(t, defaults)

	t.Logf("CallLLM schema default keys: %v", mapKeys(defaults))

	// Schema defaults provide snake_case keys
	assert.Contains(t, defaults, "tool_calls", "CallLLM schema has 'tool_calls'")
	assert.Contains(t, defaults, "token_count", "CallLLM schema has 'token_count'")

	// Simulate protojson-decoded output
	output := reliantv1.CallLLMOutput{
		ToolCalls: []*reliantv1.ToolCallMsg{
			{Id: "tc-1", Name: "Bash", Input: `{"command":"echo test"}`},
		},
		TokenCount: 150,
		Message:    &reliantv1.MessageOutput{Role: "assistant", Text: "I'll run that."},
	}

	data, err := protojson.MarshalOptions{}.Marshal(&output)
	require.NoError(t, err)

	var rawOutput map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &rawOutput))

	// Merge defaults + raw output (reproducing normalizeOutput)
	normalized := make(map[string]interface{})
	for field, defaultValue := range defaults {
		normalized[field] = defaultValue
	}
	for field, value := range rawOutput {
		normalized[field] = value
	}

	// tool_calls from defaults is []interface{}{} (empty slice, not nil)
	snakeToolCalls := normalized["tool_calls"]
	camelToolCalls := normalized["toolCalls"]

	t.Logf("tool_calls (snake) = %v (%T)", snakeToolCalls, snakeToolCalls)
	t.Logf("toolCalls (camel)  = %v (%T)", camelToolCalls, camelToolCalls)

	// The snake_case key from defaults shadows the actual data
	// For assistant messages this is "OK" (empty tool_calls is valid) — the message
	// saves without tool calls. But this means tool calls are LOST from the saved message.
}

// TestProtoJSONWithUseProtoNamesFixesKeys proves that UseProtoNames:true
// produces snake_case keys, which would fix the mismatch.
func TestProtoJSONWithUseProtoNamesFixesKeys(t *testing.T) {
	output := reliantv1.ExecuteToolsOutput{
		ToolResults: []*reliantv1.ToolResultMsg{
			{ToolCallId: "tc-1", Name: "Bash", Content: "hello world"},
		},
		ThreadTokenCount: 100,
		Message:          &reliantv1.MessageOutput{Role: "tool", Text: ""},
	}

	// With UseProtoNames: true (snake_case)
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&output)
	require.NoError(t, err)

	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &decoded))

	t.Logf("UseProtoNames output keys: %v", mapKeys(decoded))
	t.Logf("raw JSON: %s", string(data))

	// Now we get snake_case keys that match CEL expressions
	assert.Contains(t, decoded, "tool_results")
	assert.Contains(t, decoded, "thread_token_count")

	// Nested keys are also snake_case
	toolResults := decoded["tool_results"].([]interface{})
	tr := toolResults[0].(map[string]interface{})
	assert.Contains(t, tr, "tool_call_id", "nested field should be snake_case with UseProtoNames")
}

// --- Copied helper functions to avoid import cycle ---

func convertToToolCalls(maps []map[string]interface{}) ([]message.ToolCall, error) {
	if maps == nil {
		return nil, nil
	}
	result := make([]message.ToolCall, 0, len(maps))
	for i, m := range maps {
		tc := message.ToolCall{}
		id, ok := m["id"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_calls[%d].id: expected string, got %T", i, m["id"])
		}
		tc.ID = id
		name, ok := m["name"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_calls[%d].name: expected string, got %T", i, m["name"])
		}
		tc.Name = name
		if input, ok := m["input"].(string); ok {
			tc.Input = input
		}
		result = append(result, tc)
	}
	return result, nil
}

func convertToToolResults(maps []map[string]interface{}) ([]message.ToolResult, error) {
	if maps == nil {
		return nil, nil
	}
	result := make([]message.ToolResult, 0, len(maps))
	for i, m := range maps {
		tr := message.ToolResult{}
		id, ok := m["tool_call_id"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_results[%d].tool_call_id: expected string, got %T", i, m["tool_call_id"])
		}
		tr.ToolCallID = id
		content, ok := m["content"].(string)
		if !ok {
			return nil, fmt.Errorf("tool_results[%d].content: expected string, got %T", i, m["content"])
		}
		tr.Content = content
		if name, ok := m["name"].(string); ok {
			tr.Name = name
		}
		if isError, ok := m["is_error"].(bool); ok {
			tr.IsError = isError
		}
		result = append(result, tr)
	}
	return result, nil
}

func mapKeys(m interface{}) []string {
	v := reflect.ValueOf(m)
	keys := make([]string, 0)
	if v.Kind() == reflect.Map {
		for _, k := range v.MapKeys() {
			keys = append(keys, k.String())
		}
	}
	return keys
}

// TestValidationRejectsEmptyToolResults proves the final failure: a tool-role
// message with empty tool_results is rejected by validation.
func TestValidationRejectsEmptyToolResults(t *testing.T) {
	// This is what the error message check in threads/save_message.go does
	err := validateToolMessage(0, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tool_results is required")
}

// TestErrorClassificationMakesItRetryable proves the validation error is
// classified as transient (retryable), causing infinite retries until timeout.
func TestErrorClassificationMakesItRetryable(t *testing.T) {
	errMsg := "tool_results is required for tool messages"

	// Check against the terminal patterns from registry.go
	terminalPatterns := []string{
		"not found", "does not exist", "invalid", "malformed",
		"forbidden", "unauthorized", "permission denied", "quota exceeded",
		"bad request", "cannot be empty", "cannot be nil", "unknown model",
		"unsupported", "prompt is too long", "too many tokens",
		"maximum context", "context length",
	}

	isTerminal := false
	for _, pattern := range terminalPatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			isTerminal = true
			break
		}
	}

	// BUG: "required" is not in the terminal patterns, so this error is retryable
	assert.False(t, isTerminal,
		"'tool_results is required' is NOT classified as terminal — Temporal retries it forever")
}

func validateToolMessage(role int32, toolResults interface{}) error {
	// Reproduces the check from threads/save_message.go:267-268
	if toolResults == nil {
		return fmt.Errorf("tool_results is required for tool messages")
	}
	return nil
}
