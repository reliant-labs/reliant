package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// celLiteral creates a CelString with a literal value (e.g., "user", "hello").
func celLiteral(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

// celExpr creates a CelString with a CEL expression (e.g., "{{inputs.message.role}}").
func celExpr(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: s}}
}

func TestEvaluateSaveMessageConfig_NullAttachments(t *testing.T) {
	t.Parallel()
	// This test verifies that CEL expressions returning null for attachments
	// are handled correctly. The expression:
	//   "{{has(inputs.attachments) ? inputs.attachments : null}}"
	// returns structpb.NullValue when attachments doesn't exist, which was causing
	// "expected string array, got structpb.NullValue" errors.
	//
	// NOTE: Use inputs.X (not workflow.inputs.X) - see cel_env.go for namespace docs.

	tests := []struct {
		name            string
		config          *reliantv1.SaveMessageConfig
		workflowContext map[string]interface{} // This is what gets put into context["workflow"]
		expectError     bool
		expectNil       bool // true if attachments should be nil
	}{
		{
			name: "null attachments via CEL ternary",
			config: &reliantv1.SaveMessageConfig{
				Role:        celExpr("{{inputs.message.role}}"),
				Content:     celExpr("{{inputs.message.text}}"),
				Attachments: celExpr("{{has(inputs.attachments) ? inputs.attachments : null}}"),
			},
			workflowContext: map[string]interface{}{
				"inputs": map[string]interface{}{
					"message": map[string]interface{}{
						"role": "user",
						"text": "hello",
					},
					// attachments NOT present
				},
			},
			expectError: false,
			expectNil:   true,
		},
		{
			name: "empty attachments array",
			config: &reliantv1.SaveMessageConfig{
				Role:        celLiteral("user"),
				Content:     celLiteral("hello"),
				Attachments: celExpr("{{inputs.attachments}}"),
			},
			workflowContext: map[string]interface{}{
				"inputs": map[string]interface{}{
					"attachments": []interface{}{},
				},
			},
			expectError: false,
			expectNil:   false, // empty array, not nil
		},
		{
			name: "valid attachments array",
			config: &reliantv1.SaveMessageConfig{
				Role:        celLiteral("user"),
				Content:     celLiteral("hello"),
				Attachments: celExpr("{{inputs.attachments}}"),
			},
			workflowContext: map[string]interface{}{
				"inputs": map[string]interface{}{
					"attachments": []interface{}{"file1.txt", "file2.txt"},
				},
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name: "no attachments config",
			config: &reliantv1.SaveMessageConfig{
				Role:    celLiteral("user"),
				Content: celLiteral("hello"),
				// Attachments not set
			},
			workflowContext: map[string]interface{}{
				"inputs": map[string]interface{}{},
			},
			expectError: false,
			expectNil:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evalResult, err := evaluateSaveMessageConfig(
				tc.config,
				map[string]interface{}{}, // activityOutput
				tc.workflowContext,
				map[string]interface{}{}, // nodeOutputs
				"chat-123",
				"thread-path",
				"workflow-123",
				"step-id",
				nil, // execContext
				nil, // iter (not in a loop)
			)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, evalResult)

			if tc.expectNil {
				assert.Nil(t, evalResult.Attachments)
			} else {
				assert.NotNil(t, evalResult.Attachments)
			}
		})
	}
}

// TestEvaluateSaveMessageConfig_IterInContent is a regression test for the
// get-it-right feedback-bridge bug: a save_message declared on a node inside a loop
// referenced "{{iter.iteration + 1}}" in its content, but the save_message CEL
// context (PostActivityContext) did not declare the iter namespace. That made the
// template fail CEL compilation; the error was swallowed as non-fatal by the loop
// executor, so the review feedback / implementation summaries were silently never
// written to the parent thread and every fresh implement fork ran blind.
//
// Before the fix this errored ("CEL compilation error: undeclared reference to
// 'iter'"). After the fix iter resolves from the loop iteration (or a zero default
// when not in a loop).
func TestEvaluateSaveMessageConfig_IterInContent(t *testing.T) {
	t.Parallel()
	config := &reliantv1.SaveMessageConfig{
		Role:    celLiteral("assistant"),
		Content: celExpr("## Implementation Notes (Attempt {{iter.iteration + 1}})"),
	}
	workflowContext := map[string]interface{}{
		"inputs": map[string]interface{}{},
	}

	tests := []struct {
		name        string
		iter        *model.IterContext
		wantContent string
	}{
		{
			name:        "inside loop resolves iteration",
			iter:        &model.IterContext{Iteration: 1, Index: 1},
			wantContent: "## Implementation Notes (Attempt 2)",
		},
		{
			name:        "not in a loop uses zero default",
			iter:        nil,
			wantContent: "## Implementation Notes (Attempt 1)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evalResult, err := evaluateSaveMessageConfig(
				config,
				map[string]interface{}{}, // activityOutput
				workflowContext,
				map[string]interface{}{}, // nodeOutputs
				"chat-123",
				"thread-path",
				"workflow-123",
				"step-id",
				nil, // execContext
				tc.iter,
			)

			require.NoError(t, err)
			require.NotNil(t, evalResult)
			assert.Equal(t, tc.wantContent, evalResult.Content)
		})
	}
}

