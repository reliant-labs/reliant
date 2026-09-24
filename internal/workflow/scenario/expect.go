// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// EventOutput converts a SimulatedEvent into the activity output shape it
// stands for. Typed mode takes precedence: when `type:` is set, the event is
// converted to the corresponding activity output shape. For
// `type: tool_result`, the `output:` field is the tool's result payload (the
// documented shorthand for `tool_output:`), NOT a raw activity output. Raw
// output mode only applies to events without a type.
func EventOutput(event SimulatedEvent) map[string]interface{} {
	switch event.Type {
	case "llm_response":
		return llmResponseOutput(event)
	case "tool_result":
		return toolResultOutput(event)
	}

	// Raw output mode (no type) - pass through directly
	if event.Output != nil {
		return event.Output
	}

	return make(map[string]interface{})
}

// llmResponseOutput converts an llm_response event to CallLLM output format.
func llmResponseOutput(event SimulatedEvent) map[string]interface{} {
	// Convert SimToolCall to message.ToolCall
	var toolCalls []message.ToolCall
	for _, tc := range event.ToolCalls {
		inputJSON, _ := json.Marshal(tc.Input)
		toolCalls = append(toolCalls, message.ToolCall{
			ID:    uuid.New().String(),
			Name:  tc.Name,
			Input: string(inputJSON),
		})
	}

	// Build output as map directly (mirrors CallLLMOutput fields)
	result := map[string]interface{}{
		"message": map[string]interface{}{
			"id":   uuid.New().String(),
			"role": "assistant",
			"text": event.Text,
		},
		"response_text": event.Text,
	}
	if len(toolCalls) > 0 {
		// Marshal tool calls through JSON to get []map[string]interface{}
		data, _ := json.Marshal(toolCalls)
		var tcMaps []interface{}
		_ = json.Unmarshal(data, &tcMaps)
		result["tool_calls"] = tcMaps
	}
	return result
}

// toolResultOutput converts a tool_result event to ExecuteTools output format.
//
// The tool's result payload comes from `tool_output:` or, as documented shorthand,
// `output:`. Mirroring the real ExecuteTools handler, response_data is keyed by
// tool name (responseData[toolName] = parsed result) so workflows can index
// nodes.<id>.response_data[<response_tool_name>].
// executeToolsActivityFields are ExecuteTools OUTPUT-level fields (not tool payload
// fields). When present in a tool_result event's payload they are promoted to the
// top-level activity output — mirroring the real activity, where the tool's result
// lives in tool_results/response_data while these live on the output itself.
var executeToolsActivityFields = map[string]bool{
	"thread_token_count": true,
	"total_result_chars": true,
}

func toolResultOutput(event SimulatedEvent) map[string]interface{} {
	toolOutput := event.ToolOutput
	if toolOutput == nil {
		toolOutput = event.Output
	}

	// Split activity-level fields out of the tool payload.
	var activityFields map[string]interface{}
	if len(toolOutput) > 0 {
		payload := make(map[string]interface{}, len(toolOutput))
		for k, v := range toolOutput {
			if executeToolsActivityFields[k] {
				if activityFields == nil {
					activityFields = make(map[string]interface{})
				}
				activityFields[k] = v
				continue
			}
			payload[k] = v
		}
		toolOutput = payload
	}

	var toolResults []message.ToolResult
	if event.Tool != "" {
		contentJSON, _ := json.Marshal(toolOutput)
		toolResults = append(toolResults, message.ToolResult{
			ToolCallID: uuid.New().String(),
			Name:       event.Tool,
			Content:    string(contentJSON),
			IsError:    event.IsError,
		})
	}

	// Build output as map directly (mirrors ExecuteToolsOutput fields)
	result := map[string]interface{}{
		"message": map[string]interface{}{
			"id":   uuid.New().String(),
			"role": "user",
		},
	}
	if len(toolResults) > 0 {
		data, _ := json.Marshal(toolResults)
		var trMaps []interface{}
		_ = json.Unmarshal(data, &trMaps)
		result["tool_results"] = trMaps
	}
	if event.Tool != "" {
		// Keyed by tool name, exactly like the real ExecuteTools activity.
		// A failed tool call leaves response_data[tool] null — the real handler
		// only populates it from successful response tool metadata, while the
		// expected_response_tools contract pre-creates the key as null.
		var responseValue interface{}
		if !event.IsError {
			responseValue = toolOutput
		}
		result["response_data"] = map[string]interface{}{
			event.Tool: responseValue,
		}
	}
	for k, v := range activityFields {
		result[k] = v
	}
	return result
}

