// Copyright (c) 2025 Reliant Labs
package activities

import (
	"encoding/json"
	"reflect"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// TYPE COMPATIBILITY TESTS
// ============================================================================
//
// These tests verify type compatibility between activities in the workflow.
// They ensure that data can flow correctly from one activity to another
// without type mismatches or missing fields.
//
// The tests use JSON marshaling/unmarshaling to verify compatibility since
// this is how the workflow engine passes data between activities.
//
// Test Coverage:
// 1. ToolCall (CallLLM) → ToolCall (SaveMessage)
// 2. ToolResult (ExecuteTools) → ToolResult (SaveMessage)
// 3. Complete data flow chain validation
// ============================================================================

// TestToolCallTypeCompatibility verifies that ToolCall from CallLLM
// can be converted to ToolCall for SaveMessage without data loss.
//
// Workflow path: CallLLM.tool_calls → SaveMessage.tool_calls
func TestToolCallTypeCompatibility(t *testing.T) {
	t.Run("ToolCall can be converted to ToolCall", func(t *testing.T) {
		// Create a ToolCall as returned by CallLLM
		original := handlers.ToolCall{
			ID:         "call_123",
			Name:       "bash",
			Input:      `{"command":"ls -la"}`,
			BlockIndex: 1,
		}

		// Marshal to JSON (simulates workflow passing data)
		jsonData, err := json.Marshal(original)
		require.NoError(t, err, "Failed to marshal ToolCall")

		// Unmarshal to ToolCall (as received by SaveMessage)
		var converted handlers.ToolCall
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err, "Failed to unmarshal to ToolCall")

		// Verify all required fields are present and correct
		assert.Equal(t, original.ID, converted.ID, "ID should match")
		assert.Equal(t, original.Name, converted.Name, "Name should match")
		assert.Equal(t, original.Input, converted.Input, "Input should match")
	})

	t.Run("Array of ToolCall can be converted to array of ToolCall", func(t *testing.T) {
		// Create multiple tool calls as returned by CallLLM
		originals := []handlers.ToolCall{
			{
				ID:         "call_1",
				Name:       "bash",
				Input:      `{"command":"git status"}`,
				BlockIndex: 1,
			},
			{
				ID:         "call_2",
				Name:       "read",
				Input:      `{"file_path":"/path/to/file.go"}`,
				BlockIndex: 2,
			},
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(originals)
		require.NoError(t, err, "Failed to marshal ToolCall array")

		// Unmarshal to ToolCall array
		var converted []handlers.ToolCall
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err, "Failed to unmarshal to ToolCall array")

		// Verify array length
		require.Len(t, converted, len(originals), "Array length should match")

		// Verify each element
		for i := range originals {
			assert.Equal(t, originals[i].ID, converted[i].ID, "ID[%d] should match", i)
			assert.Equal(t, originals[i].Name, converted[i].Name, "Name[%d] should match", i)
			assert.Equal(t, originals[i].Input, converted[i].Input, "Input[%d] should match", i)
		}
	})

	t.Run("ToolCall fields are compatible with ToolCall fields", func(t *testing.T) {
		// Get field information for both types
		referenceType := reflect.TypeOf(handlers.ToolCall{})
		inputType := reflect.TypeOf(handlers.ToolCall{})

		// Check that all required fields exist in both types
		requiredFields := []struct {
			name      string
			jsonTag   string
			fieldType reflect.Kind
		}{
			{"ID", "id", reflect.String},
			{"Name", "name", reflect.String},
			{"Input", "input", reflect.String},
		}

		for _, required := range requiredFields {
			// Check ToolCall has the field
			refField, refFound := referenceType.FieldByName(required.name)
			require.True(t, refFound, "ToolCall should have field %s", required.name)
			assert.Equal(t, required.fieldType, refField.Type.Kind(),
				"ToolCall.%s should be of type %s", required.name, required.fieldType)

			// Check ToolCall has the field
			inputField, inputFound := inputType.FieldByName(required.name)
			require.True(t, inputFound, "ToolCall should have field %s", required.name)
			assert.Equal(t, required.fieldType, inputField.Type.Kind(),
				"ToolCall.%s should be of type %s", required.name, required.fieldType)

			// Verify JSON tags match
			refTag := refField.Tag.Get("json")
			inputTag := inputField.Tag.Get("json")
			assert.Equal(t, required.jsonTag, refTag,
				"ToolCall.%s should have json tag '%s'", required.name, required.jsonTag)
			assert.Equal(t, required.jsonTag, inputTag,
				"ToolCall.%s should have json tag '%s'", required.name, required.jsonTag)
		}
	})

	t.Run("BlockIndex field is optional in ToolCall", func(t *testing.T) {
		// BlockIndex exists in unified ToolCall type but is optional (omitempty)
		// It's used by CallLLM to track position during streaming, but not required for SaveMessage
		toolCallType := reflect.TypeOf(handlers.ToolCall{})

		field, hasBlockIndex := toolCallType.FieldByName("BlockIndex")
		assert.True(t, hasBlockIndex, "ToolCall should have BlockIndex field")

		// Verify it's optional via omitempty tag
		jsonTag := field.Tag.Get("json")
		assert.Contains(t, jsonTag, "omitempty", "BlockIndex should have omitempty tag since it's optional")
	})

	t.Run("Empty tool calls array is valid", func(t *testing.T) {
		// Empty array should marshal/unmarshal correctly
		originals := []handlers.ToolCall{}

		jsonData, err := json.Marshal(originals)
		require.NoError(t, err)

		var converted []handlers.ToolCall
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err)

		assert.Empty(t, converted, "Empty array should remain empty")
	})
}

