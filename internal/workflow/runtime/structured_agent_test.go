// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// loadStructuredAgentWorkflow loads the structured-agent workflow from the builtin directory
func loadStructuredAgentWorkflow(t *testing.T) *reliantv1.Workflow {
	data, err := os.ReadFile("../builtin/structured-agent.yaml")
	require.NoError(t, err, "should read structured-agent.yaml")

	wf, err := ParseWorkflowProtoBytesNoValidation(data)
	require.NoError(t, err, "should parse structured-agent workflow")

	return wf
}

// TestStructuredAgentWorkflow tests the structured-agent builtin workflow
func TestStructuredAgentWorkflow(t *testing.T) {
	t.Run("validates successfully", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)
		require.NotNil(t, wf)

		assert.Equal(t, "structured-agent", wf.GetName())
		assert.Contains(t, wf.GetEntry(), "agent_loop")
	})

	t.Run("has expected inputs", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		inputs := wf.GetInputs()
		assert.Contains(t, inputs, "response_tool_name")
		assert.Contains(t, inputs, "response_tool_description")
		assert.Contains(t, inputs, "model")
		assert.Contains(t, inputs, "tools")
	})

	t.Run("has response tool detection structure", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		// Find the agent_loop node
		var agentLoop *reliantv1.Node
		for _, node := range wf.GetNodes() {
			if node.GetId() == "agent_loop" {
				agentLoop = node
				break
			}
		}
		require.NotNil(t, agentLoop, "should have agent_loop node")
		assert.Equal(t, model.NodeTypeLoop, agentLoop.GetType())

		// Check the inline workflow
		loopArgs := agentLoop.GetLoop()
		require.NotNil(t, loopArgs, "agent_loop should have loop args")
		inlineWf := loopArgs.GetInline()
		require.NotNil(t, inlineWf, "agent_loop should have inline workflow")

		// Find call_llm and execute_tools nodes in the inline workflow
		var callLLM, executeTools *reliantv1.Node
		for _, node := range inlineWf.GetNodes() {
			if node.GetId() == "call_llm" {
				callLLM = node
			}
			if node.GetId() == "execute_tools" {
				executeTools = node
			}
		}

		require.NotNil(t, callLLM, "should have call_llm node in inline workflow")
		require.NotNil(t, executeTools, "should have execute_tools node in inline workflow")

		// Check call_llm has response_tool configured (via proto accessor)
		callLLMArgs := callLLM.GetCallLlm()
		require.NotNil(t, callLLMArgs)
		assert.NotNil(t, callLLMArgs.GetResponseTool(), "call_llm should have response_tool configured")

		// Check execute_tools references tool_calls
		executeToolsArgs := executeTools.GetExecuteTools()
		require.NotNil(t, executeToolsArgs)
		assert.NotNil(t, executeToolsArgs.GetToolCalls(), "execute_tools should have tool_calls configured")
	})

	t.Run("outputs reference response_data correctly", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		// Check top-level outputs
		outputs := wf.GetOutputs()
		assert.Contains(t, outputs, "response")
		assert.Contains(t, outputs, "completed")

		responseExpr := outputs["response"]
		assert.Equal(t, "{{nodes.agent_loop.response}}", responseExpr)

		// Find the agent_loop node
		var agentLoop *reliantv1.Node
		for _, node := range wf.GetNodes() {
			if node.GetId() == "agent_loop" {
				agentLoop = node
				break
			}
		}
		require.NotNil(t, agentLoop)

		// Check loop inline workflow outputs
		inlineWf := agentLoop.GetLoop().GetInline()
		require.NotNil(t, inlineWf)
		inlineOutputs := inlineWf.GetOutputs()
		assert.Contains(t, inlineOutputs, "response")

		responseOutputExpr := inlineOutputs["response"]
		t.Logf("Loop response output expression: %s", responseOutputExpr)

		assert.Contains(t, responseOutputExpr, "has(nodes.execute_tools)")
		assert.Contains(t, responseOutputExpr, "response_data")
	})

	t.Run("response tool uses dynamic name from inputs", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		// Find agent_loop inline workflow
		var agentLoop *reliantv1.Node
		for _, node := range wf.GetNodes() {
			if node.GetId() == "agent_loop" {
				agentLoop = node
				break
			}
		}
		require.NotNil(t, agentLoop)
		inlineWf := agentLoop.GetLoop().GetInline()
		require.NotNil(t, inlineWf)

		// Find call_llm node
		var callLLM *reliantv1.Node
		for _, node := range inlineWf.GetNodes() {
			if node.GetId() == "call_llm" {
				callLLM = node
				break
			}
		}
		require.NotNil(t, callLLM)

		// Check response_tool uses inputs.response_tool_name (via proto accessor)
		responseTool := callLLM.GetCallLlm().GetResponseTool()
		require.NotNil(t, responseTool)

		toolNameCS := responseTool.GetName()
		require.NotNil(t, toolNameCS)
		t.Logf("Response tool name config: %v", toolNameCS)
		// The name is a CelString with an expr (template), verify it's the expected expression
		assert.Equal(t, "{{inputs.response_tool_name}}", model.CelStringRaw(toolNameCS))

		// The default value for response_tool_name should be "submit_response"
		rtInput := wf.GetInputs()["response_tool_name"]
		require.NotNil(t, rtInput)
		defaultVal := rtInput.GetStringInput().GetDefault()
		assert.Equal(t, "submit_response", defaultVal)
	})

	t.Run("execute_tools node exists in inline workflow", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		var agentLoop *reliantv1.Node
		for _, node := range wf.GetNodes() {
			if node.GetId() == "agent_loop" {
				agentLoop = node
				break
			}
		}
		require.NotNil(t, agentLoop)
		inlineWf := agentLoop.GetLoop().GetInline()
		require.NotNil(t, inlineWf)

		var executeTools *reliantv1.Node
		for _, node := range inlineWf.GetNodes() {
			if node.GetId() == "execute_tools" {
				executeTools = node
				break
			}
		}
		require.NotNil(t, executeTools)
		assert.Equal(t, model.NodeTypeExecuteTools, executeTools.GetType())
	})

	t.Run("outputs handle null response_data values safely", func(t *testing.T) {
		wf := loadStructuredAgentWorkflow(t)

		var agentLoop *reliantv1.Node
		for _, node := range wf.GetNodes() {
			if node.GetId() == "agent_loop" {
				agentLoop = node
				break
			}
		}
		require.NotNil(t, agentLoop)
		inlineWf := agentLoop.GetLoop().GetInline()
		require.NotNil(t, inlineWf)

		completedExpr := inlineWf.GetOutputs()["completed"]
		assert.Contains(t, completedExpr, "!= null", "completed expression should check for null")
		t.Logf("completed expression checks for null: %s", completedExpr)
	})
}

