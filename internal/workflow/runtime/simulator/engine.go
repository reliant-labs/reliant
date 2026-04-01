// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Engine runs workflow simulations with mocked events
type Engine struct {
	workflow *reliantv1.Workflow
}

// NewEngine creates a new simulation engine for a workflow
func NewEngine(workflow *reliantv1.Workflow) *Engine {
	return &Engine{
		workflow: workflow,
	}
}

// NewEngineFromYAML creates a new simulation engine from workflow YAML
func NewEngineFromYAML(workflowYAML string) (*Engine, error) {
	wf, err := v2.ParseWorkflowProtoBytes([]byte(workflowYAML))
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow: %w", err)
	}
	return NewEngine(wf), nil
}

// RunScenario runs a single scenario and returns the result.
// Validates the scenario before running - returns early with errors if invalid.
func (e *Engine) RunScenario(scenario *Scenario) *ScenarioResult {
	startTime := time.Now()

	// Validate scenario before running
	validation := ValidateScenario(scenario, e.workflow)
	if !validation.Valid {
		return &ScenarioResult{
			Status:   StatusError,
			Scenario: scenario.Name,
			Execution: ExecutionDetails{
				Outcome: "error",
				Error: &ErrorDetails{
					Message: "validation failed: " + strings.Join(validation.Errors, "; "),
				},
				DurationMs: time.Since(startTime).Milliseconds(),
			},
			Mismatches: validation.Errors,
			RunAt:      time.Now(),
		}
	}

	// Build workflow inputs from scenario
	inputs := make(map[string]interface{})
	for k, v := range scenario.Inputs {
		inputs[k] = v
	}

	// Apply workflow input defaults from schema
	// This fills in default values for any inputs not provided by the scenario
	// ApplyDefaults produces nested structure directly for CEL access
	if len(e.workflow.GetInputs()) > 0 {
		inputs = v2.ApplyDefaults(inputs, e.workflow.GetInputs())
	}

	// Set defaults for common system inputs
	if _, ok := inputs["chat_id"]; !ok {
		inputs["chat_id"] = "sim-chat"
	}
	if _, ok := inputs["thread"]; !ok {
		inputs["thread"] = "0"
	}

	// Build hasInternalEvents function from scenario events
	hasInternalEvents := e.buildHasInternalEvents(scenario.Events)

	// Create simulator config
	config := v2.SimulatorConfig{
		WorkflowInputs:    inputs,
		MaxIterations:     100,
		StartAt:           scenario.StartAt,
		InitialState:      scenario.State,
		HasInternalEvents: hasInternalEvents,
		WorkflowLoader:    loadBuiltinWorkflow,
	}

	// Create the simulator
	sim := v2.NewWorkflowSimulator(e.workflow, config)

	// Build the mocker from events
	mocker, mockerState := e.buildMocker(scenario.Events)

	// Run the simulation
	runErr := sim.Run(mocker)

	// Collect actual node outputs for assertions
	actualNodeOutputs := make(map[string]map[string]interface{})
	for nodeID, output := range sim.GetNodeOutputs() {
		if outputMap, ok := output.(map[string]interface{}); ok {
			actualNodeOutputs[nodeID] = outputMap
		}
	}

	// Build node states map (convert from v2.NodeState to NodeExecutionState)
	nodeStates := make(map[string]NodeExecutionState)
	for nodeID, state := range sim.GetNodeStates() {
		switch state {
		case v2.NodeStateCompleted:
			nodeStates[nodeID] = StateCompleted
		case v2.NodeStateSkipped:
			nodeStates[nodeID] = StateSkipped
		case v2.NodeStateError:
			nodeStates[nodeID] = StateError
		}
	}

	// Build execution details
	execution := ExecutionDetails{
		NodesReached:   sim.GetVisitedSteps(),
		NodesCompleted: sim.GetCompletedSteps(),
		NodesSkipped:   sim.GetSkippedSteps(),
		NodeStates:     nodeStates,
		DurationMs:     time.Since(startTime).Milliseconds(),
		NodeOutputs:    actualNodeOutputs,
	}

	if runErr != nil {
		execution.Outcome = "error"
		execution.Error = parseError(runErr)
	} else {
		execution.Outcome = "completed"
	}

	// Compare against expectations
	result := &ScenarioResult{
		Scenario:  scenario.Name,
		Execution: execution,
		Expected:  scenario.Expect,
		RunAt:     time.Now(),
	}

	// Check for unconsumed events - indicates misconfigured scenario
	unconsumed := mockerState.UnconsumedEvents()
	var mismatches []string
	if len(unconsumed) > 0 {
		for _, event := range unconsumed {
			if event.Node != "" {
				mismatches = append(mismatches,
					fmt.Sprintf("event targeting %q was never consumed (node may not exist or wasn't reached)", event.Node))
			} else {
				mismatches = append(mismatches, "sequential event was never consumed")
			}
		}
	}

	// Determine pass/fail
	if scenario.Expect != nil {
		mismatches = append(mismatches, e.checkExpectations(scenario.Expect, &execution)...)
		result.Mismatches = mismatches
		if len(mismatches) > 0 {
			result.Status = StatusFailed
		} else {
			result.Status = StatusPassed
		}
	} else {
		// No expectations = pass if completed without error and no unconsumed events
		result.Mismatches = mismatches
		if runErr != nil {
			result.Status = StatusError
		} else if len(mismatches) > 0 {
			result.Status = StatusFailed
		} else {
			result.Status = StatusPassed
		}
	}

	return result
}