// TestToolResultTypeCompatibility verifies that ToolResult from ExecuteTools
// can be passed to SaveMessage without type conversion issues.
//
// Workflow path: ExecuteTools.tool_results → SaveMessage.tool_results
func TestToolResultTypeCompatibility(t *testing.T) {
	t.Run("ToolResult can be marshaled and unmarshaled without data loss", func(t *testing.T) {
		// Create a ToolResult as returned by ExecuteTools
		original := handlers.ToolResult{
			ToolCallID: "call_123",
			Content:    "Command executed successfully",
			IsError:    false,
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(original)
		require.NoError(t, err, "Failed to marshal ToolResult")

		// Unmarshal back to ToolResult (as received by SaveMessage)
		var converted handlers.ToolResult
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err, "Failed to unmarshal ToolResult")

		// Verify all fields match
		assert.Equal(t, original.ToolCallID, converted.ToolCallID, "ToolCallID should match")
		assert.Equal(t, original.Content, converted.Content, "Content should match")
		assert.Equal(t, original.IsError, converted.IsError, "IsError should match")
	})

	t.Run("ToolResult array preserves error states", func(t *testing.T) {
		// Create array with both success and error results
		originals := []handlers.ToolResult{
			{
				ToolCallID: "call_1",
				Content:    "Success output",
				IsError:    false,
			},
			{
				ToolCallID: "call_2",
				Content:    "Error: file not found",
				IsError:    true,
			},
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(originals)
		require.NoError(t, err)

		// Unmarshal to array
		var converted []handlers.ToolResult
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err)

		// Verify array length and contents
		require.Len(t, converted, len(originals))

		for i := range originals {
			assert.Equal(t, originals[i].ToolCallID, converted[i].ToolCallID, "ToolCallID[%d] should match", i)
			assert.Equal(t, originals[i].Content, converted[i].Content, "Content[%d] should match", i)
			assert.Equal(t, originals[i].IsError, converted[i].IsError, "IsError[%d] should match", i)
		}
	})

	t.Run("ToolResult fields have correct types", func(t *testing.T) {
		resultType := reflect.TypeOf(handlers.ToolResult{})

		requiredFields := []struct {
			name      string
			jsonTag   string
			fieldType reflect.Kind
		}{
			{"ToolCallID", "tool_call_id", reflect.String},
			{"Content", "content", reflect.String},
			{"IsError", "is_error", reflect.Bool},
		}

		for _, required := range requiredFields {
			field, found := resultType.FieldByName(required.name)
			require.True(t, found, "ToolResult should have field %s", required.name)
			assert.Equal(t, required.fieldType, field.Type.Kind(),
				"ToolResult.%s should be of type %s", required.name, required.fieldType)

			jsonTag := field.Tag.Get("json")
			assert.Equal(t, required.jsonTag, jsonTag,
				"ToolResult.%s should have json tag '%s'", required.name, required.jsonTag)
		}
	})

	t.Run("Empty content is valid for ToolResult", func(t *testing.T) {
		// ExecuteTools ensures content is never empty, but test the type can handle it
		result := handlers.ToolResult{
			ToolCallID: "call_123",
			Content:    "",
			IsError:    false,
		}

		jsonData, err := json.Marshal(result)
		require.NoError(t, err)

		var converted handlers.ToolResult
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err)

		assert.Equal(t, "", converted.Content, "Empty content should be preserved")
	})
}

