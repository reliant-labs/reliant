// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// ============================================================================
// YIELD CONDITION EVALUATION TESTS
// ============================================================================
//
// These tests verify the yield condition CEL evaluation logic using the workflow CEL
// typed API (wfcel.EvaluateTemplate with wfcel.LoopEvalContext).
//
// ============================================================================

// TestYieldCondition_EmptyString verifies that an empty yield expression
// evaluates to empty string (no yield).
func TestYieldCondition_EmptyString(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 0},
		Inputs:  map[string]interface{}{},
	}

	result, err := wfcel.EvaluateTemplate("", ctx)
	require.NoError(t, err)
	assert.Equal(t, "", result, "empty string should return empty string")
}

// TestYieldCondition_LiteralTrue verifies that yield: "true" evaluates to the string "true".
func TestYieldCondition_LiteralTrue(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 0},
		Inputs:  map[string]interface{}{},
	}

	result, err := wfcel.EvaluateTemplate("true", ctx)
	require.NoError(t, err)
	// "true" without {{}} is returned as-is (a string literal)
	assert.Equal(t, "true", result, "literal 'true' should be returned as string")
}

// TestYieldCondition_LiteralFalse verifies that yield: "false" evaluates to the string "false".
func TestYieldCondition_LiteralFalse(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 0},
		Inputs:  map[string]interface{}{},
	}

	result, err := wfcel.EvaluateTemplate("false", ctx)
	require.NoError(t, err)
	assert.Equal(t, "false", result, "literal 'false' should be returned as string")
}

// TestYieldCondition_TemplateInputsYieldTrue verifies that "{{inputs.yield}}"
// with inputs.yield=true evaluates to boolean true.
func TestYieldCondition_TemplateInputsYieldTrue(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 0},
		Inputs:  map[string]interface{}{"yield": true},
	}

	result, err := wfcel.EvaluateTemplate("{{inputs.yield}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, result, "{{inputs.yield}} with yield=true should return bool true")
}

// TestYieldCondition_TemplateInputsYieldFalse verifies that "{{inputs.yield}}"
// with inputs.yield=false evaluates to boolean false.
func TestYieldCondition_TemplateInputsYieldFalse(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 0},
		Inputs:  map[string]interface{}{"yield": false},
	}

	result, err := wfcel.EvaluateTemplate("{{inputs.yield}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, result, "{{inputs.yield}} with yield=false should return bool false")
}

// TestYieldCondition_CELExpressionFalse verifies that a CEL expression evaluating
// to false works correctly.
func TestYieldCondition_CELExpressionFalse(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 5},
		Inputs:  map[string]interface{}{"yield": false},
	}

	result, err := wfcel.EvaluateTemplate("{{inputs.yield == true}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, result, "CEL expression with yield=false should return false")
}

// TestYieldCondition_CELExpressionTrue verifies that a CEL expression evaluating
// to true works correctly.
func TestYieldCondition_CELExpressionTrue(t *testing.T) {
	ctx := &wfcel.LoopEvalContext{
		Outputs: map[string]interface{}{},
		Iter:    &model.IterContext{Iteration: 5},
		Inputs:  map[string]interface{}{"yield": true},
	}

	result, err := wfcel.EvaluateTemplate("{{inputs.yield == true}}", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, result, "CEL expression with yield=true should return true")
}

// ============================================================================
// SCHEMA TESTS — YIELD FIELD ON LOOP NODES (proto)
// ============================================================================

// TestYieldField_LoopNodeWithYield verifies the proto V2Node with loop args parses yield correctly.
func TestYieldField_LoopNodeWithYield(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "agent_loop",
		Type: model.NodeTypeLoop,
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Yield: "{{inputs.yield}}",
		}},
	}

	assert.Equal(t, "{{inputs.yield}}", node.GetLoop().GetYield())
	assert.Equal(t, model.NodeTypeLoop, node.GetType())
}

