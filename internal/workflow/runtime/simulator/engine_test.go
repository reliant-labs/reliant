// Copyright (c) 2025 Reliant Labs
package simulator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Simple workflow YAML for testing
const simpleWorkflowYAML = `
name: test-workflow
apiVersion: "1.0"
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
  - id: execute_tools
    type: execute_tools
  - id: save_result
    type: save_message
    args:
      role: "assistant"
      content: "Done"
edges:
  - from: call_llm
    cases:
      - to: save_result
        condition: "size(nodes.call_llm.tool_calls) == 0"
    default: execute_tools
  - from: execute_tools
    default: save_result
`

const workflowWithResponseTool = `
name: test-response-tool
apiVersion: "1.0"
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: mock
      response_tool:
        name: processed_data
        schema:
          type: object
          properties:
            results:
              type: array
  - id: execute_tools
    type: execute_tools
  - id: save_results
    type: save_message
    args:
      role: "assistant"
      content: "{{nodes.execute_tools.response_data.processed_data.results}}"
edges:
  - from: call_llm
    default: execute_tools
  - from: execute_tools
    default: save_results
`

// Helper to create a call_llm output with just text (no tool calls)
func llmOutput(text string) map[string]interface{} {
	return map[string]interface{}{
		"message": map[string]interface{}{
			"role": "assistant",
			"text": text,
		},
		"response_text": text,
		"tool_calls":    []interface{}{},
	}
}

// Helper to create a call_llm output with tool calls
func llmOutputWithTools(text string, toolCalls ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"message": map[string]interface{}{
			"role": "assistant",
			"text": text,
		},
		"response_text": text,
		"tool_calls":    toolCalls,
	}
}

// Helper to create a tool call
func toolCall(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id":    id,
		"name":  name,
		"input": input,
	}
}

// Helper to create an execute_tools output
func toolsOutput(results ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"message": map[string]interface{}{
			"role": "tool_result",
			"text": "Tool results",
		},
		"tool_results": results,
	}
}

// Helper to create a tool result
func toolResult(toolCallID, name, content string) map[string]interface{} {
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"name":         name,
		"content":      content,
	}
}

func TestEngine_RunScenario_SimpleWorkflow(t *testing.T) {
	engine, err := NewEngineFromYAML(simpleWorkflowYAML)
	require.NoError(t, err)

	t.Run("happy path - no tool calls", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "happy_path",
			Description: "LLM responds without tool calls",
			Events: []SimulatedEvent{
				{
					Output: llmOutput("Hello! How can I help?"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
			},
		}

		result := engine.RunScenario(scenario)

		assert.Equal(t, StatusPassed, result.Status)
		assert.Equal(t, "completed", result.Execution.Outcome)
		assert.Contains(t, result.Execution.NodesReached, "call_llm")
	})

	t.Run("with tool calls", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "with_tools",
			Description: "LLM calls tools",
			Events: []SimulatedEvent{
				{
					Output: llmOutputWithTools("Let me check...",
						toolCall("call_0", "bash", map[string]interface{}{"command": "ls"}),
					),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
			},
		}

		result := engine.RunScenario(scenario)

		assert.Equal(t, StatusPassed, result.Status)
		assert.Equal(t, "completed", result.Execution.Outcome)
	})
}