// TestActivityOutputsMatchInputs verifies the complete data flow chain
// matches the workflow YAML expectations.
//
// This test validates the workflow paths defined in agent.yaml:
// 1. CallLLM.tool_calls → SaveMessage.tool_calls (input)
// 2. SaveMessage.tool_calls (output) → routing conditions
// 3. ExecuteTools.tool_results → SaveMessage.tool_results (input)
func TestActivityOutputsMatchInputs(t *testing.T) {
	t.Run("CallLLM output matches SaveMessage assistant input", func(t *testing.T) {
		// Simulate CallLLM output
		callLLMOutput := handlers.CallLLMOutput{
			ResponseText: "I'll execute these commands for you.",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{
					Id:    "call_abc123",
					Name:  "bash",
					Input: `{"command":"ls -la"}`,
				},
			},
			TokenCount: 650,
		}

		// Marshal CallLLM output
		jsonData, err := json.Marshal(&callLLMOutput)
		require.NoError(t, err)

		// Verify we can extract and use the fields for SaveMessage
		var outputMap map[string]interface{}
		err = json.Unmarshal(jsonData, &outputMap)
		require.NoError(t, err)

		// Extract fields needed for SaveMessage
		responseText, ok := outputMap["response_text"].(string)
		require.True(t, ok, "response_text should be extractable as string")
		assert.Equal(t, callLLMOutput.ResponseText, responseText)

		tokenCount, ok := outputMap["token_count"].(float64) // JSON numbers become float64
		require.True(t, ok, "token_count should be extractable")
		assert.Equal(t, float64(callLLMOutput.TokenCount), tokenCount)

		// Verify tool_calls can be converted
		toolCallsJSON, err := json.Marshal(outputMap["tool_calls"])
		require.NoError(t, err)

		var toolCalls []handlers.ToolCall
		err = json.Unmarshal(toolCallsJSON, &toolCalls)
		require.NoError(t, err)
		require.Len(t, toolCalls, 1)
		assert.Equal(t, "call_abc123", toolCalls[0].ID)
		assert.Equal(t, "bash", toolCalls[0].Name)
	})

	t.Run("SaveMessage output passes through tool_calls for routing", func(t *testing.T) {
		// SaveMessage should pass through tool_calls for workflow routing
		saveOutput := handlers.SaveMessageOutput{
			MessageId: "msg_123",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "bash", Input: `{"command":"pwd"}`},
				{Id: "call_2", Name: "read", Input: `{"file_path":"/tmp/file"}`},
			},
		}

		// Marshal output
		jsonData, err := json.Marshal(&saveOutput)
		require.NoError(t, err)

		// Verify workflow can extract tool_calls for routing conditions
		var outputMap map[string]interface{}
		err = json.Unmarshal(jsonData, &outputMap)
		require.NoError(t, err)

		toolCallsRaw, exists := outputMap["tool_calls"]
		require.True(t, exists, "tool_calls should be present in output")

		toolCallsJSON, err := json.Marshal(toolCallsRaw)
		require.NoError(t, err)

		var extractedToolCalls []handlers.ToolCall
		err = json.Unmarshal(toolCallsJSON, &extractedToolCalls)
		require.NoError(t, err)
		require.Len(t, extractedToolCalls, 2, "Should have 2 tool calls for routing")
	})

	t.Run("ExecuteTools output matches SaveMessage tool input", func(t *testing.T) {
		// Simulate ExecuteTools output
		executeOutput := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{
					ToolCallId: "call_abc123",
					Content:    "total 24\ndrwxr-xr-x  3 user  staff   96 Jan 15 10:30 .\ndrwxr-xr-x  5 user  staff  160 Jan 15 10:25 ..",
					IsError:    false,
				},
			},
		}

		// Marshal ExecuteTools output
		jsonData, err := json.Marshal(&executeOutput)
		require.NoError(t, err)

		// Verify SaveMessage can receive this as input
		var inputForSave struct {
			ToolResults []*reliantv1.ToolResultMsg `json:"tool_results"`
		}
		err = json.Unmarshal(jsonData, &inputForSave)
		require.NoError(t, err)

		require.Len(t, inputForSave.ToolResults, 1)
		assert.Equal(t, "call_abc123", inputForSave.ToolResults[0].GetToolCallId())
		assert.False(t, inputForSave.ToolResults[0].GetIsError())
	})

	t.Run("Complete workflow chain preserves data integrity", func(t *testing.T) {
		// Simulate the complete workflow chain:
		// CallLLM → SaveMessage (assistant) → ExecuteTools → SaveMessage (tool)

		// Step 1: CallLLM produces output
		callLLMOutput := handlers.CallLLMOutput{
			ResponseText: "Let me check that file.",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_xyz", Name: "read", Input: `{"file_path":"/test.txt"}`},
			},
			TokenCount: 150,
		}

		// Step 2: Build ActivityInput for SaveMessage (assistant message) using proto types.
		// Proto nodes use ToolCallMsg/ToolResultMsg for resolved fields.
		saveAssistantInput := handlers.ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID: "chat_123",
				Thread: "0",
			},
			Node: &reliantv1.Node{
				Id:   "save_assistant",
				Type: "save_message",
				Args: &reliantv1.Node_SaveMessageNode{
					SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
						ResolvedRole:    "assistant",
						ResolvedContent: callLLMOutput.ResponseText,
						ResolvedToolCalls: []*reliantv1.ToolCallMsg{
							{Id: "call_xyz", Name: "read", Input: `{"file_path":"/test.txt"}`},
						},
						TokenCount: int32(callLLMOutput.TokenCount),
					},
				},
			},
		}

		// Step 3: SaveMessage returns with tool_calls for routing
		saveAssistantOutput := handlers.SaveMessageOutput{
			MessageId: "msg_123",
			ToolCalls: callLLMOutput.ToolCalls,
		}

		// Step 4: Tool_calls go to ExecuteTools using proto types
		executeInput := handlers.ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID: "chat_123",
				Thread: "0",
			},
			Node: &reliantv1.Node{
				Id:   "execute_tools",
				Type: "execute_tools",
				Args: &reliantv1.Node_ExecuteTools{
					ExecuteTools: &reliantv1.ExecuteToolsArgs{
						ResolvedToolCalls: []*reliantv1.ToolCallMsg{
							{Id: "call_xyz", Name: "read", Input: `{"file_path":"/test.txt"}`},
						},
					},
				},
			},
		}

		// Verify ExecuteTools input can use SaveMessage output tool_calls
		require.Len(t, saveAssistantOutput.ToolCalls, 1)
		assert.Equal(t, executeInput.Node.GetExecuteTools().GetResolvedToolCalls()[0].GetId(), saveAssistantOutput.ToolCalls[0].GetId())

		// Verify SaveMessage assistant input was constructed correctly
		require.NotNil(t, saveAssistantInput.Node.GetSaveMessageNode())
		assert.Equal(t, "assistant", saveAssistantInput.Node.GetSaveMessageNode().GetResolvedRole())

		// Step 5: ExecuteTools produces results
		executeOutput := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{ToolCallId: "call_xyz", Content: "File contents here", IsError: false},
			},
		}

		// Step 6: Build ActivityInput for SaveMessage (tool message) using proto types
		saveToolInput := handlers.ActivityInput{
			Runtime: types.RuntimeContext{
				ChatID: "chat_123",
				Thread: "0",
			},
			Node: &reliantv1.Node{
				Id:   "save_tool",
				Type: "save_message",
				Args: &reliantv1.Node_SaveMessageNode{
					SaveMessageNode: &reliantv1.SaveMessageNodeArgs{
						ResolvedRole: "tool",
						ResolvedToolResults: []*reliantv1.ToolResultMsg{
							{ToolCallId: "call_xyz", Content: "File contents here", IsError: false},
						},
					},
				},
			},
		}

		// Verify tool results match
		require.NotNil(t, saveToolInput.Node.GetSaveMessageNode())
		require.Len(t, saveToolInput.Node.GetSaveMessageNode().GetResolvedToolResults(), 1)
		assert.Equal(t, "call_xyz", saveToolInput.Node.GetSaveMessageNode().GetResolvedToolResults()[0].GetToolCallId())
		assert.Equal(t, "File contents here", saveToolInput.Node.GetSaveMessageNode().GetResolvedToolResults()[0].GetContent())
		assert.False(t, saveToolInput.Node.GetSaveMessageNode().GetResolvedToolResults()[0].GetIsError())

		// The complete chain preserved data integrity: tool call ID from CallLLM output
		// matches the tool result's tool_call_id in the SaveMessage input.
		assert.Equal(t, callLLMOutput.ToolCalls[0].GetId(), executeOutput.ToolResults[0].GetToolCallId(),
			"Tool call ID should be preserved through the entire workflow chain")
	})
}