// CheckExpectations compares execution results against expectations and returns
// one message per mismatch (empty means the expectation held).
//
// It reads only ExecutionDetails — what the run recorded — so assertion logic
// stays independent of how the run was executed.
func CheckExpectations(expect *Expectation, execution *ExecutionDetails) []string {
	var mismatches []string

	// Check outcome
	if expect.Outcome != "" {
		expectedOutcome := string(expect.Outcome)
		if execution.Outcome != expectedOutcome {
			mismatches = append(mismatches,
				fmt.Sprintf("expected outcome %q but got %q", expectedOutcome, execution.Outcome))
		}
	}

	// Check reached nodes
	reachedSet := make(map[string]bool)
	for _, node := range execution.NodesReached {
		reachedSet[node] = true
	}

	for _, expected := range expect.Reached {
		if !reachedSet[expected] {
			mismatches = append(mismatches,
				fmt.Sprintf("expected node %q to be reached but it wasn't (reached: %v)", expected, execution.NodesReached))
		}
	}

	// Check not-reached nodes
	for _, notExpected := range expect.NotReached {
		if reachedSet[notExpected] {
			mismatches = append(mismatches,
				fmt.Sprintf("expected node %q NOT to be reached but it was", notExpected))
		}
	}

	// Check completed nodes
	completedSet := make(map[string]bool)
	for _, node := range execution.NodesCompleted {
		completedSet[node] = true
	}

	for _, expected := range expect.Completed {
		if !completedSet[expected] {
			mismatches = append(mismatches,
				fmt.Sprintf("expected node %q to be completed but it wasn't (completed: %v)", expected, execution.NodesCompleted))
		}
	}

	// Check skipped nodes
	skippedSet := make(map[string]bool)
	for _, node := range execution.NodesSkipped {
		skippedSet[node] = true
	}

	for _, expected := range expect.Skipped {
		if !skippedSet[expected] {
			mismatches = append(mismatches,
				fmt.Sprintf("expected node %q to be skipped but it wasn't (skipped: %v)", expected, execution.NodesSkipped))
		}
	}

	// Check error contains
	if expect.ErrorContains != "" {
		if execution.Error == nil {
			mismatches = append(mismatches,
				fmt.Sprintf("expected error containing %q but no error occurred", expect.ErrorContains))
		} else if !strings.Contains(execution.Error.Message, expect.ErrorContains) {
			mismatches = append(mismatches,
				fmt.Sprintf("expected error containing %q but got %q", expect.ErrorContains, execution.Error.Message))
		}
	}

	// Check error node
	if expect.ErrorNode != "" {
		if execution.Error == nil {
			mismatches = append(mismatches,
				fmt.Sprintf("expected error at node %q but no error occurred", expect.ErrorNode))
		} else if execution.Error.Node != expect.ErrorNode {
			mismatches = append(mismatches,
				fmt.Sprintf("expected error at node %q but error occurred at %q", expect.ErrorNode, execution.Error.Node))
		}
	}

	mismatches = append(mismatches, checkMessageExpectations(expect.Messages, execution.SavedMessages)...)

	// Check workflow-level outputs (dotted paths into structured values)
	for path, expectedValue := range expect.Outputs {
		actualValue, found := lookupDottedPath(execution.WorkflowOutputs, path)
		if !found {
			mismatches = append(mismatches,
				fmt.Sprintf("workflow output %q not found (available: %v)", path, outputKeys(execution.WorkflowOutputs)))
			continue
		}
		if !deepEqual(expectedValue, actualValue) {
			expectedJSON, _ := json.Marshal(expectedValue)
			actualJSON, _ := json.Marshal(actualValue)
			mismatches = append(mismatches,
				fmt.Sprintf("workflow output %q: expected %s but got %s", path, expectedJSON, actualJSON))
		}
	}

	// Check node outputs
	if len(expect.NodeOutputs) > 0 {
		for nodeID, expectedOutputs := range expect.NodeOutputs {
			actualOutputs, exists := execution.NodeOutputs[nodeID]
			if !exists {
				mismatches = append(mismatches,
					fmt.Sprintf("expected outputs for node %q but node was not reached", nodeID))
				continue
			}

			// Check each expected field
			for field, expectedValue := range expectedOutputs {
				actualValue, hasField := actualOutputs[field]
				if !hasField {
					mismatches = append(mismatches,
						fmt.Sprintf("node %q: expected field %q but it doesn't exist", nodeID, field))
					continue
				}

				// Deep compare values
				if !deepEqual(expectedValue, actualValue) {
					expectedJSON, _ := json.Marshal(expectedValue)
					actualJSON, _ := json.Marshal(actualValue)
					mismatches = append(mismatches,
						fmt.Sprintf("node %q field %q: expected %s but got %s", nodeID, field, expectedJSON, actualJSON))
				}
			}
		}
	}

	return mismatches
}

// lookupDottedPath resolves a dotted path (e.g., "response.choice") into nested maps.
// Returns the value and whether the full path resolved.
func lookupDottedPath(root map[string]interface{}, path string) (interface{}, bool) {
	if root == nil {
		return nil, false
	}
	segments := strings.Split(path, ".")
	var current interface{} = root
	for _, seg := range segments {
		currentMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = currentMap[seg]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// outputKeys returns the top-level keys of a workflow outputs map for error messages.
func outputKeys(outputs map[string]interface{}) []string {
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// deepEqual compares two values for equality, handling JSON number type issues
func deepEqual(expected, actual interface{}) bool {
	// Handle numeric comparisons (JSON numbers may be float64)
	if reflect.TypeOf(expected) != reflect.TypeOf(actual) {
		// Try numeric comparison
		expNum, expOk := toFloat64(expected)
		actNum, actOk := toFloat64(actual)
		if expOk && actOk {
			return expNum == actNum
		}
	}
	return reflect.DeepEqual(expected, actual)
}

// toFloat64 converts a numeric value to float64
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	default:
		return 0, false
	}
}
