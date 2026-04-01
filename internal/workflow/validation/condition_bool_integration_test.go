package validation

import (
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConditionBoolType_Integration demonstrates the boolean type validation
// working end-to-end with realistic workflow examples.
func TestConditionBoolType_Integration(t *testing.T) {
	t.Run("Valid workflow with boolean conditions", func(t *testing.T) {
		workflowYAML := `
name: valid-workflow
entry: [check_input]
inputs:
  retry_enabled:
    type: boolean
    default: false
  max_retries:
    type: integer
    default: 3
nodes:
  - id: check_input
    type: call_llm
    model:
      tags: [flagship]
    messages:
      - role: user
        content: "Analyze this"
        
  - id: process_result
    type: call_llm
    # Valid: boolean comparison
    condition: "inputs.retry_enabled && nodes.check_input.token_count < 1000"
    model:
      tags: [flagship]
      
  - id: call_tools
    type: execute_tools
    tool_calls: "{{nodes.check_input.tool_calls}}"
    
edges:
  - from: check_input
    cases:
      # Valid: size comparison returns bool
      - to: call_tools
        condition: "size(nodes.check_input.tool_calls) > 0"
      # Valid: string equality returns bool
      - to: process_result
        condition: "nodes.check_input.response_text != ''"
`

		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		// Should have no boolean type errors
		errors := result.Errors()
		var boolErrors []*Error
		for _, e := range errors {
			if contains(e.Message, "condition must return bool") {
				boolErrors = append(boolErrors, e)
				t.Logf("Unexpected boolean type error: %s - %s", pathToString(e.Path), e.Message)
			}
		}
		assert.Empty(t, boolErrors, "valid workflow should have no boolean type errors")
	})

	t.Run("Invalid workflow - common mistakes", func(t *testing.T) {
		workflowYAML := `
name: invalid-workflow
entry: [step1]
inputs:
  count:
    type: integer
    default: 5
nodes:
  - id: step1
    type: call_llm
    # INVALID: integer without comparison
    condition: "inputs.count"
    model:
      tags: [flagship]
      
  - id: step2
    type: call_llm
    model:
      tags: [flagship]
      
edges:
  - from: step1
    cases:
      # INVALID: size() returns int, needs comparison
      - to: step2
        condition: "size(nodes.step1.tool_calls)"
`

		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		// Should have 2 boolean type errors
		errors := result.Errors()
		var boolErrors []*Error
		for _, e := range errors {
			if contains(e.Message, "condition must return bool") {
				boolErrors = append(boolErrors, e)
				t.Logf("Expected error: %s - %s", pathToString(e.Path), e.Message)
				t.Logf("  Suggestion: %s", e.Suggestion)
			}
		}

		require.Len(t, boolErrors, 2, "expected 2 boolean type errors")

		// Check error details
		foundNodeConditionError := false
		foundEdgeConditionError := false

		for _, e := range boolErrors {
			if len(e.Path) > 0 {
				lastPathElement := e.Path[len(e.Path)-1]
				if lastPathElement == "condition" {
					// Check if it's a node condition or edge condition
					if len(e.Path) > 1 && contains(e.Path[1], "nodes") {
						foundNodeConditionError = true
						assert.Contains(t, e.Message, "'int'")
						assert.NotEmpty(t, e.Suggestion)
					} else if len(e.Path) > 1 && contains(e.Path[1], "edges") {
						foundEdgeConditionError = true
						assert.Contains(t, e.Message, "'int'")
						assert.NotEmpty(t, e.Suggestion)
					}
				}
			}
		}

		assert.True(t, foundNodeConditionError, "should have node condition error")
		assert.True(t, foundEdgeConditionError, "should have edge condition error")
	})

	t.Run("Real-world pattern - tool routing", func(t *testing.T) {
		// This is a common pattern that should work
		workflowYAML := `
name: tool-routing
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    model:
      tags: [flagship]
    messages:
      - role: user
        content: "Hello"
        
  - id: tools
    type: execute_tools
    tool_calls: "{{nodes.llm.tool_calls}}"
    
  - id: done
    type: call_llm
    model:
      tags: [flagship]
      
edges:
  - from: llm
    cases:
      # Valid pattern: check if tools were requested
      - to: tools
        condition: "size(nodes.llm.tool_calls) > 0"
    default: done
`

		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		// Should have no errors
		errors := result.Errors()
		var boolErrors []*Error
		for _, e := range errors {
			if contains(e.Message, "condition must return bool") {
				boolErrors = append(boolErrors, e)
				t.Logf("Unexpected error: %s - %s", pathToString(e.Path), e.Message)
			}
		}
		assert.Empty(t, boolErrors, "tool routing pattern should have no boolean type errors")
	})

	t.Run("Common mistake - forgetting comparison", func(t *testing.T) {
		// This is what users might write by mistake
		workflowYAML := `
name: common-mistake
entry: [llm]
nodes:
  - id: llm
    type: call_llm
    model:
      tags: [flagship]
      
  - id: tools
    type: execute_tools
    tool_calls: "{{nodes.llm.tool_calls}}"
    
edges:
  - from: llm
    cases:
      # MISTAKE: forgot to compare size to 0
      - to: tools
        condition: "size(nodes.llm.tool_calls)"
`

		wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
		require.NoError(t, err)

		result := &Result{}
		ValidateCELWithCompilation(wf, result, nil)

		// Should catch this mistake
		errors := result.Errors()
		var found bool
		for _, e := range errors {
			if contains(e.Message, "condition must return bool") && contains(e.Message, "'int'") {
				found = true
				t.Logf("✓ Caught the mistake!")
				t.Logf("  Error: %s", e.Message)
				t.Logf("  Suggestion: %s", e.Suggestion)
				// Check that suggestion mentions comparison
				assert.Contains(t, e.Suggestion, "> 0")
			}
		}
		assert.True(t, found, "should catch the size() without comparison mistake")
	})
}

// TestConditionBoolType_ErrorQuality tests that error messages are helpful and actionable.
func TestConditionBoolType_ErrorQuality(t *testing.T) {
	tests := []struct {
		name               string
		condition          string
		expectedType       string
		suggestionContains string
	}{
		{
			name:               "Integer field",
			condition:          "nodes.call_llm.token_count",
			expectedType:       "int",
			suggestionContains: "> 0",
		},
		{
			name:               "String field",
			condition:          "nodes.call_llm.response_text",
			expectedType:       "string",
			suggestionContains: "!=",
		},
		{
			name:               "Size function",
			condition:          "size(nodes.call_llm.tool_calls)",
			expectedType:       "int",
			suggestionContains: "> 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflowYAML := `
name: error-quality-test
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
			var found bool
			for _, e := range errors {
				if contains(e.Message, "condition must return bool") {
					found = true

					// Check error message quality
					assert.Contains(t, e.Message, tt.expectedType,
						"error should mention the actual type")

					// Check suggestion quality
					assert.NotEmpty(t, e.Suggestion,
						"error should have a suggestion")
					assert.Contains(t, e.Suggestion, tt.suggestionContains,
						"suggestion should be relevant to the type")

					// Check path is clear
					assert.NotEmpty(t, e.Path,
						"error should have a path")

					t.Logf("✓ Error message: %s", e.Message)
					t.Logf("✓ Suggestion: %s", e.Suggestion)
					t.Logf("✓ Path: %s", pathToString(e.Path))
				}
			}
			assert.True(t, found, "should have boolean type error")
		})
	}
}