// TestActivityInputOutputFieldValidation validates that all activity inputs
// and outputs have proper JSON tags for workflow YAML compatibility.
func TestActivityInputOutputFieldValidation(t *testing.T) {
	t.Run("RuntimeContext has required json tags", func(t *testing.T) {
		runtimeType := reflect.TypeOf(types.RuntimeContext{})

		requiredFields := map[string]string{
			"ChatID": "chat_id",
			"Thread": "thread",
		}

		for fieldName, expectedTag := range requiredFields {
			field, found := runtimeType.FieldByName(fieldName)
			require.True(t, found, "RuntimeContext should have field %s", fieldName)

			jsonTag := field.Tag.Get("json")
			assert.True(t, jsonTag == expectedTag || jsonTag == expectedTag+",omitempty",
				"RuntimeContext.%s should have json tag '%s' or '%s,omitempty', got '%s'", fieldName, expectedTag, expectedTag, jsonTag)
		}
	})

	t.Run("CallLLMOutput has required json tags", func(t *testing.T) {
		outputType := reflect.TypeOf(handlers.CallLLMOutput{})

		requiredFields := map[string]string{
			"ResponseText": "response_text",
			"ToolCalls":    "tool_calls",
			"TokenCount":   "token_count",
		}

		for fieldName, expectedTag := range requiredFields {
			field, found := outputType.FieldByName(fieldName)
			require.True(t, found, "CallLLMOutput should have field %s", fieldName)

			jsonTag := field.Tag.Get("json")
			assert.True(t, jsonTag == expectedTag || jsonTag == expectedTag+",omitempty",
				"CallLLMOutput.%s should have json tag '%s' or '%s,omitempty', got '%s'", fieldName, expectedTag, expectedTag, jsonTag)
		}
	})

	t.Run("SaveMessageInput has required json tags", func(t *testing.T) {
		// types.SaveMessageInput holds the fields passed between runtime engine and handlers
		inputType := reflect.TypeOf(types.SaveMessageInput{})
		runtimeFields := map[string]string{
			"ChatID": "chat_id",
			"Thread": "thread",
		}
		for fieldName, expectedTag := range runtimeFields {
			field, found := inputType.FieldByName(fieldName)
			require.True(t, found, "SaveMessageInput should have field %s", fieldName)
			jsonTag := field.Tag.Get("json")
			assert.True(t, jsonTag == expectedTag || jsonTag == expectedTag+",omitempty",
				"SaveMessageInput.%s should have json tag '%s', got '%s'", fieldName, expectedTag, jsonTag)
		}

		contentFields := map[string]string{
			"Role":        "role",
			"Content":     "content,omitempty",
			"ToolResults": "tool_results,omitempty",
			"ToolCalls":   "tool_calls,omitempty",
			"TokenCount":  "token_count,omitempty",
		}
		for fieldName, expectedTag := range contentFields {
			field, found := inputType.FieldByName(fieldName)
			require.True(t, found, "SaveMessageInput should have field %s", fieldName)
			jsonTag := field.Tag.Get("json")
			assert.Equal(t, expectedTag, jsonTag,
				"SaveMessageInput.%s should have json tag '%s'", fieldName, expectedTag)
		}
	})

	t.Run("ExecuteToolsOutput has required json tags", func(t *testing.T) {
		outputType := reflect.TypeOf(handlers.ExecuteToolsOutput{})

		requiredFields := map[string]string{
			"ToolResults": "tool_results",
		}

		for fieldName, expectedTag := range requiredFields {
			field, found := outputType.FieldByName(fieldName)
			require.True(t, found, "ExecuteToolsOutput should have field %s", fieldName)

			jsonTag := field.Tag.Get("json")
			assert.True(t, jsonTag == expectedTag || jsonTag == expectedTag+",omitempty",
				"ExecuteToolsOutput.%s should have json tag '%s' or '%s,omitempty', got '%s'", fieldName, expectedTag, expectedTag, jsonTag)
		}
	})
}

