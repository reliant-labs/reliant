package runtime

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// TestYieldEndToEnd_TemplatePreservationAndEvaluation tests the full chain:
// 1. Parse agent.yaml with templates
// 2. JSON round-trip (simulating LoadWorkflow(loadedWf.WorkflowJSON))
// 3. Evaluate yield condition with inputs.yield = true
//
// This reproduces the exact flow: one-ring → agent sub-workflow → loop executor → yield eval
func TestYieldEndToEnd_TemplatePreservationAndEvaluation(t *testing.T) {
	agentYAML := []byte(`
name: agent
apiVersion: "0.0.5"
inputs:
  yield:
    type: boolean
    default: false
  max_turns:
    type: integer
    default: 200
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "(outputs.tool_calls != null && size(outputs.tool_calls) > 0) && iter.iteration < inputs.max_turns"
    yield: "{{inputs.yield}}"
    ref: builtin://agent-turn
`)

	// Step 1: Parse workflow via v2yaml
	parsed, err := wfyaml.ParseWorkflow(agentYAML)
	require.NoError(t, err)

	yieldExpr := parsed.GetNodes()[0].GetLoop().GetYield()
	t.Logf("Node[0].Yield after parse: %q", yieldExpr)

	// Yield should be preserved as template, NOT replaced with placeholder
	assert.Equal(t, "{{inputs.yield}}", yieldExpr,
		"yield should be preserved as template string")

	// Step 2: JSON round-trip (simulates LoadWorkflow(loadedWf.WorkflowJSON))
	// Convert proto to a generic map via YAML, then JSON, to simulate the round-trip
	var rawMap map[string]interface{}
	yamlOut, err := yaml.Marshal(protoWorkflowToMap(parsed))
	require.NoError(t, err)
	err = yaml.Unmarshal(yamlOut, &rawMap)
	require.NoError(t, err)
	jsonData, err := json.Marshal(rawMap)
	require.NoError(t, err)

	wf, err := LoadWorkflow(jsonData)
	require.NoError(t, err)

	yieldAfterRoundTrip := wf.GetNodes()[0].GetLoop().GetYield()
	t.Logf("Node[0].Yield after JSON round-trip: %q", yieldAfterRoundTrip)
	assert.Equal(t, "{{inputs.yield}}", yieldAfterRoundTrip,
		"yield should survive JSON round-trip")

	// Step 3: Evaluate with bool true (what one-ring sends)
	t.Run("bool_true", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{},
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{"yield": true, "max_turns": int64(200)},
		}

		result, err := wfcel.EvaluateTemplate(yieldAfterRoundTrip, ctx)
		require.NoError(t, err)
		t.Logf("CEL result: %v (type: %T)", result, result)

		switch v := result.(type) {
		case bool:
			assert.True(t, v, "yield should evaluate to true")
		case string:
			assert.Equal(t, "true", v, "yield string should be 'true'")
		default:
			t.Fatalf("unexpected result type %T: %v", result, result)
		}
	})

	// Step 4: Evaluate with string "true" (possible type coercion scenario)
	t.Run("string_true", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{},
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{"yield": "true", "max_turns": int64(200)},
		}

		result, err := wfcel.EvaluateTemplate(yieldAfterRoundTrip, ctx)
		require.NoError(t, err)
		t.Logf("CEL result: %v (type: %T)", result, result)

		switch v := result.(type) {
		case bool:
			assert.True(t, v)
		case string:
			assert.Equal(t, "true", v)
		default:
			t.Fatalf("unexpected result type %T: %v", result, result)
		}
	})

	// Step 5: Evaluate with bool false
	t.Run("bool_false", func(t *testing.T) {
		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{},
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  map[string]interface{}{"yield": false, "max_turns": int64(200)},
		}

		result, err := wfcel.EvaluateTemplate(yieldAfterRoundTrip, ctx)
		require.NoError(t, err)
		t.Logf("CEL result: %v (type: %T)", result, result)

		switch v := result.(type) {
		case bool:
			assert.False(t, v, "yield should evaluate to false")
		case string:
			assert.Equal(t, "false", v)
		default:
			t.Fatalf("unexpected result type %T: %v", result, result)
		}
	})

	// Step 6: Full ApplyDefaults chain — simulate what InlineWorkflowExecutor does
	t.Run("with_apply_defaults", func(t *testing.T) {
		subInputs := map[string]interface{}{
			"yield": true,
		}

		defaultYield := false
		defaultMaxTurns := int64(200)
		schemas := map[string]*reliantv1.Input{
			"yield": {
				Type: "boolean",
				Config: &reliantv1.Input_BooleanInput{
					BooleanInput: &reliantv1.BooleanInputConfig{Default: &defaultYield},
				},
			},
			"max_turns": {
				Type: "integer",
				Config: &reliantv1.Input_IntegerInput{
					IntegerInput: &reliantv1.IntegerInputConfig{Default: &defaultMaxTurns},
				},
			},
		}

		result := ApplyDefaults(subInputs, schemas)
		t.Logf("After ApplyDefaults: yield=%v (type: %T)", result["yield"], result["yield"])

		assert.Equal(t, true, result["yield"], "ApplyDefaults should preserve yield=true")

		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{},
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  result,
		}

		celResult, err := wfcel.EvaluateTemplate("{{inputs.yield}}", ctx)
		require.NoError(t, err)
		t.Logf("CEL result after ApplyDefaults: %v (type: %T)", celResult, celResult)

		switch v := celResult.(type) {
		case bool:
			assert.True(t, v)
		default:
			t.Fatalf("expected bool, got %T: %v", celResult, celResult)
		}
	})

	// Step 7: ResolveAndParseWorkflow chain
	t.Run("resolve_and_parse", func(t *testing.T) {
		inputs := map[string]interface{}{
			"yield":     true,
			"max_turns": int64(200),
		}

		resolved, err := ResolveAndParseWorkflow(agentYAML, inputs)
		require.NoError(t, err)

		resolvedYield := resolved.GetNodes()[0].GetLoop().GetYield()
		t.Logf("Resolved Node[0].Yield: %q", resolvedYield)
		assert.Equal(t, "{{inputs.yield}}", resolvedYield,
			"yield should remain as template even after ResolveAndParseWorkflow")
	})

	// Step 8: Simulate the EXACT evaluateYieldCondition flow with fmt.Sprintf
	t.Run("exact_coercion_flow", func(t *testing.T) {
		workflowInputs := map[string]interface{}{
			"yield":     true,
			"max_turns": int64(200),
		}

		yieldExpr := "{{inputs.yield}}"

		ctx := &wfcel.LoopEvalContext{
			Outputs: map[string]interface{}{},
			Iter:    &model.IterContext{Iteration: 0},
			Inputs:  workflowInputs,
		}

		result, err := wfcel.EvaluateTemplate(yieldExpr, ctx)
		require.NoError(t, err)

		t.Logf("inputsYieldValue: %v", fmt.Sprintf("%v", workflowInputs["yield"]))
		t.Logf("inputsYieldType: %T", workflowInputs["yield"])
		t.Logf("celResult: %v", fmt.Sprintf("%v", result))
		t.Logf("celResultType: %T", result)

		var shouldYield bool
		switch v := result.(type) {
		case bool:
			shouldYield = v
		case string:
			shouldYield = v == "true"
		default:
			t.Fatalf("unexpected type %T: %v", result, result)
		}

		assert.True(t, shouldYield, "shouldYield must be true")
	})
}