// TestYieldField_LoopNodeWithoutYield verifies loop nodes without yield have empty Yield field.
func TestYieldField_LoopNodeWithoutYield(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "retry_loop",
		Type: model.NodeTypeLoop,
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{}},
	}

	assert.Equal(t, "", node.GetLoop().GetYield())
}

// TestYieldField_ProtoJSONRoundTrip verifies that the Yield field survives proto JSON marshal/unmarshal.
func TestYieldField_ProtoJSONRoundTrip(t *testing.T) {
	original := &reliantv1.Node{
		Id:   "agent_loop",
		Type: model.NodeTypeLoop,
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{
			Yield: "{{inputs.yield}}",
		}},
	}

	data, err := protojson.Marshal(original)
	require.NoError(t, err)

	roundTripped := &reliantv1.Node{}
	err = protojson.Unmarshal(data, roundTripped)
	require.NoError(t, err)

	assert.Equal(t, original.GetId(), roundTripped.GetId())
	assert.Equal(t, original.GetType(), roundTripped.GetType())
	assert.Equal(t, original.GetLoop().GetYield(), roundTripped.GetLoop().GetYield())
}

// TestYieldField_ProtoJSONOmitEmpty verifies that Yield is omitted from proto JSON when empty.
func TestYieldField_ProtoJSONOmitEmpty(t *testing.T) {
	node := &reliantv1.Node{
		Id:   "retry_loop",
		Type: model.NodeTypeLoop,
		Args: &reliantv1.Node_Loop{Loop: &reliantv1.LoopArgs{}},
	}

	data, err := protojson.Marshal(node)
	require.NoError(t, err)

	// Yield should be omitted from the JSON output when empty
	assert.NotContains(t, string(data), `"yield"`)
}

// TestYieldField_ParseFromYAML verifies that a YAML workflow with yield on a loop
// node parses correctly through the workflow parser.
func TestYieldField_ParseFromYAML(t *testing.T) {
	t.Run("literal yield value", func(t *testing.T) {
		yamlData := `
name: test-yield-workflow
nodes:
  - id: agent_loop
    type: loop
    yield: "true"
    while: "outputs.tool_calls != null && iter.iteration < 50"
    ref: builtin://agent
edges:
  - from: started
    default: agent_loop
`
		wf, err := wfyaml.ParseWorkflow([]byte(yamlData))
		require.NoError(t, err)
		require.Len(t, wf.GetNodes(), 1)
		assert.Equal(t, "true", wf.GetNodes()[0].GetLoop().GetYield())
		assert.Equal(t, "loop", wf.GetNodes()[0].GetType())
	})

	t.Run("template yield is preserved during parse", func(t *testing.T) {
		yamlData := `
name: test-yield-workflow
nodes:
  - id: agent_loop
    type: loop
    yield: "{{inputs.yield}}"
    while: "outputs.tool_calls != null && iter.iteration < 50"
    ref: builtin://agent
edges:
  - from: started
    default: agent_loop
`
		tw, err := wfyaml.ParseWorkflow([]byte(yamlData))
		require.NoError(t, err)
		require.Len(t, tw.GetNodes(), 1)
		assert.Equal(t, "loop", tw.GetNodes()[0].GetType())
		assert.Equal(t, "{{inputs.yield}}", tw.GetNodes()[0].GetLoop().GetYield(), "yield template should be preserved")
	})
}

// TestYieldField_ParseFromYAMLWithoutYield verifies backward compatibility for
// YAML workflows that don't have a yield field on their loop nodes.
func TestYieldField_ParseFromYAMLWithoutYield(t *testing.T) {
	yamlData := `
name: test-no-yield-workflow
nodes:
  - id: retry_loop
    type: loop
    while: "iter.iteration < 3"
    ref: some-workflow
edges:
  - from: started
    default: retry_loop
`
	wf, err := wfyaml.ParseWorkflow([]byte(yamlData))
	require.NoError(t, err)
	require.Len(t, wf.GetNodes(), 1)
	assert.Equal(t, "", wf.GetNodes()[0].GetLoop().GetYield(), "Yield should be empty when not specified")
}