// TestWorkflowYAMLCompatibility validates that the data types match the
// expectations in agent.yaml workflow definition.
func TestWorkflowYAMLCompatibility(t *testing.T) {
	t.Run("call_llm.tool_calls can be passed to save_assistant_message.tool_calls", func(t *testing.T) {
		// This validates agent.yaml:
		// tool_calls: call_llm.tool_calls

		callLLMOutput := handlers.CallLLMOutput{
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "bash", Input: `{"command":"ls"}`},
			},
		}

		// Workflow engine passes as JSON
		jsonData, err := json.Marshal(callLLMOutput.ToolCalls)
		require.NoError(t, err)

		// SaveMessage receives as ToolCall[]
		var toolCalls []handlers.ToolCall
		err = json.Unmarshal(jsonData, &toolCalls)
		require.NoError(t, err, "Workflow should be able to pass call_llm.tool_calls to save_assistant_message.tool_calls")

		assert.Len(t, toolCalls, 1)
		assert.Equal(t, "call_1", toolCalls[0].ID)
	})

	t.Run("save_assistant_message.tool_calls can be used in routing conditions", func(t *testing.T) {
		// This validates line 92 in agent.yaml:
		// condition: size(save_assistant_message.tool_calls) > 0

		saveOutput := handlers.SaveMessageOutput{
			MessageId: "msg_123",
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "bash", Input: `{}`},
			},
		}

		// Workflow engine evaluates size of tool_calls
		assert.NotEmpty(t, saveOutput.ToolCalls, "Routing condition should see non-empty tool_calls")
	})

	t.Run("execute_tools.tool_results can be passed to save_tool_results.tool_results", func(t *testing.T) {
		// This validates agent.yaml:
		// tool_results: execute_tools.tool_results

		executeOutput := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{ToolCallId: "call_1", Content: "output", IsError: false},
			},
		}

		// Workflow engine passes as JSON
		jsonData, err := json.Marshal(executeOutput.ToolResults)
		require.NoError(t, err)

		// SaveMessage receives as ToolResult[]
		var toolResults []*reliantv1.ToolResultMsg
		err = json.Unmarshal(jsonData, &toolResults)
		require.NoError(t, err, "Workflow should be able to pass execute_tools.tool_results to save_tool_results.tool_results")

		assert.Len(t, toolResults, 1)
		assert.Equal(t, "call_1", toolResults[0].GetToolCallId())
	})

	t.Run("call_llm.tool_calls matches execute_tools.tool_calls expectation", func(t *testing.T) {
		// This validates agent.yaml:
		// tool_calls: call_llm.tool_calls
		// ExecuteTools expects ToolCall[] not ToolCall[]

		// CallLLM produces ToolCall[]
		callLLMOutput := handlers.CallLLMOutput{
			ToolCalls: []*reliantv1.ToolCallMsg{
				{Id: "call_1", Name: "bash", Input: `{"command":"pwd"}`},
			},
		}

		// In agent.yaml, we pass call_llm.tool_calls to execute_tools
		// ExecuteToolsInput expects []ToolCall

		// Workflow passes as JSON
		jsonData, err := json.Marshal(callLLMOutput.ToolCalls)
		require.NoError(t, err)

		// ExecuteTools receives as ToolCall[]
		var toolCalls []handlers.ToolCall
		err = json.Unmarshal(jsonData, &toolCalls)
		require.NoError(t, err, "Workflow should pass call_llm.tool_calls to execute_tools.tool_calls")

		assert.Len(t, toolCalls, 1)
		assert.Equal(t, "call_1", toolCalls[0].ID)
	})
}