// protoWorkflowToMap converts a proto V2Workflow to a generic map for JSON round-trip testing.
func protoWorkflowToMap(wf *reliantv1.Workflow) map[string]interface{} {
	result := map[string]interface{}{
		"name": wf.GetName(),
	}
	if wf.GetApiVersion() != "" {
		result["apiVersion"] = wf.GetApiVersion()
	}
	if len(wf.GetEntry()) > 0 {
		result["entry"] = wf.GetEntry()
	}

	var nodes []map[string]interface{}
	for _, n := range wf.GetNodes() {
		nodeMap := map[string]interface{}{
			"id":   n.GetId(),
			"type": n.GetType(),
		}
		if n.GetLoop() != nil {
			if w := n.GetLoop().GetWhile(); w != nil && w.GetExpr() != "" {
				nodeMap["while"] = w.GetExpr()
			}
			if n.GetLoop().GetYield() != "" {
				nodeMap["yield"] = n.GetLoop().GetYield()
			}
			if r := n.GetLoop().GetRef(); r != nil {
				if lit := r.GetLiteral(); lit != "" {
					nodeMap["ref"] = lit
				} else if expr := r.GetExpr(); expr != "" {
					nodeMap["ref"] = expr
				}
			}
		}
		nodes = append(nodes, nodeMap)
	}
	result["nodes"] = nodes

	var edges []map[string]interface{}
	for _, e := range wf.GetEdges() {
		edgeMap := map[string]interface{}{
			"from": e.GetFrom(),
		}
		if len(e.GetDefault()) > 0 {
			if len(e.GetDefault()) == 1 {
				edgeMap["default"] = e.GetDefault()[0]
			} else {
				edgeMap["default"] = e.GetDefault()
			}
		}
		edges = append(edges, edgeMap)
	}
	if len(edges) > 0 {
		result["edges"] = edges
	}

	return result
}
