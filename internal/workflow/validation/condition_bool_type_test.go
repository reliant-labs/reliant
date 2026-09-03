package validation

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEdgeCondition_BoolType tests that edge conditions must return boolean type.
func TestEdgeCondition_BoolType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		condition      string
		shouldPass     bool
		expectedErrMsg string
	}{
		{
			name:       "Valid: boolean from comparison",
			condition:  "nodes.call_llm.token_count > 0",
			shouldPass: true,
		},
		{
			name:       "Valid: boolean comparison",
			condition:  "size(nodes.call_llm.tool_calls) > 0",
			shouldPass: true,
		},
		{
			name:       "Valid: equality comparison",
			condition:  "nodes.call_llm.response_text == 'hello'",
			shouldPass: true,
		},
		{
			name:       "Valid: numeric comparison",
			condition:  "nodes.call_llm.token_count > 100",
			shouldPass: true,
		},
		{
			name:           "Invalid: integer without comparison",
			condition:      "size(nodes.call_llm.tool_calls)",
			shouldPass:     false,
			expectedErrMsg: "condition must return bool, but expression returns",
		},
		{
			name:           "Invalid: string field",
			condition:      "nodes.call_llm.message.role",
			shouldPass:     false,
			expectedErrMsg: "condition must return bool, but expression returns 'string'",
		},
		{
			name:           "Invalid: string field without comparison",
			condition:      "nodes.call_llm.response_text",
			shouldPass:     false,
			expectedErrMsg: "condition must return bool, but expression returns 'string'",
		},
		{
			name:       "Valid: logical AND",
			condition:  "nodes.call_llm.token_count > 0 && size(nodes.call_llm.tool_calls) > 0",
			shouldPass: true,
		},
		{
			name:       "Valid: logical OR",
			condition:  "nodes.call_llm.token_count > 100 || size(nodes.call_llm.tool_calls) > 0",
			shouldPass: true,
		},
		{
			name:       "Valid: negation",
			condition:  "!(nodes.call_llm.token_count > 1000)",
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `
name: test-edge-condition-bool
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    model:
      tags: [flagship]
  - id: next_step
    type: call_llm
    model:
      tags: [flagship]
edges:
  - from: call_llm
    cases:
      - to: next_step
        condition: "` + tt.condition + `"
`

			wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.shouldPass {
				// Filter to only condition-related errors
				var conditionErrors []*Error
				for _, e := range errors {
					if len(e.Path) > 0 && e.Path[len(e.Path)-1] == "condition" {
						conditionErrors = append(conditionErrors, e)
					}
				}
				for _, e := range conditionErrors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, conditionErrors, "expected no validation errors for valid boolean condition")
			} else {
				require.NotEmpty(t, errors, "expected validation error for non-boolean condition")
				found := false
				for _, e := range errors {
					t.Logf("Error: %s - %s", pathToString(e.Path), e.Message)
					if contains(e.Message, tt.expectedErrMsg) {
						found = true
						// Check that suggestion is provided
						assert.NotEmpty(t, e.Suggestion, "expected suggestion for non-boolean condition")
						t.Logf("Suggestion: %s", e.Suggestion)
					}
				}
				assert.True(t, found, "expected error message containing '%s'", tt.expectedErrMsg)
			}
		})
	}
}

// TestNodeCondition_BoolType tests that node conditions must return boolean type.
func TestNodeCondition_BoolType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		condition      string
		shouldPass     bool
		expectedErrMsg string
	}{
		{
			name:       "Valid: boolean input",
			condition:  "inputs.should_run",
			shouldPass: true,
		},
		{
			name:       "Valid: comparison",
			condition:  "inputs.count > 5",
			shouldPass: true,
		},
		{
			name:           "Invalid: integer input without comparison",
			condition:      "inputs.count",
			shouldPass:     false,
			expectedErrMsg: "condition must return bool, but expression returns 'int'",
		},
		{
			name:           "Invalid: string input without comparison",
			condition:      "inputs.name",
			shouldPass:     false,
			expectedErrMsg: "condition must return bool, but expression returns 'string'",
		},
		{
			name:       "Valid: node output comparison",
			condition:  "nodes.prev.token_count > 50",
			shouldPass: true,
		},
		{
			name:       "Valid: complex boolean expression",
			condition:  "inputs.enabled && nodes.prev.token_count > 0",
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `
name: test-node-condition-bool
entry: [prev, conditional_step]
inputs:
  should_run:
    type: boolean
    default: true
  count:
    type: integer
    default: 10
  name:
    type: string
    default: "test"
  enabled:
    type: boolean
    default: true
nodes:
  - id: prev
    type: call_llm
    model:
      tags: [flagship]
  - id: conditional_step
    type: call_llm
    condition: "` + tt.condition + `"
    model:
      tags: [flagship]
`

			wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.shouldPass {
				// Filter to only condition-related errors
				var conditionErrors []*Error
				for _, e := range errors {
					if len(e.Path) > 0 && e.Path[len(e.Path)-1] == "condition" {
						conditionErrors = append(conditionErrors, e)
					}
				}
				for _, e := range conditionErrors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, conditionErrors, "expected no validation errors for valid boolean condition")
			} else {
				require.NotEmpty(t, errors, "expected validation error for non-boolean condition")
				found := false
				for _, e := range errors {
					t.Logf("Error: %s - %s", pathToString(e.Path), e.Message)
					if contains(e.Message, tt.expectedErrMsg) {
						found = true
						// Check that suggestion is provided
						assert.NotEmpty(t, e.Suggestion, "expected suggestion for non-boolean condition")
						t.Logf("Suggestion: %s", e.Suggestion)
					}
				}
				assert.True(t, found, "expected error message containing '%s'", tt.expectedErrMsg)
			}
		})
	}
}