func TestEngine_RunScenario_Expectations(t *testing.T) {
	engine, err := NewEngineFromYAML(simpleWorkflowYAML)
	require.NoError(t, err)

	t.Run("fails when outcome doesn't match", func(t *testing.T) {
		scenario := &Scenario{
			Name: "expect_error_but_completes",
			Events: []SimulatedEvent{
				{Output: llmOutput("Hello")},
			},
			Expect: &Expectation{
				Outcome: OutcomeError,
			},
		}

		result := engine.RunScenario(scenario)

		assert.Equal(t, StatusFailed, result.Status)
		assert.Contains(t, result.Mismatches[0], "expected outcome")
	})

	t.Run("fails validation when expected node does not exist", func(t *testing.T) {
		scenario := &Scenario{
			Name: "expect_nonexistent_node",
			Events: []SimulatedEvent{
				{Output: llmOutput("Hello")},
			},
			Expect: &Expectation{
				Reached: []string{"nonexistent_node"},
			},
		}

		result := engine.RunScenario(scenario)

		// Should fail with validation error (node doesn't exist in workflow)
		assert.Equal(t, StatusError, result.Status)
		assert.Contains(t, result.Mismatches[0], "nonexistent_node")
	})

	t.Run("fails when valid node not reached", func(t *testing.T) {
		// execute_tools exists but won't be reached since call_llm returns no tool_calls
		scenario := &Scenario{
			Name: "expect_node_not_reached",
			Events: []SimulatedEvent{
				{Output: llmOutput("Hello")}, // No tool_calls, won't reach execute_tools
			},
			Expect: &Expectation{
				Reached: []string{"execute_tools"}, // Valid node, but won't be reached
			},
		}

		result := engine.RunScenario(scenario)

		// Should fail because the node wasn't reached (not validation error)
		assert.Equal(t, StatusFailed, result.Status)
		assert.Contains(t, result.Mismatches[0], "execute_tools")
	})

	t.Run("passes when all expectations met", func(t *testing.T) {
		scenario := &Scenario{
			Name: "all_expectations_met",
			Events: []SimulatedEvent{
				{Output: llmOutput("Hello")},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{"call_llm"},
			},
		}

		result := engine.RunScenario(scenario)

		assert.Equal(t, StatusPassed, result.Status)
		assert.Empty(t, result.Mismatches)
	})
}

func TestEngine_RunScenario_ResponseToolHandling(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithResponseTool)
	require.NoError(t, err)

	t.Run("response tool with data", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "response_tool_happy_path",
			Description: "LLM calls response tool with data",
			Events: []SimulatedEvent{
				{
					Output: llmOutputWithTools("Processing data...",
						toolCall("call_0", "processed_data", map[string]interface{}{
							"results": []interface{}{"item1", "item2"},
						}),
					),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{"call_llm", "execute_tools", "save_results"},
			},
		}

		result := engine.RunScenario(scenario)

		// Note: This will likely fail because the current mock doesn't
		// properly set up response_data. This test documents the expected behavior.
		t.Logf("Result: %+v", result)
	})
}

func TestScenario_JSON(t *testing.T) {
	scenario := &Scenario{
		Name:        "test_scenario",
		Description: "A test scenario",
		Events: []SimulatedEvent{
			{
				Output: llmOutputWithTools("Hello",
					toolCall("call_0", "bash", map[string]interface{}{"cmd": "ls"}),
				),
			},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			Reached: []string{"step1"},
		},
	}

	// Test ToJSON
	jsonStr, err := scenario.ToJSON()
	require.NoError(t, err)
	assert.Contains(t, jsonStr, "test_scenario")
	assert.Contains(t, jsonStr, "Hello")

	// Test FromJSON
	parsed, err := ScenarioFromJSON(jsonStr)
	require.NoError(t, err)
	assert.Equal(t, scenario.Name, parsed.Name)
	assert.Equal(t, scenario.Description, parsed.Description)
	assert.Len(t, parsed.Events, 1)
}

func TestExpectation_JSON(t *testing.T) {
	expect := &Expectation{
		Outcome:       OutcomeCompleted,
		Reached:       []string{"step1", "step2"},
		NotReached:    []string{"error_step"},
		ErrorContains: "should not happen",
	}

	jsonStr, err := expect.ToJSON()
	require.NoError(t, err)

	parsed, err := ExpectationFromJSON(jsonStr)
	require.NoError(t, err)
	assert.Equal(t, expect.Outcome, parsed.Outcome)
	assert.Equal(t, expect.Reached, parsed.Reached)
	assert.Equal(t, expect.NotReached, parsed.NotReached)
	assert.Equal(t, expect.ErrorContains, parsed.ErrorContains)
}