// mockerState tracks event consumption for diagnostics
type mockerState struct {
	nodeEvents       map[string][]SimulatedEvent
	sequentialEvents []SimulatedEvent
	nodeEventIndex   map[string]int
	sequentialIndex  int
}

// UnconsumedEvents returns events that were never consumed during simulation.
// This helps diagnose misconfigured scenarios where events target wrong nodes.
func (m *mockerState) UnconsumedEvents() []SimulatedEvent {
	var unconsumed []SimulatedEvent

	// Check node-targeted events
	for node, events := range m.nodeEvents {
		consumedCount := m.nodeEventIndex[node]
		for i := consumedCount; i < len(events); i++ {
			unconsumed = append(unconsumed, events[i])
		}
	}

	// Check sequential events
	for i := m.sequentialIndex; i < len(m.sequentialEvents); i++ {
		unconsumed = append(unconsumed, m.sequentialEvents[i])
	}

	return unconsumed
}

// buildMocker creates a StepMocker function from simulated events
func (e *Engine) buildMocker(events []SimulatedEvent) (v2.StepMocker, *mockerState) {
	state := &mockerState{
		nodeEvents:     make(map[string][]SimulatedEvent),
		nodeEventIndex: make(map[string]int),
	}

	for _, event := range events {
		if event.Node != "" {
			state.nodeEvents[event.Node] = append(state.nodeEvents[event.Node], event)
		} else {
			state.sequentialEvents = append(state.sequentialEvents, event)
		}
	}

	mocker := func(stepID string, inputs map[string]interface{}) map[string]interface{} {
		// Check for node-specific events first
		if events, ok := state.nodeEvents[stepID]; ok {
			idx := state.nodeEventIndex[stepID]
			if idx < len(events) {
				state.nodeEventIndex[stepID] = idx + 1
				return e.eventToOutput(events[idx])
			}
		}

		// Fall back to sequential events
		if state.sequentialIndex < len(state.sequentialEvents) {
			event := state.sequentialEvents[state.sequentialIndex]
			state.sequentialIndex++
			return e.eventToOutput(event)
		}

		// Default: return empty output
		return map[string]interface{}{}
	}

	return mocker, state
}