// TestEdgeCondition_BoolType_DetailedSuggestions tests that error suggestions are helpful.
func TestEdgeCondition_BoolType_DetailedSuggestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		condition          string
		expectedType       string
		expectedSuggestion string
	}{
		{
			name:               "Integer - suggest comparison",
			condition:          "size(nodes.call_llm.tool_calls)",
			expectedType:       "int",
			expectedSuggestion: "> 0",
		},
		{
			name:               "String - suggest comparison",
			condition:          "nodes.call_llm.response_text",
			expectedType:       "string",
			expectedSuggestion: "!=",
		},
		{
			name:               "Numeric field - suggest comparison",
			condition:          "nodes.call_llm.token_count",
			expectedType:       "int",
			expectedSuggestion: "> 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `
name: test-suggestions
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    model:
      tags: [flagship]
  - id: next
    type: call_llm
    model:
      tags: [flagship]
edges:
  - from: call_llm
    cases:
      - to: next
        condition: "` + tt.condition + `"
`

			wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			require.NotEmpty(t, errors, "expected validation error")

			found := false
			for _, e := range errors {
				if contains(e.Message, "condition must return bool") {
					found = true
					assert.Contains(t, e.Message, tt.expectedType, "error should mention the actual type")
					assert.Contains(t, e.Suggestion, tt.expectedSuggestion, "suggestion should contain helpful hint")
					t.Logf("Error: %s", e.Message)
					t.Logf("Suggestion: %s", e.Suggestion)
				}
			}
			assert.True(t, found, "expected error about non-boolean condition")
		})
	}
}

// TestLoopWhileCondition_BoolType tests that loop while conditions must return boolean.
// Note: Loop while conditions are validated separately, but should also enforce bool type.
func TestLoopWhileCondition_BoolType(t *testing.T) {
	t.Parallel()
	t.Run("Valid: boolean comparison", func(t *testing.T) {
		workflowYAML := `
name: test-loop-while-bool
entry: [loop]
nodes:
  - id: loop
    type: loop
    while: "iter._index < 5"
    inline:
      name: loop-iteration
      entry: [step]
      nodes:
        - id: step
          type: call_llm
          model:
            tags: [flagship]
`

		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		// Should have no errors about boolean type
		errors := result.Errors()
		for _, e := range errors {
			if contains(e.Message, "condition must return bool") {
				t.Errorf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
			}
		}
	})

	// Note: Loop while conditions are complex and may have separate validation
	// This test documents expected behavior
}

// TestConditionBoolType_ComplexExpressions tests boolean validation with complex expressions.
func TestConditionBoolType_ComplexExpressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		condition  string
		shouldPass bool
	}{
		{
			name:       "Valid: ternary with boolean result",
			condition:  "nodes.call_llm.token_count > 100 ? true : false",
			shouldPass: true,
		},
		{
			name:       "Valid: in operator",
			condition:  "nodes.call_llm.response_text in ['yes', 'no']",
			shouldPass: true,
		},
		{
			name:       "Valid: null check",
			condition:  "nodes.call_llm.message != null",
			shouldPass: true,
		},
		{
			name:       "Valid: has() function",
			condition:  "has(nodes.call_llm.message)",
			shouldPass: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `
name: test-complex-conditions
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    model:
      tags: [flagship]
  - id: next
    type: call_llm
    model:
      tags: [flagship]
edges:
  - from: call_llm
    cases:
      - to: next
        condition: "` + tt.condition + `"
`

			wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
			require.NoError(t, err)

			result := &Result{}
			ValidateCELWithCompilation(wf, result, nil)

			errors := result.Errors()
			if tt.shouldPass {
				// Filter to only boolean type errors
				var boolErrors []*Error
				for _, e := range errors {
					if contains(e.Message, "condition must return bool") {
						boolErrors = append(boolErrors, e)
					}
				}
				for _, e := range boolErrors {
					t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
				}
				assert.Empty(t, boolErrors, "expected no boolean type errors for valid condition")
			}
		})
	}
}