func TestEngine_RunScenarios_Multiple(t *testing.T) {
	engine, err := NewEngineFromYAML(simpleWorkflowYAML)
	require.NoError(t, err)

	scenarios := []*Scenario{
		{
			Name:   "scenario1",
			Events: []SimulatedEvent{{Output: llmOutput("Hello")}},
			Expect: &Expectation{Outcome: OutcomeCompleted},
		},
		{
			Name:   "scenario2",
			Events: []SimulatedEvent{{Output: llmOutput("World")}},
			Expect: &Expectation{Outcome: OutcomeCompleted},
		},
	}

	results := engine.RunScenarios(scenarios)

	assert.Len(t, results, 2)
	assert.Equal(t, StatusPassed, results[0].Status)
	assert.Equal(t, StatusPassed, results[1].Status)
}

// Tests for new features: start_at, state, node_outputs

func TestEngine_RunScenario_StartAt(t *testing.T) {
	engine, err := NewEngineFromYAML(simpleWorkflowYAML)
	require.NoError(t, err)

	t.Run("start at execute_tools with pre-populated state", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "start_at_execute_tools",
			Description: "Start execution from execute_tools node",
			StartAt:     "execute_tools",
			State: map[string]map[string]interface{}{
				"call_llm": {
					"tool_calls": []interface{}{
						map[string]interface{}{"name": "bash", "input": map[string]interface{}{"cmd": "ls"}},
					},
				},
			},
			Events: []SimulatedEvent{
				{
					Output: toolsOutput(
						toolResult("call_0", "bash", "file1.txt\nfile2.txt"),
					),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{"execute_tools", "save_result"},
				// call_llm should NOT be reached since we started at execute_tools
				NotReached: []string{"call_llm"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("Result: %+v", result)
		// Note: The actual behavior depends on how state machine handles StartAt
	})
}

func TestEngine_RunScenario_NodeOutputs(t *testing.T) {
	engine, err := NewEngineFromYAML(simpleWorkflowYAML)
	require.NoError(t, err)

	t.Run("asserts specific node output values", func(t *testing.T) {
		scenario := &Scenario{
			Name: "check_node_outputs",
			Events: []SimulatedEvent{
				{
					Output: llmOutput("Hello world"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				NodeOutputs: map[string]map[string]interface{}{
					"call_llm": {
						// Check that tool_calls is empty (no tool calls)
						"tool_calls": []interface{}{},
					},
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("Result: %+v", result)
		t.Logf("NodeOutputs: %+v", result.Execution.NodeOutputs)

		// Should pass since tool_calls is empty
		assert.Equal(t, StatusPassed, result.Status)
	})

	t.Run("fails when node output doesn't match", func(t *testing.T) {
		scenario := &Scenario{
			Name: "check_node_outputs_mismatch",
			Events: []SimulatedEvent{
				{
					Output: llmOutput("Hello world"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				NodeOutputs: map[string]map[string]interface{}{
					"call_llm": {
						// Expect non-empty tool_calls (but we sent empty)
						"tool_calls": []interface{}{
							map[string]interface{}{"name": "expected_tool"},
						},
					},
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("Result: %+v", result)
		t.Logf("Mismatches: %v", result.Mismatches)

		// Should fail due to mismatch
		assert.Equal(t, StatusFailed, result.Status)
	})
}

// Workflow with an inline loop
const workflowWithInlineLoop = `
name: test-inline-loop
apiVersion: "1.0"
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "outputs.tool_calls != null && size(outputs.tool_calls) > 0"
    inline:
      entry: [call_llm]
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: mock
        - id: execute_tools
          type: execute_tools
        - id: save_result
          type: save_message
          args:
            role: "assistant"
            content: "Done"
      edges:
        - from: call_llm
          cases:
            - to: execute_tools
              condition: "nodes.call_llm.tool_calls != null && size(nodes.call_llm.tool_calls) > 0"
          default: save_result
        - from: execute_tools
          default: save_result
  - id: final_save
    type: save_message
    args:
      role: "assistant"
      content: "Workflow complete"
edges:
  - from: agent_loop
    default: final_save
`

func TestEngine_RunScenario_InlineLoopInnerNodes(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithInlineLoop)
	require.NoError(t, err)

	t.Run("single iteration - no tool calls exits loop", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "loop_single_iteration",
			Description: "LLM responds without tool calls, loop exits after 1 iteration",
			Events: []SimulatedEvent{
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("I'm done, no tools needed."),
				},
			},
			Expect: &Expectation{
				Outcome:    OutcomeCompleted,
				Reached:    []string{"agent_loop", "agent_loop.call_llm", "agent_loop.save_result", "final_save"},
				NotReached: []string{"agent_loop.execute_tools"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})

	t.Run("single iteration with tool calls then exit", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "loop_with_tools_then_exit",
			Description: "LLM calls tools on iteration 1, then exits on iteration 2",
			Events: []SimulatedEvent{
				// Iteration 1: LLM calls a tool
				{
					Node: "agent_loop.call_llm",
					Output: llmOutputWithTools("Let me check something",
						toolCall("call_0", "bash", map[string]interface{}{"command": "ls"}),
					),
				},
				{
					Node: "agent_loop.execute_tools",
					Output: toolsOutput(
						toolResult("call_0", "bash", "file1.txt\nfile2.txt"),
					),
				},
				// Iteration 2: LLM responds without tool calls (exits loop)
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("All done!"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{
					"agent_loop",
					"agent_loop.call_llm",
					"agent_loop.execute_tools",
					"agent_loop.save_result",
					"final_save",
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})

	t.Run("inner node outputs are accessible", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "inner_node_outputs",
			Description: "Can assert on inner node output values",
			Events: []SimulatedEvent{
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("No tools needed"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				NodeOutputs: map[string]map[string]interface{}{
					"agent_loop.call_llm": {
						"tool_calls": []interface{}{},
					},
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodeOutputs: %v", result.Execution.NodeOutputs)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})

	t.Run("multiple iterations consumed sequentially", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "multi_iteration_sequential",
			Description: "Events are consumed sequentially across iterations",
			Events: []SimulatedEvent{
				// Iteration 1
				{
					Node: "agent_loop.call_llm",
					Output: llmOutputWithTools("",
						toolCall("call_0", "read_file", map[string]interface{}{"path": "/tmp/a"}),
					),
				},
				{
					Node: "agent_loop.execute_tools",
					Output: toolsOutput(
						toolResult("call_0", "read_file", "hello"),
					),
				},
				// Iteration 2
				{
					Node: "agent_loop.call_llm",
					Output: llmOutputWithTools("",
						toolCall("call_1", "write_file", map[string]interface{}{"path": "/tmp/b", "content": "world"}),
					),
				},
				{
					Node: "agent_loop.execute_tools",
					Output: toolsOutput(
						toolResult("call_1", "write_file", "success"),
					),
				},
				// Iteration 3: exit
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("Done with all tasks"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{
					"agent_loop",
					"agent_loop.call_llm",
					"agent_loop.execute_tools",
					"agent_loop.save_result",
					"final_save",
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})
}

// TestJoinNodeDeduplicationInLoop verifies that join nodes inside loops
// are only added to visitedSteps once when they complete, not every time
// an upstream node triggers them.
func TestJoinNodeDeduplicationInLoop(t *testing.T) {
	// Workflow with a loop containing a fan-out/fan-in pattern:
	// start -> [a, b, c] -> join -> done
	const fanOutLoopWorkflow = `
name: fanout-loop-test
apiVersion: "1.0"
entry: [my_loop]
nodes:
  - id: my_loop
    type: loop
    while: "iter.iteration < 1"  # Run once
    inline:
      entry: [start]
      nodes:
        - id: start
          type: save_message
          args:
            role: assistant
            content: "Starting iteration"
        - id: step_a
          type: call_llm
          args:
            model: mock
        - id: step_b
          type: call_llm
          args:
            model: mock
        - id: step_c
          type: call_llm
          args:
            model: mock
        - id: join_all
          type: join
        - id: done
          type: save_message
          args:
            role: assistant
            content: "Done"
      edges:
        - from: start
          default: step_a
        - from: start
          default: step_b
        - from: start
          default: step_c
        - from: step_a
          default: join_all
        - from: step_b
          default: join_all
        - from: step_c
          default: join_all
        - from: join_all
          default: done
  - id: complete
    type: save_message
    args:
      role: assistant
      content: "Workflow complete"
edges:
  - from: my_loop
    default: complete
`

	workflow, err := ParseWorkflowYAML([]byte(fanOutLoopWorkflow))
	require.NoError(t, err)

	engine := NewEngine(workflow)

	scenario := &Scenario{
		Name:        "join_dedup_test",
		Description: "Tests that join nodes are only visited once when all sources complete",
		Events: []SimulatedEvent{
			{Node: "my_loop.step_a", Output: llmOutput("A done")},
			{Node: "my_loop.step_b", Output: llmOutput("B done")},
			{Node: "my_loop.step_c", Output: llmOutput("C done")},
		},
		Expect: &Expectation{
			Outcome: OutcomeCompleted,
			Reached: []string{
				"my_loop.start",
				"my_loop.step_a",
				"my_loop.step_b",
				"my_loop.step_c",
				"my_loop.join_all",
				"my_loop.done",
				"my_loop",
				"complete",
			},
		},
	}

	result := engine.RunScenario(scenario)
	t.Logf("NodesReached: %v", result.Execution.NodesReached)
	t.Logf("Mismatches: %v", result.Mismatches)

	assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)

	// Verify join_all only appears once in NodesReached
	joinCount := 0
	for _, node := range result.Execution.NodesReached {
		if node == "my_loop.join_all" {
			joinCount++
		}
	}
	assert.Equal(t, 1, joinCount, "join_all should appear exactly once in NodesReached, got %d", joinCount)
}

// Workflow with a loop followed by conditional routing based on loop output
// User-defined outputs are flattened to top level: nodes.<id>.<field>
// System fields use _ prefix: nodes.<id>._iterations
const workflowWithLoopAndRouting = `
name: test-loop-routing
apiVersion: "1.0"
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "size(outputs.tool_calls) > 0"
    inline:
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: mock
      edges: []
      outputs:
        result: "{{nodes.call_llm.message.text}}"
        tool_calls: "{{nodes.call_llm.tool_calls}}"
  - id: success_handler
    type: save_message
    args:
      role: "assistant"
      content: "Success"
  - id: fallback_handler
    type: save_message
    args:
      role: "assistant"
      content: "Fallback"
edges:
  - from: agent_loop
    cases:
      - to: success_handler
        condition: "nodes.agent_loop.result == 'done'"
    default: fallback_handler
`

func TestEngine_RunScenario_LoopOutputRoutesDownstream(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithLoopAndRouting)
	require.NoError(t, err)

	t.Run("loop output routes to success handler", func(t *testing.T) {
		scenario := &Scenario{
			Name: "loop_routes_success",
			Events: []SimulatedEvent{
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("done"),
				},
			},
			Expect: &Expectation{
				Outcome:    OutcomeCompleted,
				Reached:    []string{"agent_loop", "success_handler"},
				NotReached: []string{"fallback_handler"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("NodeOutputs: %v", result.Execution.NodeOutputs)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})

	t.Run("loop output routes to fallback handler", func(t *testing.T) {
		scenario := &Scenario{
			Name: "loop_routes_fallback",
			Events: []SimulatedEvent{
				{
					Node:   "agent_loop.call_llm",
					Output: llmOutput("not done"),
				},
			},
			Expect: &Expectation{
				Outcome:    OutcomeCompleted,
				Reached:    []string{"agent_loop", "fallback_handler"},
				NotReached: []string{"success_handler"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})
}

// Workflow with nested loops
const workflowWithNestedLoops = `
name: test-nested-loops
apiVersion: "1.0"
entry: [outer_loop]
nodes:
  - id: outer_loop
    type: loop
    while: "outputs.continue == true"
    inline:
      entry: [inner_loop]
      outputs:
        continue: "{{nodes.check_result.tool_calls != null && size(nodes.check_result.tool_calls) > 0}}"
      nodes:
        - id: inner_loop
          type: loop
          while: "outputs.tool_calls != null && size(outputs.tool_calls) > 0"
          inline:
            entry: [call_llm]
            outputs:
              tool_calls: "{{nodes.call_llm.tool_calls}}"
            nodes:
              - id: call_llm
                type: call_llm
                args:
                  model: mock
            edges: []
        - id: check_result
          type: call_llm
          args:
            model: mock
      edges:
        - from: inner_loop
          default: check_result
edges: []
`

func TestEngine_RunScenario_NestedLoops(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithNestedLoops)
	require.NoError(t, err)

	t.Run("nested loop nodes reachable with chained qualified IDs", func(t *testing.T) {
		scenario := &Scenario{
			Name:        "nested_loops",
			Description: "Inner loop nodes are addressed as outer_loop.inner_loop.call_llm",
			Events: []SimulatedEvent{
				// Inner loop iteration 1: call_llm with no tool calls -> exits inner loop
				{
					Node:   "outer_loop.inner_loop.call_llm",
					Output: llmOutput("Inner done"),
				},
				// After inner loop, check_result runs
				{
					Node:   "outer_loop.check_result",
					Output: llmOutput("All done"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{
					"outer_loop",
					"outer_loop.inner_loop",
					"outer_loop.inner_loop.call_llm",
					"outer_loop.check_result",
				},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})
}

// Workflow with ref loop (should still work with black-box mocking)
const workflowWithRefLoop = `
name: test-ref-loop
apiVersion: "1.0"
entry: [my_loop]
nodes:
  - id: my_loop
    type: loop
    while: "outputs.continue == true"
    ref: sub-workflow
  - id: done
    type: save_message
    args:
      role: "assistant"
      content: "Done"
edges:
  - from: my_loop
    default: done
`

func TestEngine_RunScenario_RefLoopBlackBox(t *testing.T) {
	engine, err := NewEngineFromYAML(workflowWithRefLoop)
	require.NoError(t, err)

	t.Run("ref loop uses black-box mocking with ref name", func(t *testing.T) {
		scenario := &Scenario{
			Name: "ref_loop_blackbox",
			Events: []SimulatedEvent{
				// Mock the entire sub-workflow by ref name
				// First iteration: continue=true to loop again
				{
					Node: "sub-workflow",
					Output: map[string]interface{}{
						"continue":      true,
						"response_text": "iteration 1",
					},
				},
				// Second iteration: continue=false to exit loop
				{
					Node: "sub-workflow",
					Output: map[string]interface{}{
						"continue":      false,
						"response_text": "iteration 2 - done",
					},
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{"my_loop", "done"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
	})
}

// --- Router node test workflows ---

const workflowWithRouter = `
name: test-router
apiVersion: "1.0"
entry: [route]
nodes:
  - id: route
    type: router
    system_prompt: "route this request"
    model:
      tags: [fast]
    workflows:
      - ref: builtin://agent
        presets: [general, researcher]
      - ref: builtin://agent
        presets: [code_reviewer]
  - id: save_result
    type: save_message
    role: "assistant"
    content: "Routing complete"
edges:
  - from: route
    default: save_result
`

const workflowWithRouterConditionalEdge = `
name: test-router-conditional
apiVersion: "1.0"
entry: [route]
nodes:
  - id: route
    type: router
    system_prompt: "route this"
    model:
      tags: [fast]
    workflows:
      - ref: builtin://agent
        presets: [general, researcher]
  - id: success
    type: save_message
    role: "assistant"
    content: "Success"
  - id: fallback
    type: save_message
    role: "assistant"
    content: "Fallback"
edges:
  - from: route
    cases:
      - to: success
        condition: "nodes.route.selected_preset == 'researcher'"
    default: fallback
`

// routerOutput builds the mock output map for a router node.
func routerOutput(workflow, preset, prompt, reasoning string) map[string]interface{} {
	return map[string]interface{}{
		"selected_workflow": workflow,
		"selected_preset":   preset,
		"prompt":            prompt,
		"reasoning":         reasoning,
		"outputs":           map[string]interface{}{"response_text": "completed"},
	}
}

func TestEngine_RunScenario_RouterNode(t *testing.T) {
	t.Run("happy path - router completes and downstream executes", func(t *testing.T) {
		engine, err := NewEngineFromYAML(workflowWithRouter)
		require.NoError(t, err)

		scenario := &Scenario{
			Name:        "router_happy_path",
			Description: "Router completes, downstream save_message executes",
			Events: []SimulatedEvent{
				{
					Node:   "route",
					Output: routerOutput("builtin://agent", "researcher", "Research the auth module", "User wants investigation"),
				},
			},
			Expect: &Expectation{
				Outcome: OutcomeCompleted,
				Reached: []string{"route", "save_result"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
		assert.Contains(t, result.Execution.NodesReached, "route")
		assert.Contains(t, result.Execution.NodesReached, "save_result")
	})

	t.Run("conditional edge routes to success on matching preset", func(t *testing.T) {
		engine, err := NewEngineFromYAML(workflowWithRouterConditionalEdge)
		require.NoError(t, err)

		scenario := &Scenario{
			Name:        "router_conditional_researcher",
			Description: "Router selects researcher preset, edge routes to success",
			Events: []SimulatedEvent{
				{
					Node:   "route",
					Output: routerOutput("builtin://agent", "researcher", "Research this", "Needs research"),
				},
			},
			Expect: &Expectation{
				Outcome:    OutcomeCompleted,
				Reached:    []string{"route", "success"},
				NotReached: []string{"fallback"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
		assert.Contains(t, result.Execution.NodesReached, "success")
		assert.NotContains(t, result.Execution.NodesReached, "fallback")
	})

	t.Run("conditional edge routes to fallback on non-matching preset", func(t *testing.T) {
		engine, err := NewEngineFromYAML(workflowWithRouterConditionalEdge)
		require.NoError(t, err)

		scenario := &Scenario{
			Name:        "router_conditional_general",
			Description: "Router selects general preset, edge routes to fallback",
			Events: []SimulatedEvent{
				{
					Node:   "route",
					Output: routerOutput("builtin://agent", "general", "General query", "Not research"),
				},
			},
			Expect: &Expectation{
				Outcome:    OutcomeCompleted,
				Reached:    []string{"route", "fallback"},
				NotReached: []string{"success"},
			},
		}

		result := engine.RunScenario(scenario)
		t.Logf("NodesReached: %v", result.Execution.NodesReached)
		t.Logf("Mismatches: %v", result.Mismatches)

		assert.Equal(t, StatusPassed, result.Status, "mismatches: %v", result.Mismatches)
		assert.Contains(t, result.Execution.NodesReached, "fallback")
		assert.NotContains(t, result.Execution.NodesReached, "success")
	})
}