// eventToOutput converts a SimulatedEvent to the mock output format.
// Handles both raw Output mode and typed mode (llm_response, tool_result).
func (e *Engine) eventToOutput(event SimulatedEvent) map[string]interface{} {
	// Raw output mode - pass through directly
	if event.Output != nil {
		return event.Output
	}

	// Typed mode - convert to proper output structure
	switch event.Type {
	case "llm_response":
		return e.buildLLMResponseOutput(event)
	case "tool_result":
		return e.buildToolResultOutput(event)
	default:
		return make(map[string]interface{})
	}
}

// buildLLMResponseOutput converts an llm_response event to CallLLM output format.
func (e *Engine) buildLLMResponseOutput(event SimulatedEvent) map[string]interface{} {
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

// buildToolResultOutput converts a tool_result event to ExecuteTools output format.
func (e *Engine) buildToolResultOutput(event SimulatedEvent) map[string]interface{} {
	var toolResults []message.ToolResult
	if event.Tool != "" {
		contentJSON, _ := json.Marshal(event.ToolOutput)
		toolResults = append(toolResults, message.ToolResult{
			ToolCallID: uuid.New().String(),
			Name:       event.Tool,
			Content:    string(contentJSON),
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
	if event.ToolOutput != nil {
		result["response_data"] = event.ToolOutput
	}
	return result
}

// checkExpectations compares execution results against expectations
func (e *Engine) checkExpectations(expect *Expectation, execution *ExecutionDetails) []string {
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

// parseError extracts error details from a simulation error
func parseError(err error) *ErrorDetails {
	errMsg := err.Error()

	details := &ErrorDetails{
		Message: errMsg,
	}

	// Try to extract node name from common error patterns
	// Pattern: "failed to evaluate config for node X: ..."
	if strings.Contains(errMsg, "failed to evaluate config for node") {
		parts := strings.SplitN(errMsg, "node ", 2)
		if len(parts) > 1 {
			nodePart := strings.SplitN(parts[1], ":", 2)
			if len(nodePart) > 0 {
				details.Node = strings.TrimSpace(nodePart[0])
			}
		}
	}

	// Pattern: "CEL evaluation error" or "CEL evaluation failed"
	if strings.Contains(errMsg, "CEL evaluation") {
		details.Step = "CEL evaluation"
	}

	// Pattern: "template evaluation failed"
	if strings.Contains(errMsg, "template evaluation failed") {
		details.Step = "template evaluation"
	}

	return details
}

// RunScenarios runs multiple scenarios and returns all results
func (e *Engine) RunScenarios(scenarios []*Scenario) []*ScenarioResult {
	results := make([]*ScenarioResult, len(scenarios))
	for i, scenario := range scenarios {
		results[i] = e.RunScenario(scenario)
	}
	return results
}

// buildHasInternalEvents creates a function that checks if there are events
// with qualified IDs starting with the given prefix.
// This enables internal node mocking for workflow nodes.
func (e *Engine) buildHasInternalEvents(events []SimulatedEvent) func(string) bool {
	// Pre-compute all event prefixes for efficient lookup
	prefixes := make(map[string]bool)
	for _, event := range events {
		if event.Node != "" && strings.Contains(event.Node, ".") {
			// Extract all possible prefixes
			// e.g., "a.b.c" -> "a.", "a.b."
			parts := strings.Split(event.Node, ".")
			for i := 1; i < len(parts); i++ {
				prefix := strings.Join(parts[:i], ".") + "."
				prefixes[prefix] = true
			}
		}
	}

	return func(prefix string) bool {
		return prefixes[prefix]
	}
}

// loadBuiltinWorkflow loads a workflow from the builtin workflows.
// Supports refs like "builtin://agent" or just "agent".
func loadBuiltinWorkflow(ref string) (*reliantv1.Workflow, error) {
	// Strip builtin:// prefix if present
	name := strings.TrimPrefix(ref, "builtin://")

	// Try to load from builtin FS
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	if err != nil {
		// Try without .yaml extension (in case it's already included)
		data, err = builtin.BuiltinWorkflowsFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("workflow %q not found in builtins: %w", ref, err)
		}
	}

	return wfyaml.ParseWorkflow(data)
}