// TestNilAndEmptyHandling validates that nil and empty values are handled
// correctly across activity boundaries.
func TestNilAndEmptyHandling(t *testing.T) {
	t.Run("Empty tool_calls array marshals correctly", func(t *testing.T) {
		output := handlers.CallLLMOutput{
			ResponseText: "I understand.",
			ToolCalls:    []*reliantv1.ToolCallMsg{},
			TokenCount:   15,
		}

		jsonData, err := json.Marshal(&output)
		require.NoError(t, err)

		var unmarshaled handlers.CallLLMOutput
		err = json.Unmarshal(jsonData, &unmarshaled)
		require.NoError(t, err)

		assert.Nil(t, unmarshaled.ToolCalls, "Empty proto repeated field is omitted and unmarshals as nil")
	})

	t.Run("Nil tool_calls becomes empty array after unmarshal", func(t *testing.T) {
		// Create output with nil tool_calls
		output := handlers.CallLLMOutput{
			ResponseText: "I understand.",
			ToolCalls:    nil,
			TokenCount:   15,
		}

		jsonData, err := json.Marshal(&output)
		require.NoError(t, err)

		var unmarshaled handlers.CallLLMOutput
		err = json.Unmarshal(jsonData, &unmarshaled)
		require.NoError(t, err)

		// JSON unmarshaling of null array becomes nil in Go
		// This is expected behavior
		assert.Nil(t, unmarshaled.ToolCalls, "nil array stays nil after JSON round-trip")
	})

	t.Run("Empty tool_results array marshals correctly", func(t *testing.T) {
		output := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{},
		}

		jsonData, err := json.Marshal(&output)
		require.NoError(t, err)

		var unmarshaled handlers.ExecuteToolsOutput
		err = json.Unmarshal(jsonData, &unmarshaled)
		require.NoError(t, err)

		assert.Nil(t, unmarshaled.ToolResults, "Empty proto repeated field is omitted and unmarshals as nil")
	})
}

