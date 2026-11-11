// Copyright (c) 2025 Reliant Labs
//
//forge:exclude-contract: workflow scenario file format and expectation checking; pure parse/compare over YAML the caller names
package scenario

import (
	"encoding/json"
	"fmt"
	"math/big"
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

func completedMatchesFailed(expected ExpectedOutcome, got string) bool {
	return expected == OutcomeCompleted && got == string(OutcomeFailed)
}

// CompletedOverFailedWarning is the warning for a scenario that expects
// `outcome: completed` of a run whose graph declared it failed. Empty when the
// expectation does not have that shape.
func CompletedOverFailedWarning(expect *Expectation, execution *ExecutionDetails) string {
	if expect == nil || !completedMatchesFailed(expect.Outcome, execution.Outcome) {
		return ""
	}
	return fmt.Sprintf("expect.outcome is \"completed\" but the run ended at node %q, which declares "+
		"outcome: failure, so the run FAILED; it still matches for compatibility — assert "+
		"`outcome: failed` to pin the verdict", execution.OutcomeNode)
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
		// A run that ends at a node declaring `outcome: failure` reports
		// "failed". Before the runner observed that verdict it reported
		// "completed", and scenarios written then assert `completed` on such
		// paths; they keep matching (the run did complete its graph), and
		// CompletedOverFailedWarning tells their author to assert `failed`.
		if execution.Outcome != expectedOutcome && !completedMatchesFailed(expect.Outcome, execution.Outcome) {
			msg := fmt.Sprintf("expected outcome %q but got %q", expectedOutcome, execution.Outcome)
			// Say why, here: the mismatch line is all a text report prints,
			// and the cause otherwise appeared only in --json.
			switch {
			case execution.Error != nil && execution.Error.Node != "":
				msg += fmt.Sprintf(" (node %q: %s)", execution.Error.Node, execution.Error.Message)
			case execution.Error != nil && execution.Error.Message != "":
				msg += fmt.Sprintf(" (%s)", execution.Error.Message)
			case execution.OutcomeNode != "":
				msg += fmt.Sprintf(" (node %q declares outcome: failure)", execution.OutcomeNode)
			}
			mismatches = append(mismatches, msg)
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
	mismatches = append(mismatches, checkOnFailureExpectation(expect.OnFailure, execution.OnFailure)...)
	mismatches = append(mismatches, checkFinallyExpectation(expect.Finally, execution.Finally)...)

	// Check workflow-level outputs (dotted paths into structured values)
	for path, expectedValue := range expect.Outputs {
		actualValue, found := lookupDottedPath(execution.WorkflowOutputs, path)
		if !found {
			mismatches = append(mismatches,
				fmt.Sprintf("workflow output %q not found (available: %v)", path, outputKeys(execution.WorkflowOutputs)))
			continue
		}
		if !deepEqual(expectedValue, actualValue) {
			mismatches = append(mismatches,
				fmt.Sprintf("workflow output %q: %s", path, valueMismatch(expectedValue, actualValue)))
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
					mismatches = append(mismatches,
						fmt.Sprintf("node %q field %q: %s", nodeID, field, valueMismatch(expectedValue, actualValue)))
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

// deepEqual compares an expected value from scenario YAML with an observed one,
// at every depth, as JSON values: numbers compare by value (YAML's 1750 is an
// int, the runtime's is a float64 after CEL or structpb), maps compare key by
// key (whatever their Go key and element types), and lists element by element
// (a []map[string]interface{} equals the same []interface{}). Map comparison
// is exact — every key on both sides — at nested levels; node_outputs and
// outputs are partial only in which top-level fields they name.
func deepEqual(expected, actual interface{}) bool {
	if expNum, ok := asNumber(expected); ok {
		actNum, ok := asNumber(actual)
		return ok && expNum.equal(actNum)
	}
	if expMap, ok := asStringKeyedMap(expected); ok {
		actMap, ok := asStringKeyedMap(actual)
		if !ok || len(expMap) != len(actMap) {
			return false
		}
		for k, ev := range expMap {
			av, present := actMap[k]
			if !present || !deepEqual(ev, av) {
				return false
			}
		}
		return true
	}
	if expList, ok := asSlice(expected); ok {
		actList, ok := asSlice(actual)
		if !ok || len(expList) != len(actList) {
			return false
		}
		for i := range expList {
			if !deepEqual(expList[i], actList[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(expected, actual)
}

// valueMismatch formats an expected/actual pair for a mismatch message. When
// the two print as the same JSON — a type difference deepEqual does not
// forgive, such as the string "1" against the number 1 nested in a list —
// the Go types are added, so the message never reads "expected X but got X".
func valueMismatch(expected, actual interface{}) string {
	expectedJSON, _ := json.Marshal(expected)
	actualJSON, _ := json.Marshal(actual)
	msg := fmt.Sprintf("expected %s but got %s", expectedJSON, actualJSON)
	if string(expectedJSON) == string(actualJSON) {
		msg += fmt.Sprintf(" (same JSON, different types: expected %T, got %T)", expected, actual)
	}
	return msg
}

// number is a Go numeric value as deepEqual compares it: an integer kind is
// held exactly (signed and unsigned alike, so uint64 values above 2^63 and
// int64 values above 2^53 keep every bit), a float as float64.
type number struct {
	isInt bool
	i     *big.Int
	f     float64
}

// equal compares two integers exactly, and falls back to float64 only when
// at least one side is a float — so distinct integers past float64's exact
// range (2^53) never collapse to the same rounded value.
func (n number) equal(o number) bool {
	if n.isInt && o.isInt {
		return n.i.Cmp(o.i) == 0
	}
	return n.float64() == o.float64()
}

func (n number) float64() float64 {
	if n.isInt {
		f, _ := new(big.Float).SetInt(n.i).Float64()
		return f
	}
	return n.f
}

// asNumber converts any Go numeric value to a number.
func asNumber(v interface{}) (number, bool) {
	switch n := v.(type) {
	case int:
		return intNumber(int64(n)), true
	case int8:
		return intNumber(int64(n)), true
	case int16:
		return intNumber(int64(n)), true
	case int32:
		return intNumber(int64(n)), true
	case int64:
		return intNumber(n), true
	case uint:
		return uintNumber(uint64(n)), true
	case uint8:
		return uintNumber(uint64(n)), true
	case uint16:
		return uintNumber(uint64(n)), true
	case uint32:
		return uintNumber(uint64(n)), true
	case uint64:
		return uintNumber(n), true
	case float64:
		return number{f: n}, true
	case float32:
		return number{f: float64(n)}, true
	case json.Number:
		if i, ok := new(big.Int).SetString(string(n), 10); ok {
			return number{isInt: true, i: i}, true
		}
		f, err := n.Float64()
		return number{f: f}, err == nil
	default:
		return number{}, false
	}
}

func intNumber(i int64) number   { return number{isInt: true, i: big.NewInt(i)} }
func uintNumber(u uint64) number { return number{isInt: true, i: new(big.Int).SetUint64(u)} }

// checkFinallyExpectation compares an `expect.finally` block with what the
// run's handler did. A workflow with no handler has nothing observed, which is
// "did not run".
func checkFinallyExpectation(want *FinallyExpectation, got *FinallyObserved) []string {
	if want == nil {
		return nil
	}
	var out []string
	ran := got != nil && got.Ran
	if want.Ran != nil && *want.Ran != ran {
		out = append(out, fmt.Sprintf("expected finally ran=%t but it ran=%t", *want.Ran, ran))
	}
	if !ran {
		if want.Status != "" || want.HandlerFailed != nil {
			out = append(out, "expected the finally handler to have run, but it did not")
		}
		return out
	}
	if want.Status != "" && got.Status != want.Status {
		out = append(out, fmt.Sprintf("expected run.status %q but got %q", want.Status, got.Status))
	}
	if want.HandlerFailed != nil && *want.HandlerFailed != (got.HandlerError != "") {
		out = append(out, fmt.Sprintf("expected the finally handler to have failed=%t but its error was %q", *want.HandlerFailed, got.HandlerError))
	}
	return out
}

// checkOnFailureExpectation compares an `expect.on_failure` block with what the
// run's handler did. A workflow with no handler has nothing observed, which is
// "did not run".
func checkOnFailureExpectation(want *OnFailureExpectation, got *OnFailureObserved) []string {
	if want == nil {
		return nil
	}
	var out []string
	ran := got != nil && got.Ran
	if want.Ran != nil && *want.Ran != ran {
		out = append(out, fmt.Sprintf("expected on_failure ran=%t but it ran=%t", *want.Ran, ran))
	}
	needsRun := want.NodeID != "" || want.NodePath != "" || want.ErrorCode != "" || want.Attempt != 0 ||
		want.ErrorContains != "" || want.HandlerFailed != nil
	if !ran {
		if needsRun {
			out = append(out, "expected the on_failure handler to have run, but it did not")
		}
		return out
	}
	if want.NodeID != "" && got.NodeID != want.NodeID {
		out = append(out, fmt.Sprintf("expected failure.node_id %q but got %q", want.NodeID, got.NodeID))
	}
	if want.NodePath != "" && got.NodePath != want.NodePath {
		out = append(out, fmt.Sprintf("expected failure.node_path %q but got %q", want.NodePath, got.NodePath))
	}
	if want.ErrorCode != "" && got.ErrorCode != want.ErrorCode {
		out = append(out, fmt.Sprintf("expected failure.error_code %q but got %q", want.ErrorCode, got.ErrorCode))
	}
	if want.Attempt != 0 && got.Attempt != want.Attempt {
		out = append(out, fmt.Sprintf("expected failure.attempt %d but got %d", want.Attempt, got.Attempt))
	}
	if want.ErrorContains != "" && !strings.Contains(got.Error, want.ErrorContains) {
		out = append(out, fmt.Sprintf("expected failure.error to contain %q but it was %q", want.ErrorContains, got.Error))
	}
	if want.HandlerFailed != nil && *want.HandlerFailed != (got.HandlerError != "") {
		out = append(out, fmt.Sprintf("expected the handler to have failed=%t but its error was %q", *want.HandlerFailed, got.HandlerError))
	}
	return out
}