func TestEvaluateSaveMessageConfig_ThreadField(t *testing.T) {
	t.Parallel()
	// Thread is now always inherited from execution context (workflowThread parameter)
	// No explicit thread field in SaveMessageConfig anymore
	t.Run("thread is always inherited from workflow context", func(t *testing.T) {
		config := &reliantv1.SaveMessageConfig{
			Role:    celLiteral("user"),
			Content: celLiteral("hello"),
		}
		workflowContext := map[string]interface{}{
			"inputs": map[string]interface{}{},
		}
		defaultThread := "main-thread"

		evalResult, err := evaluateSaveMessageConfig(
			config,
			map[string]interface{}{}, // activityOutput
			workflowContext,
			map[string]interface{}{}, // nodeOutputs
			"chat-123",
			defaultThread,
			"workflow-123",
			"step-id",
			nil, // execContext
			nil, // iter (not in a loop)
		)

		require.NoError(t, err)
		require.NotNil(t, evalResult)

		// Thread should always be the workflowThread (defaultThread parameter)
		assert.Equal(t, defaultThread, evalResult.Thread)
	})
}

func TestEvaluateCELTemplate_MultilineYAML(t *testing.T) {
	t.Parallel()
	// This test verifies that multi-line YAML strings (using | operator) are
	// correctly handled when they contain pure CEL expressions.
	// YAML's | operator preserves whitespace, which was causing pure expressions
	// like {{expr}} to be treated as mixed strings when surrounded by indentation.

	tests := []struct {
		name        string
		expr        string
		ctx         wfcel.CELEvalContext
		expectArray bool // true if result should be an array, false if string
	}{
		{
			name: "single line pure expression",
			expr: "{{nodes.step1.results}}",
			ctx: &wfcel.EdgeEvalContext{
				Nodes: map[string]interface{}{
					"step1": map[string]interface{}{
						"results": []interface{}{"a", "b", "c"},
					},
				},
				Inputs:   map[string]interface{}{},
				Workflow: &model.WorkflowContext{},
			},
			expectArray: true,
		},
		{
			// Simulates YAML: tool_results: |
			//   {{nodes.step1.results}}
			name: "multi-line YAML with leading/trailing whitespace",
			expr: "\n  {{nodes.step1.results}}\n",
			ctx: &wfcel.EdgeEvalContext{
				Nodes: map[string]interface{}{
					"step1": map[string]interface{}{
						"results": []interface{}{"a", "b", "c"},
					},
				},
				Inputs:   map[string]interface{}{},
				Workflow: &model.WorkflowContext{},
			},
			expectArray: true,
		},
		{
			// Simulates YAML: tool_results: |
			//   {{
			//     nodes.step1.results.map(r, r + "!")
			//   }}
			name: "multi-line YAML with complex expression spanning lines",
			expr: "\n  {{\n    nodes.step1.results.map(r, r + \"!\")\n  }}\n",
			ctx: &wfcel.EdgeEvalContext{
				Nodes: map[string]interface{}{
					"step1": map[string]interface{}{
						"results": []interface{}{"a", "b"},
					},
				},
				Inputs:   map[string]interface{}{},
				Workflow: &model.WorkflowContext{},
			},
			expectArray: true,
		},
		{
			name: "mixed string with expression should remain string",
			expr: "prefix {{nodes.step1.value}} suffix",
			ctx: &wfcel.EdgeEvalContext{
				Nodes: map[string]interface{}{
					"step1": map[string]interface{}{
						"value": "hello",
					},
				},
				Inputs:   map[string]interface{}{},
				Workflow: &model.WorkflowContext{},
			},
			expectArray: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := wfcel.EvaluateTemplate(tc.expr, tc.ctx)
			require.NoError(t, err)

			if tc.expectArray {
				// CEL can return different array types: []interface{}, []ref.Val, etc.
				// Check it's not a string (the bug we're fixing)
				_, isString := result.(string)
				assert.False(t, isString, "expected non-string (array) but got string: %v", result)
			} else {
				_, isString := result.(string)
				assert.True(t, isString, "expected string but got %T: %v", result, result)
			}
		})
	}
}

