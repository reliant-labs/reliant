// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"encoding/json"
	"time"
)

// SimulatedEvent represents a single mocked event in a test scenario.
//
// Events target specific nodes and provide mock outputs. There are two modes:
//  1. Raw output mode: Set Output directly with the activity's output structure
//  2. Typed mode: Set Type with typed fields (text, tool_calls, tool_output)
//
// Typed mode is automatically converted to the appropriate output structure.
//
// Example - LLM response with text:
//
//   - node: call_llm
//     type: llm_response
//     text: "Hello, how can I help you?"
//
// Example - LLM response with tool calls:
//
//   - node: agent_loop.call_llm
//     type: llm_response
//     tool_calls:
//   - name: bash
//     input: {command: "ls -la"}
//
// Example - Tool result:
//
//   - node: agent_loop.execute_tools
//     type: tool_result
//     tool: bash
//     output: {result: "file1.txt\nfile2.txt"}
//
// Example - Raw output mode:
//
//   - node: custom_node
//     output:
//     message:
//     role: assistant
//     content: "Custom response"
type SimulatedEvent struct {
	// Node targets a specific node using dot-notation for qualified IDs.
	// Use dot-notation to target nodes inside loops or workflows: "loop_id.inner_node"
	// For deeply nested structures: "outer.middle.inner_node"
	// Events without a node field are consumed sequentially in order.
	Node string `json:"node,omitempty" yaml:"node,omitempty"`

	// Output is the raw mock output (mutually exclusive with Type).
	// Use this when you want full control over the output structure.
	// The structure should match the node's expected output schema.
	Output map[string]interface{} `json:"output,omitempty" yaml:"output,omitempty"`

	// Type specifies the event type for automatic conversion.
	// Supported values:
	//   - llm_response: Simulate LLM returning text and/or tool_calls
	//   - tool_result: Simulate tool returning output
	//   - tool_error: Simulate tool error
	//   - llm_error: Simulate LLM error
	//   - user_input: Simulate user message
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Text is the LLM response text (for type: llm_response).
	// Can be combined with ToolCalls for responses that have both.
	Text string `json:"text,omitempty" yaml:"text,omitempty"`

	// ToolCalls are tool invocations from the LLM (for type: llm_response).
	// Each tool call has a name and input parameters.
	ToolCalls []SimToolCall `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`

	// Tool is the tool name (for type: tool_result or tool_error).
	// Must match the tool name from the corresponding tool call.
	Tool string `json:"tool,omitempty" yaml:"tool,omitempty"`

	// ToolOutput is the tool execution result (for type: tool_result).
	// Structure depends on the tool being simulated.
	ToolOutput map[string]interface{} `json:"tool_output,omitempty" yaml:"tool_output,omitempty"`

	// IsError marks a tool_result as failed (for type: tool_result).
	// Mirrors the real ExecuteTools behavior: the tool result carries the error
	// content with is_error set, and response_data[tool] stays null — a failed
	// response tool call does NOT count as a structured response.
	IsError bool `json:"is_error,omitempty" yaml:"is_error,omitempty"`
}

// SimToolCall represents a tool call in a simulated LLM response.
//
// Example:
//
//	tool_calls:
//	  - name: bash
//	    input: {command: "echo hello"}
//	  - name: search
//	    input: {query: "test results"}
type SimToolCall struct {
	// Name is the tool name (e.g., "bash", "search", "edit").
	Name string `json:"name" yaml:"name"`

	// Input contains the tool's input parameters.
	// Structure depends on the tool's schema.
	Input map[string]interface{} `json:"input,omitempty" yaml:"input,omitempty"`
}

// ExpectedOutcome defines the expected final state of a scenario.
type ExpectedOutcome string

const (
	// OutcomeCompleted means the workflow finished successfully.
	OutcomeCompleted ExpectedOutcome = "completed"
	// OutcomeError means the workflow terminated with an error.
	OutcomeError ExpectedOutcome = "error"
	// OutcomeFailed means the workflow failed (distinct from error - e.g., validation failure).
	OutcomeFailed ExpectedOutcome = "failed"
)

// NodeExecutionState represents the execution state of a node.
type NodeExecutionState string

