// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// BuildResponseToolContext Tests (via YAML + validation)
// =============================================================================

func TestBuildResponseToolContext_SimpleNode(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm, execute]
nodes:
  - id: call_llm
    type: call_llm
    model: {tags: [flagship]}
    response_tool:
      name: review
      schema:
        type: object
        properties:
          score:
            type: integer
            description: Review score
          comment:
            type: string
            description: Review comment
  - id: execute
    type: execute_tools
    tool_calls: "{{nodes.call_llm.tool_calls}}"
edges:
  - from: call_llm
    to: execute
outputs:
  # Valid response_data access
  score: "{{nodes.execute.response_data.review.score}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	// Should have no errors for valid access
	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no validation errors for valid response tool access")
}

func TestBuildResponseToolContext_MultipleTools(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm_1]
nodes:
  - id: call_llm_1
    type: call_llm
    args:
      model: {tags: [flagship]}
      response_tool:
        name: review
        schema:
          type: object
          properties:
            score:
              type: integer
  - id: execute_1
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm_1.tool_calls}}"
  - id: call_llm_2
    type: call_llm
    args:
      model: {tags: [flagship]}
      response_tool:
        name: analyze
        schema:
          type: object
          properties:
            result:
              type: string
  - id: execute_2
    type: execute_tools
    args:
      tool_calls: "{{nodes.call_llm_2.tool_calls}}"
edges:
  - from: call_llm_1
    to: execute_1
  - from: execute_1
    to: call_llm_2
  - from: call_llm_2
    to: execute_2
outputs:
  review_score: "{{nodes.execute_1.response_data.review.score}}"
  analyze_result: "{{nodes.execute_2.response_data.analyze.result}}"
  # Cross-access should fail
  wrong_access: "{{nodes.execute_1.response_data.analyze.result}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)
	// Should have an error about wrong tool access
	errors := result.Errors()
	found := false
	for _, e := range errors {
		if strings.Contains(e.Message, "unknown response tool 'analyze'") {
			found = true
		}
	}
	assert.True(t, found, "expected error about accessing wrong response tool from execute_1")
}

func TestBuildResponseToolContext_DynamicSource(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm, execute]
nodes:
  - id: call_llm
    type: call_llm
    model: {tags: [flagship]}
    response_tool:
      name: review
      schema:
        type: object
        properties:
          score:
            type: integer
  - id: execute
    type: execute_tools
    # Complex expression - dynamic source
    tool_calls: "{{nodes.call_llm.tool_calls + nodes.other.tool_calls}}"
edges:
  - from: call_llm
    to: execute
outputs:
  result: "{{nodes.execute.response_data.review.score}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	// Dynamic source - should be lenient (no errors about response tools)
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "unknown response tool") {
			t.Errorf("unexpected response tool error for dynamic source: %s", e.Message)
		}
	}
}

func TestBuildResponseToolContext_NoResponseTool(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm, execute]
nodes:
  - id: call_llm
    type: call_llm
    model: {tags: [flagship]}
    # No response_tool defined
  - id: execute
    type: execute_tools
    tool_calls: "{{nodes.call_llm.tool_calls}}"
edges:
  - from: call_llm
    to: execute
outputs:
  result: "{{nodes.execute.response_data.some_tool.field}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	// No response tool = lenient
	for _, e := range result.Errors() {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, result.Errors(), "expected no errors when no response tool defined")
}

func TestBuildResponseToolContext_TemplateToolName(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm, execute]
nodes:
  - id: call_llm
    type: call_llm
    model: {tags: [flagship]}
    response_tool:
      name: "{{inputs.tool_name}}"
      schema:
        type: object
        properties:
          result:
            type: string
  - id: execute
    type: execute_tools
    tool_calls: "{{nodes.call_llm.tool_calls}}"
edges:
  - from: call_llm
    to: execute
outputs:
  result: "{{nodes.execute.response_data.some_tool.field}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	// Dynamic tool name - should be lenient
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "unknown response tool") {
			t.Errorf("unexpected response tool error for dynamic tool name: %s", e.Message)
		}
	}
}

// =============================================================================
// ResolveToolCallsSource Tests
// Note: These test the local resolveToolCallsSource logic via response_data validation
// =============================================================================

func TestResolveToolCallsSource_SimplePattern(t *testing.T) {
	// Test that simple node.X.tool_calls patterns are correctly resolved
	// by checking that validation correctly identifies the source node
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [call_llm, execute]
nodes:
  - id: call_llm
    type: call_llm
    model: {tags: [flagship]}
    response_tool:
      name: review
      schema:
        type: object
        properties:
          score:
            type: integer
  - id: execute
    type: execute_tools
    tool_calls: "{{nodes.call_llm.tool_calls}}"
edges:
  - from: call_llm
    to: execute
outputs:
  # Valid: review is the tool from call_llm
  score: "{{nodes.execute.response_data.review.score}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	assert.Empty(t, result.Errors(), "simple tool_calls pattern should resolve correctly")
}

func TestResolveToolCallsSource_ComplexExpression(t *testing.T) {
	// Complex expressions should be treated as dynamic source
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [llm_a, execute]
nodes:
  - id: llm_a
    type: call_llm
    model: {tags: [flagship]}
    response_tool:
      name: tool_a
      schema:
        type: object
        properties:
          field:
            type: string
  - id: execute
    type: execute_tools
    # Concatenation = complex expression = dynamic source
    tool_calls: "{{nodes.llm_a.tool_calls + nodes.llm_a.tool_calls}}"
edges:
  - from: llm_a
    to: execute
outputs:
  result: "{{nodes.execute.response_data.tool_a.field}}"
`))
	require.NoError(t, err)

	result := &Result{}
	validateCEL(wf, result)

	// Dynamic source should be lenient
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "unknown response tool") {
			t.Errorf("unexpected response tool error for complex expression: %s", e.Message)
		}
	}
}

// =============================================================================
// JSONSchema Conversion Tests (via validation of object inputs)
// =============================================================================

func TestJSONSchemaToFieldInfo(t *testing.T) {
	// Test that JSON Schema properties are correctly converted and used in validation
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [step1]
inputs:
  data:
    type: object
    properties:
      name:
        type: string
        description: The name
      age:
        type: integer
      score:
        type: number
      active:
        type: boolean
      tags:
        type: array
      metadata:
        type: object
nodes:
  - id: step1
    type: call_llm
    model: {tags: [flagship]}
    # Access various properties to verify they're recognized
    condition: "size(inputs.data.name) > 0 && inputs.data.active == true"
`))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no errors for valid object property access")
}

func TestJSONSchemaToFieldInfo_EmptySchema(t *testing.T) {
	// Object with no properties should allow dynamic access
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: test
entry: [step1]
inputs:
  data:
    type: object
nodes:
  - id: step1
    type: call_llm
    model: {tags: [flagship]}
    condition: "has(inputs.data.anything)"
`))
	require.NoError(t, err)

	result := &Result{}
	ValidateCELWithCompilation(wf, result, nil)

	errors := result.Errors()
	for _, e := range errors {
		t.Logf("Error: %s - %s", e.Path, e.Message)
	}
	assert.Empty(t, errors, "expected no errors for schema-less object access")
}