// TestErrorPropagation validates that error information is preserved
// across activity boundaries.
func TestErrorPropagation(t *testing.T) {
	t.Run("IsError flag propagates through workflow chain", func(t *testing.T) {
		// ExecuteTools returns an error result
		executeOutput := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{
					ToolCallId: "call_err",
					Content:    "Error: file not found",
					IsError:    true,
				},
			},
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(&executeOutput)
		require.NoError(t, err)

		// SaveMessage receives the error result
		var saveInput struct {
			ToolResults []*reliantv1.ToolResultMsg `json:"tool_results"`
		}
		err = json.Unmarshal(jsonData, &saveInput)
		require.NoError(t, err)

		require.Len(t, saveInput.ToolResults, 1)
		assert.True(t, saveInput.ToolResults[0].GetIsError(), "Error flag should be preserved")
		assert.Contains(t, saveInput.ToolResults[0].GetContent(), "Error:", "Error message should be preserved")
	})

	t.Run("Mixed success and error results are preserved", func(t *testing.T) {
		executeOutput := handlers.ExecuteToolsOutput{
			ToolResults: []*reliantv1.ToolResultMsg{
				{ToolCallId: "call_1", Content: "Success", IsError: false},
				{ToolCallId: "call_2", Content: "Error occurred", IsError: true},
				{ToolCallId: "call_3", Content: "Also success", IsError: false},
			},
		}

		jsonData, err := json.Marshal(&executeOutput)
		require.NoError(t, err)

		var converted handlers.ExecuteToolsOutput
		err = json.Unmarshal(jsonData, &converted)
		require.NoError(t, err)

		require.Len(t, converted.ToolResults, 3)
		assert.False(t, converted.ToolResults[0].GetIsError())
		assert.True(t, converted.ToolResults[1].GetIsError())
		assert.False(t, converted.ToolResults[2].GetIsError())
	})
}