const (
	// StateCompleted means the node executed successfully.
	StateCompleted NodeExecutionState = "completed"
	// StateSkipped means the node was scheduled but skipped due to a false condition.
	StateSkipped NodeExecutionState = "skipped"
	// StateError means the node failed with an error.
	StateError NodeExecutionState = "error"
)

// Expectation defines what to verify after a scenario runs.
//
// All fields are optional - specify only what you want to assert.
// Node references support qualified IDs for inner loop and workflow nodes.
//
// Example - Assert completion and reached nodes:
//
//	expect:
//	  outcome: completed
//	  reached: [call_llm, process_result]
//	  not_reached: [error_handler]
//
// Example - Assert error state:
//
//	expect:
//	  outcome: error
//	  error_contains: "rate limit exceeded"
//	  error_node: call_llm
//
// Example - Assert specific output values:
//
//	expect:
//	  outcome: completed
//	  node_outputs:
//	    classify:
//	      category: "question"
//	      confidence: 0.95
type Expectation struct {
	// Outcome specifies whether the workflow should complete or error.
	// Values: "completed", "error", or "failed"
	Outcome ExpectedOutcome `json:"outcome,omitempty" yaml:"outcome,omitempty"`

	// Reached lists nodes that must be scheduled during the scenario (completed, skipped, or errored).
	// Use qualified IDs for inner loop or workflow nodes: "loop_id.inner_node"
	Reached []string `json:"reached,omitempty" yaml:"reached,omitempty"`

	// NotReached lists nodes that must NOT be scheduled during the scenario.
	// A node is "not reached" if it was never scheduled at all.
	NotReached []string `json:"not_reached,omitempty" yaml:"not_reached,omitempty"`

	// Completed lists nodes that must have executed successfully.
	// Unlike Reached, this excludes skipped nodes.
	Completed []string `json:"completed,omitempty" yaml:"completed,omitempty"`

	// Skipped lists nodes that must have been skipped due to a false condition.
	// These nodes were scheduled but did not execute.
	Skipped []string `json:"skipped,omitempty" yaml:"skipped,omitempty"`

	// ErrorContains specifies a substring that should appear in the error message.
	// Only relevant when Outcome is "error".
	ErrorContains string `json:"error_contains,omitempty" yaml:"error_contains,omitempty"`

	// ErrorNode specifies which node should produce the error.
	// Only relevant when Outcome is "error".
	ErrorNode string `json:"error_node,omitempty" yaml:"error_node,omitempty"`

	// NodeOutputs specifies expected output values for specific nodes.
	// Key is node ID (supports qualified IDs), value is a map of field->expected_value.
	// Partial matching: only specified fields are checked.
	NodeOutputs map[string]map[string]interface{} `json:"node_outputs,omitempty" yaml:"node_outputs,omitempty"`

	// Outputs specifies expected values for the workflow's declared outputs.
	// Keys support dotted paths into structured output values
	// (e.g., "response.choice": "complete"). Partial matching: only the
	// specified paths are checked.
	Outputs map[string]interface{} `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

// Scenario defines a complete test case for a workflow.
//
// A scenario provides simulated events that mock external interactions (LLM calls,
// tool executions) and expectations that verify the workflow behaves correctly.
//
// Example - Basic LLM response test:
//
//	name: simple_response
//	description: Tests basic LLM response handling
//	events:
//	  - node: call_llm
//	    type: llm_response
//	    text: "Hello!"
//	expect:
//	  outcome: completed
//	  reached: [call_llm]
//
// Example - Agent loop with multiple iterations:
//
//	name: agent_two_iterations
//	description: Agent uses a tool then completes
//	events:
//	  - node: agent_loop.call_llm
//	    type: llm_response
//	    tool_calls: [{name: bash, input: {command: ls}}]
//	  - node: agent_loop.execute_tools
//	    type: tool_result
//	    tool: bash
//	    output: {result: "file.txt"}
//	  - node: agent_loop.call_llm
//	    type: llm_response
//	    text: "Done!"
//	expect:
//	  outcome: completed
//	  reached: [agent_loop.call_llm, agent_loop.execute_tools]
//
// Example - Partial testing with state:
//
//	name: test_from_middle
//	description: Test from a specific point with pre-populated state
//	start_at: process_result
//	state:
//	  call_llm:
//	    message: {role: assistant, content: "Previous response"}
//	events:
//	  - node: process_result
//	    output: {formatted: "Processed: Previous response"}
//	expect:
//	  outcome: completed
type Scenario struct {
	// Name uniquely identifies this scenario. Required.
	Name string `json:"name" yaml:"name"`

	// ApiVersion specifies the schema version. Optional, for future compatibility.
	ApiVersion string `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`

	// Description documents what this scenario tests.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Events lists the simulated events in execution order. Required.
	// Events are matched to nodes either by the node field or sequentially.
	Events []SimulatedEvent `json:"events" yaml:"events"`

	// Expect defines the expected outcome and assertions. Optional.
	// If omitted, only verifies the workflow doesn't crash.
	Expect *Expectation `json:"expect,omitempty" yaml:"expect,omitempty"`

	// Inputs overrides workflow inputs for this scenario. Optional.
	// Useful for testing different input configurations.
	Inputs map[string]interface{} `json:"inputs,omitempty" yaml:"inputs,omitempty"`

	// StartAt begins execution at a specific node instead of the entry point. Optional.
	// Use with State to test from a specific point with known context.
	StartAt string `json:"start_at,omitempty" yaml:"start_at,omitempty"`

	// State pre-populates node outputs before execution. Optional.
	// Key is node ID, value is the node's output map.
	// Use with StartAt to test from a specific point with known state.
	State map[string]map[string]interface{} `json:"state,omitempty" yaml:"state,omitempty"`
}

// ScenarioStatus represents the status of a scenario run
type ScenarioStatus string

const (
	StatusPassed ScenarioStatus = "passed"
	StatusFailed ScenarioStatus = "failed"
	StatusError  ScenarioStatus = "error"
)

// ExecutionDetails contains details about the simulation execution
type ExecutionDetails struct {
	NodesReached    []string                          `json:"nodes_reached"`             // All scheduled nodes (completed + skipped + errored)
	NodesCompleted  []string                          `json:"nodes_completed,omitempty"` // Nodes that executed successfully
	NodesSkipped    []string                          `json:"nodes_skipped,omitempty"`   // Nodes skipped due to false conditions
	NodeStates      map[string]NodeExecutionState     `json:"node_states,omitempty"`     // Explicit state for each node
	Outcome         string                            `json:"outcome"`                   // "completed", "error", or "failed"
	Error           *ErrorDetails                     `json:"error,omitempty"`
	DurationMs      int64                             `json:"duration_ms"`
	NodeOutputs     map[string]map[string]interface{} `json:"node_outputs,omitempty"`     // Actual outputs from each node
	WorkflowOutputs map[string]interface{}            `json:"workflow_outputs,omitempty"` // Evaluated workflow-level outputs (nil if none declared)
}

// ErrorDetails contains information about an error that occurred
type ErrorDetails struct {
	Node       string `json:"node"`
	Step       string `json:"step,omitempty"`
	Message    string `json:"message"`
	Expression string `json:"expression,omitempty"`
}

// ScenarioResult contains the result of running a scenario
type ScenarioResult struct {
	Status     ScenarioStatus   `json:"status"`
	Scenario   string           `json:"scenario"`
	Execution  ExecutionDetails `json:"execution"`
	Expected   *Expectation     `json:"expected,omitempty"`
	Mismatches []string         `json:"mismatches,omitempty"`
	RunAt      time.Time        `json:"run_at"`
}

// ToJSON converts a scenario to JSON
func (s *Scenario) ToJSON() (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ScenarioFromJSON parses a scenario from JSON
func ScenarioFromJSON(data string) (*Scenario, error) {
	var s Scenario
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ExpectationToJSON converts an expectation to JSON
func (e *Expectation) ToJSON() (string, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ExpectationFromJSON parses an expectation from JSON
func ExpectationFromJSON(data string) (*Expectation, error) {
	var e Expectation
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// ResultToJSON converts a result to JSON
func (r *ScenarioResult) ToJSON() (string, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