// TestStructuredAgentOutputCEL tests that the structured-agent inline workflow outputs
// evaluate safely in all scenarios, especially when the LLM calls regular tools
// (not the response tool) and response_data doesn't contain the response tool key.
//
// This is the regression test for: "CEL evaluation error: no such key: submit_review"
func TestStructuredAgentOutputCEL(t *testing.T) {
	wf := loadStructuredAgentWorkflow(t)

	// Extract the inline workflow outputs from agent_loop
	var agentLoop *reliantv1.Node
	for _, node := range wf.GetNodes() {
		if node.GetId() == "agent_loop" {
			agentLoop = node
			break
		}
	}
	require.NotNil(t, agentLoop)
	inlineWf := agentLoop.GetLoop().GetInline()
	require.NotNil(t, inlineWf)

	responseExpr := inlineWf.GetOutputs()["response"]
	completedExpr := inlineWf.GetOutputs()["completed"]
	require.NotEmpty(t, responseExpr, "should have response output expression")
	require.NotEmpty(t, completedExpr, "should have completed output expression")

	tests := []struct {
		name              string
		responseToolName  string
		nodeOutputs       map[string]interface{}
		expectedResponse  interface{}
		expectedCompleted interface{}
	}{
		{
			name:             "regular_tool_called_no_response_key",
			responseToolName: "submit_review",
			nodeOutputs: map[string]interface{}{
				// LLM called bash, not submit_review. response_data has no submit_review key.
				"execute_tools": map[string]interface{}{
					"response_data": map[string]interface{}{},
				},
			},
			expectedResponse:  nil,
			expectedCompleted: false,
		},
		{
			name:             "response_tool_called_successfully",
			responseToolName: "submit_review",
			nodeOutputs: map[string]interface{}{
				"execute_tools": map[string]interface{}{
					"response_data": map[string]interface{}{
						"submit_review": map[string]interface{}{
							"grade":   "pass",
							"summary": "All good",
						},
					},
				},
			},
			expectedResponse: map[string]interface{}{
				"grade":   "pass",
				"summary": "All good",
			},
			expectedCompleted: true,
		},
		{
			name:             "response_key_exists_but_null",
			responseToolName: "submit_response",
			nodeOutputs: map[string]interface{}{
				// expected_response_tools pre-created the key as null
				"execute_tools": map[string]interface{}{
					"response_data": map[string]interface{}{
						"submit_response": nil,
					},
				},
			},
			expectedResponse:  nil,
			expectedCompleted: false,
		},
		{
			name:             "execute_tools_not_run",
			responseToolName: "submit_response",
			// LLM responded without tool calls, so execute_tools never ran
			nodeOutputs:       map[string]interface{}{},
			expectedResponse:  nil,
			expectedCompleted: false,
		},
		{
			name:             "response_data_missing_entirely",
			responseToolName: "submit_review",
			nodeOutputs: map[string]interface{}{
				// execute_tools ran but response_data field is absent
				"execute_tools": map[string]interface{}{},
			},
			expectedResponse:  nil,
			expectedCompleted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputs := map[string]interface{}{
				"response_tool_name": tt.responseToolName,
			}

			workflowContext := buildWorkflowContext("test-wf-id", "structured-agent", "test-chat", inputs)
			outputs, err := EvaluateWorkflowOutputs(
				inlineWf.GetOutputs(),
				tt.nodeOutputs,
				workflowContext,
			)
			require.NoError(t, err, "CEL evaluation should not fail")

			if tt.expectedResponse == nil {
				// CEL returns structpb.NullValue(0) for null, not Go nil
				assertCELNull(t, outputs["response"], "response output should be null")
			} else {
				assert.Equal(t, tt.expectedResponse, outputs["response"],
					"response output mismatch")
			}
			assert.Equal(t, tt.expectedCompleted, outputs["completed"],
				"completed output mismatch")
		})
	}
}