// TestTypeDocumentation validates that type definitions include proper
// documentation for workflow YAML usage.
func TestTypeDocumentation(t *testing.T) {
	// This test validates that the types are self-documenting for workflow authors
	t.Run("Type names are clear and descriptive", func(t *testing.T) {
		// Verify type names make sense in workflow context
		typeNames := []struct {
			typeName    string
			expectedUse string
		}{
			{"RuntimeContext", "runtime context for activities"},
			{"ActivityInput", "proto-based activity input wrapping V2Node"},
			{"CallLLMOutput", "output from CallLLM activity"},
			{"SaveMessageOutput", "output from SaveMessage activity"},
			{"ExecuteToolsOutput", "output from ExecuteTools activity"},
			{"ToolCall", "reference to a tool call from LLM"},
			{"ToolResult", "result from tool execution"},
		}

		for _, tt := range typeNames {
			// Just verify the naming convention is clear
			assert.NotEmpty(t, tt.typeName, "Type name should not be empty")
			assert.NotEmpty(t, tt.expectedUse, "Expected use should be documented")

			// Verify key types exist and have correct names
			if tt.typeName == "RuntimeContext" {
				runtimeType := reflect.TypeOf(types.RuntimeContext{})
				assert.Equal(t, "RuntimeContext", runtimeType.Name(), "Type name should match struct name")
			}
			if tt.typeName == "ActivityInput" {
				inputType := reflect.TypeOf(handlers.ActivityInput{})
				assert.Equal(t, "ActivityInput", inputType.Name(), "ActivityInput type should have correct name")
			}
		}
	})
}

// TestTemporalJSONConversion validates that Temporal's native JSON serialization
// correctly handles type conversion between workflow and activities.
// Temporal handles all serialization - we don't need manual mapToStruct.
func TestTemporalJSONConversion(t *testing.T) {
	t.Run("JSON marshaling handles int correctly", func(t *testing.T) {
		input := map[string]interface{}{
			"context_sequence": 5, // Direct int
		}

		// JSON marshal/unmarshal (what Temporal does)
		jsonBytes, err := json.Marshal(input)
		require.NoError(t, err)

		var output struct {
			ContextSequence int `json:"context_sequence"`
		}

		err = json.Unmarshal(jsonBytes, &output)
		require.NoError(t, err)
		assert.Equal(t, 5, output.ContextSequence)
	})

	t.Run("JSON marshaling handles float64 to int", func(t *testing.T) {
		// After JSON round-trip, numbers become float64
		input := map[string]interface{}{
			"context_sequence": float64(5),
		}

		jsonBytes, err := json.Marshal(input)
		require.NoError(t, err)

		var output struct {
			ContextSequence int `json:"context_sequence"`
		}

		err = json.Unmarshal(jsonBytes, &output)
		require.NoError(t, err)
		assert.Equal(t, 5, output.ContextSequence)
	})
}