func TestConvertToToolCalls_TypeAssertions(t *testing.T) {
	t.Parallel()
	// Test that type assertion failures are caught and reported, not silently ignored
	tests := []struct {
		name        string
		input       []map[string]interface{}
		expectError bool
		errorMatch  string
	}{
		{
			name: "valid tool calls",
			input: []map[string]interface{}{
				{
					"id":   "call_123",
					"name": "test_tool",
				},
			},
			expectError: false,
		},
		{
			name: "valid tool calls with optional fields",
			input: []map[string]interface{}{
				{
					"id":                "call_123",
					"name":              "test_tool",
					"input":             "{\"arg\":\"value\"}",
					"thought_signature": "sig_456",
				},
			},
			expectError: false,
		},
		{
			name: "missing id - should fail",
			input: []map[string]interface{}{
				{
					"name": "test_tool",
				},
			},
			expectError: true,
			errorMatch:  "tool_calls[0].id: expected string, got",
		},
		{
			name: "id has wrong type - should fail",
			input: []map[string]interface{}{
				{
					"id":   123, // int instead of string
					"name": "test_tool",
				},
			},
			expectError: true,
			errorMatch:  "tool_calls[0].id: expected string, got int",
		},
		{
			name: "name has wrong type - should fail",
			input: []map[string]interface{}{
				{
					"id":   "call_123",
					"name": []string{"wrong", "type"}, // array instead of string
				},
			},
			expectError: true,
			errorMatch:  "tool_calls[0].name: expected string, got",
		},
		{
			name: "input has wrong type - should fail",
			input: []map[string]interface{}{
				{
					"id":    "call_123",
					"name":  "test_tool",
					"input": 123, // int instead of string
				},
			},
			expectError: true,
			errorMatch:  "tool_calls[0].input: expected string, got int",
		},
		{
			name: "nil input is allowed (optional field)",
			input: []map[string]interface{}{
				{
					"id":    "call_123",
					"name":  "test_tool",
					"input": nil,
				},
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := convertToToolCalls(tc.input)

			if tc.expectError {
				require.Error(t, err)
				if tc.errorMatch != "" {
					assert.Contains(t, err.Error(), tc.errorMatch)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestConvertToToolResults_TypeAssertions(t *testing.T) {
	t.Parallel()
	// Test that type assertion failures are caught and reported, not silently ignored
	tests := []struct {
		name        string
		input       []map[string]interface{}
		expectError bool
		errorMatch  string
	}{
		{
			name: "valid tool results",
			input: []map[string]interface{}{
				{
					"tool_call_id": "call_123",
					"content":      "result text",
				},
			},
			expectError: false,
		},
		{
			name: "missing tool_call_id - should fail",
			input: []map[string]interface{}{
				{
					"content": "result text",
				},
			},
			expectError: true,
			errorMatch:  "tool_results[0].tool_call_id: expected string, got",
		},
		{
			name: "missing content - should fail",
			input: []map[string]interface{}{
				{
					"tool_call_id": "call_123",
				},
			},
			expectError: true,
			errorMatch:  "tool_results[0].content: expected string, got",
		},
		{
			name: "is_error has wrong type - should fail",
			input: []map[string]interface{}{
				{
					"tool_call_id": "call_123",
					"content":      "result text",
					"is_error":     "not_a_bool",
				},
			},
			expectError: true,
			errorMatch:  "tool_results[0].is_error: expected bool, got string",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := convertToToolResults(tc.input)

			if tc.expectError {
				require.Error(t, err)
				if tc.errorMatch != "" {
					assert.Contains(t, err.Error(), tc.errorMatch)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

func TestEvaluateSaveMessageConfig_ThinkingOutput(t *testing.T) {
	t.Parallel()
	// Test that thinking output is correctly extracted from activity output
	// and included in the SaveMessageInput for proper persistence.

	tests := []struct {
		name           string
		activityOutput map[string]interface{}
		expectError    bool
		errorMatch     string
	}{
		{
			name: "valid thinking with content and signature",
			activityOutput: map[string]interface{}{
				"thinking": map[string]interface{}{
					"content":   "I need to think about this...",
					"signature": "sig_abc123",
				},
			},
			expectError: false,
		},
		{
			name: "thinking with content only (no signature)",
			activityOutput: map[string]interface{}{
				"thinking": map[string]interface{}{
					"content": "thinking without signature",
				},
			},
			expectError: false,
		},
		{
			name: "thinking with empty content - should skip",
			activityOutput: map[string]interface{}{
				"thinking": map[string]interface{}{
					"content": "",
				},
			},
			expectError: false,
		},
		{
			name: "thinking has wrong type - should fail",
			activityOutput: map[string]interface{}{
				"thinking": "not a map",
			},
			expectError: true,
			errorMatch:  "thinking: expected map[string]interface{}, got string",
		},
		{
			name: "thinking content has wrong type - should fail",
			activityOutput: map[string]interface{}{
				"thinking": map[string]interface{}{
					"content": 123, // int instead of string
				},
			},
			expectError: true,
			errorMatch:  "thinking.content: expected string, got int",
		},
		{
			name: "thinking signature has wrong type - should fail",
			activityOutput: map[string]interface{}{
				"thinking": map[string]interface{}{
					"content":   "thinking content",
					"signature": 123, // int instead of string
				},
			},
			expectError: true,
			errorMatch:  "thinking.signature: expected string, got int",
		},
		{
			name:           "no thinking field - should work",
			activityOutput: map[string]interface{}{},
			expectError:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &reliantv1.SaveMessageConfig{
				Role:    celLiteral("assistant"),
				Content: celLiteral("test"),
			}
			workflowContext := map[string]interface{}{
				"inputs": map[string]interface{}{},
			}

			result, err := evaluateSaveMessageConfig(
				config,
				tc.activityOutput,
				workflowContext,
				map[string]interface{}{}, // nodeOutputs
				"chat-123",
				"thread-path",
				"workflow-123",
				"step-id",
				nil, // execContext
				nil, // iter (not in a loop)
			)

			if tc.expectError {
				require.Error(t, err)
				if tc.errorMatch != "" {
					assert.Contains(t, err.Error(), tc.errorMatch)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, result)
			}
		})
	}
}

// TestEvaluateSaveMessageConfig_ModelAndAgent verifies that the resolved model
// is auto-extracted from the activity output (CallLLMOutput.model) and the agent
// identity from the workflow context, so messages.model / messages.agent are
// populated for assistant messages.
func TestEvaluateSaveMessageConfig_ModelAndAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		activityOutput  map[string]interface{}
		workflowContext map[string]interface{}
		wantModel       string
		wantAgent       string
	}{
		{
			name:           "model from output, agent from workflow name",
			activityOutput: map[string]interface{}{"model": "claude-4.8-opus"},
			workflowContext: map[string]interface{}{
				workflowContextKeyName:   "builtin://agent",
				workflowContextKeyInputs: map[string]interface{}{},
			},
			wantModel: "claude-4.8-opus",
			wantAgent: "builtin://agent",
		},
		{
			name:           "agent_name input wins over workflow name",
			activityOutput: map[string]interface{}{"model": "gpt-5.5"},
			workflowContext: map[string]interface{}{
				workflowContextKeyName:      "get-it-right",
				workflowContextKeyAgentName: "researcher",
				workflowContextKeyInputs:    map[string]interface{}{},
			},
			wantModel: "gpt-5.5",
			wantAgent: "researcher",
		},
		{
			name:           "no model, no agent",
			activityOutput: map[string]interface{}{},
			workflowContext: map[string]interface{}{
				workflowContextKeyInputs: map[string]interface{}{},
			},
			wantModel: "",
			wantAgent: "",
		},
		{
			name:           "non-string model is ignored",
			activityOutput: map[string]interface{}{"model": 123},
			workflowContext: map[string]interface{}{
				workflowContextKeyName:   "agent",
				workflowContextKeyInputs: map[string]interface{}{},
			},
			wantModel: "",
			wantAgent: "agent",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := &reliantv1.SaveMessageConfig{
				Role:    celLiteral("assistant"),
				Content: celLiteral("test"),
			}

			result, err := evaluateSaveMessageConfig(
				config,
				tc.activityOutput,
				tc.workflowContext,
				map[string]interface{}{}, // nodeOutputs
				"chat-123",
				"thread-path",
				"workflow-123",
				"step-id",
				nil, // execContext
				nil, // iter
			)

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantModel, result.Model)
			assert.Equal(t, tc.wantAgent, result.Agent)
		})
	}
}