// assertCELNull checks that a value is null in the CEL sense: either Go nil or structpb.NullValue.
func assertCELNull(t *testing.T, val interface{}, msgAndArgs ...interface{}) {
	t.Helper()
	if val == nil {
		return
	}
	if _, ok := val.(structpb.NullValue); ok {
		return
	}
	t.Errorf("expected null (nil or NullValue), got %T(%v) - %v", val, val, msgAndArgs)
}

// TestFilterUnresolvedTemplates tests the helper that removes unresolved {{...}} templates.
func TestFilterUnresolvedTemplates(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "all resolved",
			input:    []string{"submit_review", "submit_response"},
			expected: []string{"submit_review", "submit_response"},
		},
		{
			name:     "all unresolved",
			input:    []string{"{{inputs.response_tool_name}}"},
			expected: nil,
		},
		{
			name:     "mixed",
			input:    []string{"submit_review", "{{inputs.other}}"},
			expected: []string{"submit_review"},
		},
		{
			name:     "empty",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "nil",
			input:    nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterUnresolvedTemplates(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestResponseToolDetectionInLoop tests that response tool detection works in loop contexts
func TestResponseToolDetectionInLoop(t *testing.T) {
	t.Run("inline workflow can detect response tools", func(t *testing.T) {
		wfJSON := `{
			"name": "test-inline",
			"entry": ["call_llm"],
			"nodes": [
				{
					"id": "call_llm",
					"type": "call_llm",
					"args": {
						"response_tool": {
							"name": "my_response",
							"description": "Test response tool",
							"schema": {
								"type": "object",
								"properties": {
									"result": {"type": "string"}
								}
							}
						}
					}
				},
				{
					"id": "execute_tools",
					"type": "execute_tools",
					"args": {
						"tool_calls": "{{nodes.call_llm.tool_calls}}"
					}
				}
			],
			"edges": [{"from": "call_llm", "default": "execute_tools"}],
			"outputs": {"response_data": "{{nodes.execute_tools.response_data}}"}
		}`
		inlineWf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		toolCallsArg := "{{nodes.call_llm.tool_calls}}"
		expectedTools, schemas := detectResponseToolsFromWorkflow(toolCallsArg, inlineWf.Nodes, nil)

		t.Logf("Detected tools: %v", expectedTools)
		t.Logf("Detected schemas: %v", schemas)

		assert.Equal(t, []string{"my_response"}, expectedTools, "should detect response tool name")
		assert.Contains(t, schemas, "my_response", "should have schema for response tool")
	})

	t.Run("detection handles template expressions in response_tool.name", func(t *testing.T) {
		wfJSON := `{
			"name": "test-dynamic-name",
			"entry": ["call_llm"],
			"nodes": [
				{
					"id": "call_llm",
					"type": "call_llm",
					"args": {
						"response_tool": {
							"name": "{{inputs.response_tool_name}}",
							"description": "Dynamic response tool"
						}
					}
				},
				{
					"id": "execute_tools",
					"type": "execute_tools",
					"args": {
						"tool_calls": "{{nodes.call_llm.tool_calls}}"
					}
				}
			],
			"edges": []
		}`
		inlineWf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		toolCallsArg := "{{nodes.call_llm.tool_calls}}"
		expectedTools, _ := detectResponseToolsFromWorkflow(toolCallsArg, inlineWf.Nodes, nil)

		assert.Nil(t, expectedTools, "should not detect tools when name is a template expression without inputs")
	})

	t.Run("detection resolves template expression with workflow inputs", func(t *testing.T) {
		wfJSON := `{
			"name": "test-dynamic-name",
			"entry": ["call_llm"],
			"nodes": [
				{
					"id": "call_llm",
					"type": "call_llm",
					"args": {
						"response_tool": {
							"name": "{{inputs.response_tool_name}}",
							"description": "Dynamic response tool",
							"schema": {
								"type": "object",
								"required": ["strategy", "winner"]
							}
						}
					}
				},
				{
					"id": "execute_tools",
					"type": "execute_tools",
					"args": {
						"tool_calls": "{{nodes.call_llm.tool_calls}}"
					}
				}
			],
			"edges": []
		}`
		inlineWf, err := LoadWorkflow([]byte(wfJSON))
		require.NoError(t, err)

		workflowInputs := map[string]interface{}{"response_tool_name": "submit_review"}
		toolCallsArg := "nodes.call_llm.tool_calls" // bare expression, as CelStringRaw returns
		expectedTools, schemas := detectResponseToolsFromWorkflow(toolCallsArg, inlineWf.Nodes, workflowInputs)

		assert.Equal(t, []string{"submit_review"}, expectedTools, "should resolve response tool name from inputs")
		assert.Contains(t, schemas, "submit_review", "should have schema for resolved response tool")
	})
}
