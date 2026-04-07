package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateResponseDataAccess_ValidAccess tests that valid response_data field access passes.
func TestValidateResponseDataAccess_ValidAccess(t *testing.T) {
	workflowYAML := `
name: test-valid-response-data
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: review
        description: Submit a code review
        schema:
          type: object
          properties:
            verdict:
              type: string
            confidence:
              type: number
            issues:
              type: array
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm.tool_calls}}"
edges:
  - from: call_llm
    to: execute_tools
  - from: execute_tools
    cases:
      - to: done
        condition: "nodes.execute_tools.response_data.review.verdict == 'approve'"
  - from: done
    to: ~
outputs:
  verdict: "{{nodes.execute_tools.response_data.review.verdict}}"
  confidence: "{{nodes.execute_tools.response_data.review.confidence}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have no errors for valid response_data access
	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for valid response_data access")
}

// TestValidateResponseDataAccess_InvalidToolName tests that invalid tool names are caught.
func TestValidateResponseDataAccess_InvalidToolName(t *testing.T) {
	workflowYAML := `
name: test-invalid-tool-name
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: review
        description: Submit a code review
        schema:
          type: object
          properties:
            verdict:
              type: string
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm.tool_calls}}"
outputs:
  # Typo: 'reveiw' instead of 'review'
  verdict: "{{nodes.execute_tools.response_data.reveiw.verdict}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have an error about unknown response tool
	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for invalid tool name")

	found := false
	for _, e := range errors {
		t.Logf("Error: %s - %s (suggestion: %s)", e.Path, e.Message, e.Suggestion)
		if strings.Contains(e.Message, "unknown response tool 'reveiw'") {
			found = true
			// Should suggest 'review'
			assert.Contains(t, e.Suggestion, "review", "should suggest 'review'")
		}
	}
	assert.True(t, found, "expected error about unknown response tool 'reveiw'")
}

// TestValidateResponseDataAccess_InvalidFieldName tests that invalid field names are caught.
func TestValidateResponseDataAccess_InvalidFieldName(t *testing.T) {
	workflowYAML := `
name: test-invalid-field-name
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: review
        description: Submit a code review
        schema:
          type: object
          properties:
            verdict:
              type: string
            confidence:
              type: number
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm.tool_calls}}"
outputs:
  # Typo: 'verdic' instead of 'verdict'
  verdict: "{{nodes.execute_tools.response_data.review.verdic}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have an error about unknown field
	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for invalid field name")

	found := false
	for _, e := range errors {
		t.Logf("Error: %s - %s (suggestion: %s)", e.Path, e.Message, e.Suggestion)
		if strings.Contains(e.Message, "has no field 'verdic'") {
			found = true
			assert.Contains(t, e.Message, "response_data", "error should include response_data context")
			assert.Contains(t, e.Message, "Available fields:", "error should enumerate available fields")
			// Should suggest 'verdict'
			assert.Contains(t, e.Suggestion, "verdict", "should suggest 'verdict'")
		}
	}
	assert.True(t, found, "expected error about unknown field 'verdic'")
}

// TestValidateResponseDataAccess_NoResponseTool tests that accessing response_data
// without a response tool defined doesn't cause errors (lenient for MCP/external tools).
func TestValidateResponseDataAccess_NoResponseTool(t *testing.T) {
	workflowYAML := `
name: test-no-response-tool
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: claude-4-sonnet
    # No response_tool defined
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm.tool_calls}}"
outputs:
  # Accessing response_data without a defined response tool - should be lenient
  result: "{{nodes.execute_tools.response_data.some_tool.field}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should NOT have errors - we're lenient when no response tool is defined
	// (could be MCP tools or dynamically generated tools)
	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors when no response_tool is defined")
}

// TestValidateResponseDataAccess_EdgeCondition tests validation in edge conditions.
func TestValidateResponseDataAccess_EdgeCondition(t *testing.T) {
	workflowYAML := `
name: test-edge-condition
entry: [call_llm]
nodes:
  - id: call_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: decision
        schema:
          type: object
          properties:
            approved:
              type: boolean
            reason:
              type: string
  - id: execute_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm.tool_calls}}"
  - id: approved
    type: call_llm
    args:
      model: claude-4-sonnet
  - id: rejected
    type: call_llm
    args:
      model: claude-4-sonnet
edges:
  - from: call_llm
    to: execute_tools
  - from: execute_tools
    cases:
      # Typo: 'aproved' instead of 'approved'
      - to: approved
        condition: "nodes.execute_tools.response_data.decision.aproved == true"
      - to: rejected
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have an error about unknown field in edge condition
	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for invalid field in edge condition")

	found := false
	for _, e := range errors {
		t.Logf("Error: %s - %s (suggestion: %s)", e.Path, e.Message, e.Suggestion)
		if strings.Contains(e.Message, "has no field 'aproved'") {
			found = true
			assert.Contains(t, e.Suggestion, "approved", "should suggest 'approved'")
		}
	}
	assert.True(t, found, "expected error about unknown field 'aproved' in edge condition")
}

// TestValidateResponseDataAccess_MultipleResponseTools tests validation with multiple
// response tools (multiple call_llm nodes feeding into different execute_tools).
func TestValidateResponseDataAccess_MultipleResponseTools(t *testing.T) {
	workflowYAML := `
name: test-multiple-response-tools
entry: [review_llm]
nodes:
  - id: review_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: code_review
        schema:
          type: object
          properties:
            score:
              type: number
            issues:
              type: array
  - id: review_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.review_llm.tool_calls}}"
  - id: summary_llm
    type: call_llm
    args:
      model: claude-4-sonnet
      response_tool:
        name: summary
        schema:
          type: object
          properties:
            title:
              type: string
            body:
              type: string
  - id: summary_tools
    type: execute_tools
    args:
      tool_calls: "{{nodes.summary_llm.tool_calls}}"
outputs:
  review_score: "{{nodes.review_tools.response_data.code_review.score}}"
  summary_title: "{{nodes.summary_tools.response_data.summary.title}}"
  # This should fail - wrong tool for this execute_tools node
  wrong_access: "{{nodes.review_tools.response_data.summary.title}}"
`

	wf, err := wfyaml.ParseWorkflow([]byte(workflowYAML))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	// Should have an error about accessing 'summary' from review_tools
	errors := result.Errors()
	require.NotEmpty(t, errors, "expected validation errors for wrong tool access")

	found := false
	for _, e := range errors {
		t.Logf("Error: %s - %s (suggestion: %s)", e.Path, e.Message, e.Suggestion)
		if strings.Contains(e.Message, "unknown response tool 'summary'") {
			found = true
			// Should suggest 'code_review' since that's what review_tools has
			assert.Contains(t, e.Suggestion, "code_review", "should suggest 'code_review'")
		}
	}
	assert.True(t, found, "expected error about accessing wrong response tool")
}
