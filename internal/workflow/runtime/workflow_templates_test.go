// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTemplateResolution tests various template expression patterns
// These tests define the expected behavior for template resolution
func TestTemplateResolution(t *testing.T) {
	tests := []struct {
		name     string
		template string
		inputs   map[string]interface{}
		want     interface{}
		wantErr  bool
	}{
		// === Simple variable substitution ===
		{
			name:     "simple int",
			template: "{{inputs.max_turns}}",
			inputs:   map[string]interface{}{"max_turns": 100},
			want:     100, // Should return int, not string
		},
		{
			name:     "simple float",
			template: "{{inputs.temperature}}",
			inputs:   map[string]interface{}{"temperature": 0.7},
			want:     0.7,
		},
		{
			name:     "simple bool",
			template: "{{inputs.auto_approve}}",
			inputs:   map[string]interface{}{"auto_approve": true},
			want:     true,
		},
		{
			name:     "simple string",
			template: "{{inputs.model}}",
			inputs:   map[string]interface{}{"model": "claude-sonnet"},
			want:     "claude-sonnet",
		},

		// === Arithmetic expressions ===
		{
			name:     "division",
			template: "{{inputs.max_turns / 10}}",
			inputs:   map[string]interface{}{"max_turns": 100},
			want:     10, // 100 / 10 = 10
		},
		{
			name:     "multiplication",
			template: "{{inputs.base * 2}}",
			inputs:   map[string]interface{}{"base": 50},
			want:     100,
		},
		{
			name:     "addition",
			template: "{{inputs.a + inputs.b}}",
			inputs:   map[string]interface{}{"a": 30, "b": 70},
			want:     100,
		},
		{
			name:     "complex arithmetic",
			template: "{{(inputs.max_turns / 2) + 10}}",
			inputs:   map[string]interface{}{"max_turns": 80},
			want:     50, // (80 / 2) + 10 = 50
		},

		// === Conditional expressions ===
		{
			name:     "ternary true branch",
			template: "{{inputs.count > 0 ? inputs.count : 100}}",
			inputs:   map[string]interface{}{"count": 50},
			want:     50,
		},
		{
			name:     "ternary false branch",
			template: "{{inputs.count > 0 ? inputs.count : 100}}",
			inputs:   map[string]interface{}{"count": 0},
			want:     100,
		},
		{
			name:     "ternary with default",
			template: "{{has(inputs.custom) ? inputs.custom : 200}}",
			inputs:   map[string]interface{}{}, // custom not provided
			want:     200,
		},
		{
			name:     "ternary with provided value",
			template: "{{has(inputs.custom) ? inputs.custom : 200}}",
			inputs:   map[string]interface{}{"custom": 50},
			want:     50,
		},

		// === String operations ===
		{
			name:     "string concatenation",
			template: "{{inputs.prefix + '-' + inputs.suffix}}",
			inputs:   map[string]interface{}{"prefix": "agent", "suffix": "v1"},
			want:     "agent-v1",
		},

		// === Mixed content (interpolation) ===
		{
			name:     "string with embedded expression",
			template: "Model: {{inputs.model}}",
			inputs:   map[string]interface{}{"model": "claude-sonnet"},
			want:     "Model: claude-sonnet", // Returns string because mixed content
		},
		{
			name:     "multiple expressions in string",
			template: "{{inputs.model}} at temp {{inputs.temp}}",
			inputs:   map[string]interface{}{"model": "claude", "temp": 0.7},
			want:     "claude at temp 0.7",
		},

		// === Array interpolation (for spawn syntax) ===
		{
			name:     "array interpolation joins with commas",
			template: "spawn:workflow({{inputs.presets}})",
			inputs:   map[string]interface{}{"presets": []interface{}{"general", "researcher"}},
			want:     "spawn:workflow(general,researcher)",
		},
		{
			name:     "array interpolation single element",
			template: "spawn:workflow({{inputs.presets}})",
			inputs:   map[string]interface{}{"presets": []interface{}{"only"}},
			want:     "spawn:workflow(only)",
		},
		{
			name:     "array interpolation empty",
			template: "spawn:workflow({{inputs.presets}})",
			inputs:   map[string]interface{}{"presets": []interface{}{}},
			want:     "spawn:workflow()",
		},
		{
			name:     "array interpolation with static prefix",
			template: "spawn:workflow(static,{{inputs.presets}})",
			inputs:   map[string]interface{}{"presets": []interface{}{"dynamic1", "dynamic2"}},
			want:     "spawn:workflow(static,dynamic1,dynamic2)",
		},

		// === Whitespace handling ===
		{
			name:     "whitespace inside braces",
			template: "{{ inputs.max_turns }}",
			inputs:   map[string]interface{}{"max_turns": 100},
			want:     100,
		},
		{
			name:     "no whitespace",
			template: "{{inputs.max_turns}}",
			inputs:   map[string]interface{}{"max_turns": 100},
			want:     100,
		},

		// === No template (passthrough) ===
		{
			name:     "plain string no template",
			template: "just a string",
			inputs:   map[string]interface{}{},
			want:     "just a string",
		},
		{
			name:     "empty string",
			template: "",
			inputs:   map[string]interface{}{},
			want:     "",
		},

		// === Error cases ===
		{
			name:     "missing input key",
			template: "{{inputs.nonexistent}}",
			inputs:   map[string]interface{}{},
			wantErr:  true,
		},
		{
			name:     "invalid CEL syntax",
			template: "{{inputs.foo +}}",
			inputs:   map[string]interface{}{"foo": 1},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build context in the expected format
			// Use inputs.X directly - NOT workflow.inputs.X
			// See cel_env.go for namespace documentation
			context := map[string]interface{}{
				"inputs": tt.inputs,
			}

			got, err := resolveTemplateString(tt.template, context)

			if tt.wantErr {
				assert.Error(t, err, "expected error for template: %s", tt.template)
				return
			}

			require.NoError(t, err, "unexpected error for template: %s", tt.template)

			// For numeric comparisons, handle type flexibility (int vs int64 vs float64)
			switch want := tt.want.(type) {
			case int:
				switch g := got.(type) {
				case int:
					assert.Equal(t, want, g)
				case int64:
					assert.Equal(t, int64(want), g)
				case float64:
					assert.Equal(t, float64(want), g)
				default:
					t.Errorf("expected int-like, got %T: %v", got, got)
				}
			case float64:
				switch g := got.(type) {
				case float64:
					assert.InDelta(t, want, g, 0.001)
				case int64:
					assert.InDelta(t, want, float64(g), 0.001)
				default:
					t.Errorf("expected float-like, got %T: %v", got, got)
				}
			default:
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestResolveWorkflowMap tests resolving templates in a full workflow map structure
func TestResolveWorkflowMap(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]interface{}
		inputs  map[string]interface{}
		check   func(t *testing.T, resolved map[string]interface{})
		wantErr bool
	}{
		{
			name: "loop while from input",
			raw: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "agent_loop",
						"type":  "loop",
						"while": "iter.iteration >= {{inputs.max_turns}}",
						"ref":   "child",
					},
				},
			},
			inputs: map[string]interface{}{"max_turns": 100},
			check: func(t *testing.T, resolved map[string]interface{}) {
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})
				assert.Equal(t, "iter.iteration >= 100", node["while"])
			},
		},
		{
			name: "loop while with arithmetic",
			raw: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "loop",
						"type":  "loop",
						"while": "iter.iteration >= {{inputs.max_turns / 2}}",
						"ref":   "child",
					},
				},
			},
			inputs: map[string]interface{}{"max_turns": 200},
			check: func(t *testing.T, resolved map[string]interface{}) {
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})
				assert.Equal(t, "iter.iteration >= 100", node["while"])
			},
		},
		{
			// Test that node-level fields (like timeout) are resolved,
			// but node.args are preserved as templates for runtime evaluation
			name: "node fields resolved but args preserved",
			raw: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":      "step",
						"timeout": "{{inputs.timeout}}",
						"args": map[string]interface{}{
							"model":       "{{inputs.model}}",
							"temperature": "{{inputs.temp}}",
						},
					},
				},
			},
			inputs: map[string]interface{}{
				"timeout": "5m",
				"model":   "claude-sonnet",
				"temp":    0.7,
			},
			check: func(t *testing.T, resolved map[string]interface{}) {
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})
				// Node-level field (timeout) should be resolved
				assert.Equal(t, "5m", node["timeout"])
				// But node.args should remain as templates for runtime evaluation
				args := node["args"].(map[string]interface{})
				assert.Equal(t, "{{inputs.model}}", args["model"],
					"node args should remain as templates")
				assert.Equal(t, "{{inputs.temp}}", args["temperature"],
					"node args should remain as templates")
			},
		},
		{
			name: "preserves non-template values",
			raw: map[string]interface{}{
				"name":    "test-workflow",
				"version": "v1.0.0",
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "step1",
						"type":  "loop",
						"while": "iter.iteration >= 10",
						"ref":   "child",
					},
				},
			},
			inputs: map[string]interface{}{},
			check: func(t *testing.T, resolved map[string]interface{}) {
				assert.Equal(t, "test-workflow", resolved["name"])
				assert.Equal(t, "v1.0.0", resolved["version"])
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})
				assert.Equal(t, "iter.iteration >= 10", node["while"])
			},
		},
		{
			name: "conditional default in while",
			raw: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "loop",
						"type":  "loop",
						"while": "iter.iteration >= {{has(inputs.max) ? inputs.max : 100}}",
						"ref":   "child",
					},
				},
			},
			inputs: map[string]interface{}{}, // max not provided
			check: func(t *testing.T, resolved map[string]interface{}) {
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})
				assert.Equal(t, "iter.iteration >= 100", node["while"])
			},
		},
		{
			// Test that inline loop outputs are NOT resolved at load time
			// They should remain as template strings to be evaluated at runtime
			name: "inline loop outputs preserved",
			raw: map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "agent_loop",
						"type":  "loop",
						"while": "iter.iteration >= {{inputs.max_turns}}",
						"inline": map[string]interface{}{
							"nodes": []interface{}{
								map[string]interface{}{
									"id":   "call_llm",
									"type": "call_llm",
								},
							},
							"outputs": map[string]interface{}{
								// These reference nodes.* which is only available at runtime
								"tool_calls":    "{{nodes.call_llm.tool_calls}}",
								"response_text": "{{nodes.call_llm.response_text}}",
							},
						},
					},
				},
			},
			inputs: map[string]interface{}{"max_turns": 50},
			check: func(t *testing.T, resolved map[string]interface{}) {
				nodes := resolved["nodes"].([]interface{})
				node := nodes[0].(map[string]interface{})

				// Loop while should be resolved
				assert.Equal(t, "iter.iteration >= 50", node["while"])

				// But inline.outputs should remain as templates (not resolved)
				inline := node["inline"].(map[string]interface{})
				outputs := inline["outputs"].(map[string]interface{})
				assert.Equal(t, "{{nodes.call_llm.tool_calls}}", outputs["tool_calls"],
					"inline loop outputs should remain as templates")
				assert.Equal(t, "{{nodes.call_llm.response_text}}", outputs["response_text"],
					"inline loop outputs should remain as templates")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := ResolveWorkflowTemplates(tt.raw, tt.inputs)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			tt.check(t, resolved)
		})
	}
}

// TestEndToEndWithTypedStruct tests the full flow: raw map -> resolve -> proto
func TestEndToEndWithTypedStruct(t *testing.T) {
	tests := []struct {
		name   string
		raw    map[string]interface{}
		inputs map[string]interface{}
		check  func(t *testing.T, wf *reliantv1.Workflow)
	}{
		{
			name: "loop while with templated iteration limit",
			raw: map[string]interface{}{
				"name":  "test",
				"entry": []interface{}{"loop1"},
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "loop1",
						"type":  "loop",
						"while": "outputs.done || iter.iteration >= {{inputs.max_turns}}",
						"ref":   "child",
					},
				},
			},
			inputs: map[string]interface{}{"max_turns": 50},
			check: func(t *testing.T, wf *reliantv1.Workflow) {
				require.Len(t, wf.Nodes, 1)
				assert.Equal(t, "loop", wf.Nodes[0].GetType())
				loop := wf.Nodes[0].GetLoop()
				require.NotNil(t, loop)
				assert.Equal(t, "outputs.done || iter.iteration >= 50", model.DirectCelExpr(loop.GetWhile()))
			},
		},
		{
			name: "arithmetic in loop while",
			raw: map[string]interface{}{
				"name":  "test",
				"entry": []interface{}{"loop1"},
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "loop1",
						"type":  "loop",
						"while": "iter.iteration >= {{inputs.total / inputs.batch_size}}",
						"ref":   "process-batch",
					},
				},
			},
			inputs: map[string]interface{}{"total": 1000, "batch_size": 10},
			check: func(t *testing.T, wf *reliantv1.Workflow) {
				loop := wf.Nodes[0].GetLoop()
				require.NotNil(t, loop)
				assert.Equal(t, "iter.iteration >= 100", model.DirectCelExpr(loop.GetWhile())) // 1000 / 10 = 100
			},
		},
		{
			name: "inline loop with outputs preserved",
			raw: map[string]interface{}{
				"name":  "test",
				"entry": []interface{}{"agent_loop"},
				"nodes": []interface{}{
					map[string]interface{}{
						"id":    "agent_loop",
						"type":  "loop",
						"while": "outputs.done == true || iter.iteration >= {{inputs.max_turns}}",
						"inline": map[string]interface{}{
							"nodes": []interface{}{
								map[string]interface{}{
									"id":   "call_llm",
									"type": "call_llm",
								},
							},
							"outputs": map[string]interface{}{
								"tool_calls":    "{{nodes.call_llm.tool_calls}}",
								"response_text": "{{nodes.call_llm.response_text}}",
								"done":          "{{nodes.call_llm.tool_calls == null}}",
							},
						},
					},
				},
			},
			inputs: map[string]interface{}{"max_turns": 25},
			check: func(t *testing.T, wf *reliantv1.Workflow) {
				require.Len(t, wf.Nodes, 1)
				assert.Equal(t, "loop", wf.Nodes[0].GetType())

				loop := wf.Nodes[0].GetLoop()
				require.NotNil(t, loop)

				// Loop while should be resolved with max_turns interpolated
				assert.Equal(t, "outputs.done == true || iter.iteration >= 25", model.DirectCelExpr(loop.GetWhile()))

				// Inline body should exist
				inline := loop.GetInline()
				require.NotNil(t, inline)

				// Outputs should be preserved as template strings
				outputs := inline.GetOutputs()
				assert.Equal(t, "{{nodes.call_llm.tool_calls}}", outputs["tool_calls"],
					"inline outputs should remain as templates")
				assert.Equal(t, "{{nodes.call_llm.response_text}}", outputs["response_text"],
					"inline outputs should remain as templates")
				assert.Equal(t, "{{nodes.call_llm.tool_calls == null}}", outputs["done"],
					"inline outputs should remain as templates")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Resolve templates
			resolved, err := ResolveWorkflowTemplates(tt.raw, tt.inputs)
			require.NoError(t, err)

			// Step 2: Parse to proto
			wf, err := parseResolvedWorkflow(resolved)
			require.NoError(t, err)

			// Step 3: Check the result
			tt.check(t, wf)
		})
	}
}

// TestTemplateResolutionAndParsing tests template-aware parsing for validation
func TestTemplateResolutionAndParsing(t *testing.T) {
	// YAML with template in loop while
	yaml := []byte(`
name: test-workflow
apiVersion: "0.0.6"

inputs:
  max_turns:
    type: integer
    default: 50

entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "iter.iteration >= {{inputs.max_turns}}"
    ref: builtin://agent-turn
`)

	t.Run("resolve and parse with inputs", func(t *testing.T) {
		inputs := map[string]interface{}{"max_turns": 75}

		// Should resolve and parse with actual value
		wf, err := ResolveAndParseWorkflow(yaml, inputs)
		require.NoError(t, err)
		assert.Equal(t, "loop", wf.Nodes[0].GetType())
		loop := wf.Nodes[0].GetLoop()
		require.NotNil(t, loop)
		assert.Equal(t, "iter.iteration >= 75", model.DirectCelExpr(loop.GetWhile())) // Actual resolved value!
	})

	t.Run("resolve with arithmetic expression", func(t *testing.T) {
		yamlWithArithmetic := []byte(`
name: test-workflow
apiVersion: "0.0.6"

inputs:
  total_items:
    type: integer
  batch_size:
    type: integer
    default: 10

entry: [batch_loop]
nodes:
  - id: batch_loop
    type: loop
    while: "iter.iteration >= {{inputs.total_items / inputs.batch_size}}"
    ref: process-batch
`)
		inputs := map[string]interface{}{"total_items": 500, "batch_size": 25}

		wf, err := ResolveAndParseWorkflow(yamlWithArithmetic, inputs)
		require.NoError(t, err)
		assert.Equal(t, "loop", wf.Nodes[0].GetType())
		loop := wf.Nodes[0].GetLoop()
		require.NotNil(t, loop)
		assert.Equal(t, "iter.iteration >= 20", model.DirectCelExpr(loop.GetWhile())) // 500 / 25 = 20
	})
}

// TestRuntimeTemplateResolutionFlow tests the full runtime flow as it happens in DynamicWorkflow
func TestRuntimeTemplateResolutionFlow(t *testing.T) {
	yaml := []byte(`
name: agent
apiVersion: "0.0.6"

inputs:
  max_turns:
    type: integer
    default: 100
    description: Maximum agent loop iterations

entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "outputs.tool_calls == null || iter.iteration >= {{inputs.max_turns}}"
    ref: builtin://agent-turn
`)

	t.Run("full flow with default", func(t *testing.T) {
		// Provide the default value directly (default from schema is max_turns: 100)
		inputs := map[string]interface{}{"max_turns": 100}

		// Resolve templates with inputs
		resolved, err := ResolveAndParseWorkflow(yaml, inputs)
		require.NoError(t, err)
		assert.Equal(t, "loop", resolved.Nodes[0].GetType())
		loop1 := resolved.Nodes[0].GetLoop()
		require.NotNil(t, loop1)
		assert.Equal(t, "outputs.tool_calls == null || iter.iteration >= 100", model.DirectCelExpr(loop1.GetWhile()))
	})

	t.Run("full flow with custom value", func(t *testing.T) {
		inputs := map[string]interface{}{"max_turns": 25}

		// Resolve templates
		wf, err := ResolveAndParseWorkflow(yaml, inputs)
		require.NoError(t, err)
		assert.Equal(t, "loop", wf.Nodes[0].GetType())
		loop2 := wf.Nodes[0].GetLoop()
		require.NotNil(t, loop2)
		assert.Equal(t, "outputs.tool_calls == null || iter.iteration >= 25", model.DirectCelExpr(loop2.GetWhile()))
	})
}

// TestOutputsNotResolvedAtLoadTime verifies that the outputs section is NOT resolved
// during template resolution. This is critical because outputs typically reference
// step outputs (e.g., {{nodes.agent_loop.message}}) which don't exist until workflow
// completion. Output templates should be preserved and only evaluated later via
// EvaluateWorkflowOutputs().
func TestOutputsNotResolvedAtLoadTime(t *testing.T) {
	t.Run("outputs remain as templates", func(t *testing.T) {
		// This mimics the structure of agent.yaml which caused the bug
		raw := map[string]interface{}{
			"name": "test-workflow",
			"nodes": []interface{}{
				map[string]interface{}{
					"id":    "agent_loop",
					"type":  "loop",
					"while": "iter.iteration >= {{inputs.max_turns}}",
					"ref":   "builtin://agent-turn",
				},
			},
			// Output templates reference step outputs that don't exist at load time
			"outputs": map[string]interface{}{
				"message":       "{{nodes.agent_loop.message}}",
				"response_text": "{{nodes.agent_loop.response_text}}",
				"iterations":    "{{nodes.agent_loop._iterations}}",
				"succeeded":     "{{nodes.agent_loop.succeeded}}",
			},
		}

		inputs := map[string]interface{}{"max_turns": 50}

		// This should NOT fail even though outputs reference undefined nodes.*
		resolved, err := ResolveWorkflowTemplates(raw, inputs)
		require.NoError(t, err, "ResolveWorkflowTemplates should not try to resolve outputs")

		// Node templates should be resolved
		nodes := resolved["nodes"].([]interface{})
		node := nodes[0].(map[string]interface{})
		assert.Equal(t, "iter.iteration >= 50", node["while"], "while should be resolved from input")

		// But outputs should remain as template strings, NOT resolved
		outputs := resolved["outputs"].(map[string]interface{})
		assert.Equal(t, "{{nodes.agent_loop.message}}", outputs["message"],
			"outputs.message should remain as template")
		assert.Equal(t, "{{nodes.agent_loop.response_text}}", outputs["response_text"],
			"outputs.response_text should remain as template")
		assert.Equal(t, "{{nodes.agent_loop._iterations}}", outputs["iterations"],
			"outputs.iterations should remain as template")
		assert.Equal(t, "{{nodes.agent_loop.succeeded}}", outputs["succeeded"],
			"outputs.succeeded should remain as template")
	})

	t.Run("outputs preserved in ResolveAndParseWorkflow", func(t *testing.T) {
		// Full YAML with outputs referencing steps
		yaml := []byte(`
name: test-workflow
apiVersion: "0.0.6"

inputs:
  max_turns:
    type: integer
    default: 50

entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "iter.iteration >= {{inputs.max_turns}}"
    ref: builtin://agent-turn

outputs:
  message: "{{nodes.agent_loop.message}}"
  iterations: "{{nodes.agent_loop._iterations}}"
`)

		inputs := map[string]interface{}{"max_turns": 25}

		// This should NOT fail
		wf, err := ResolveAndParseWorkflow(yaml, inputs)
		require.NoError(t, err, "ResolveAndParseWorkflow should not try to resolve outputs")

		// Node templates should be resolved
		assert.Equal(t, "loop", wf.Nodes[0].GetType())
		loop := wf.Nodes[0].GetLoop()
		require.NotNil(t, loop)
		assert.Equal(t, "iter.iteration >= 25", model.DirectCelExpr(loop.GetWhile()), "while should be resolved")

		// Outputs should remain as template strings for later evaluation
		assert.Equal(t, "{{nodes.agent_loop.message}}", wf.GetOutputs()["message"],
			"outputs should remain as template strings")
		assert.Equal(t, "{{nodes.agent_loop._iterations}}", wf.GetOutputs()["iterations"],
			"outputs should remain as template strings")
	})
}

// TestNodeInputsNotResolvedAtLoadTime verifies that node.inputs are NOT resolved
// during template resolution. Node inputs often reference runtime context like
// workflow.thread, workflow.id, or step outputs that don't exist at load time.
// They are evaluated at step execution time via evaluateTemplates().
func TestNodeInputsNotResolvedAtLoadTime(t *testing.T) {
	t.Run("node inputs remain as templates", func(t *testing.T) {
		// This mimics the structure of agent.yaml where node inputs reference
		// inputs.* fields that may not be provided
		raw := map[string]interface{}{
			"name": "test-workflow",
			"nodes": []interface{}{
				map[string]interface{}{
					"id":    "agent_loop",
					"type":  "loop",
					"while": "iter.iteration >= {{inputs.max_turns}}",
					"ref":   "builtin://agent-turn",
					// These args reference runtime context that doesn't exist at load time
					"args": map[string]interface{}{
						"chat_id":     "{{inputs.chat_id}}",
						"thread":      "{{workflow.thread}}",
						"workflow_id": "{{inputs.workflow_id}}",
					},
				},
			},
		}

		// Only provide max_turns - other inputs like workflow_id are optional/hidden
		inputs := map[string]interface{}{"max_turns": 50}

		// This should NOT fail even though inputs reference undefined fields
		resolved, err := ResolveWorkflowTemplates(raw, inputs)
		require.NoError(t, err, "ResolveWorkflowTemplates should not try to resolve node inputs")

		// while should be resolved (it's a typed field)
		nodes := resolved["nodes"].([]interface{})
		node := nodes[0].(map[string]interface{})
		assert.Equal(t, "iter.iteration >= 50", node["while"], "while should be resolved from input")

		// But node args should remain as template strings
		nodeArgs := node["args"].(map[string]interface{})
		assert.Equal(t, "{{inputs.chat_id}}", nodeArgs["chat_id"],
			"node args should remain as templates")
		assert.Equal(t, "{{workflow.thread}}", nodeArgs["thread"],
			"node args should remain as templates")
		assert.Equal(t, "{{inputs.workflow_id}}", nodeArgs["workflow_id"],
			"node args should remain as templates")
	})

	t.Run("node inputs preserved in ResolveAndParseWorkflow", func(t *testing.T) {
		// Full YAML with node inputs that reference runtime context
		yaml := []byte(`
name: test-workflow
apiVersion: "0.0.6"

inputs:
  max_turns:
    type: integer
    default: 50
  chat_id:
    type: string
    ui: hidden
  workflow_id:
    type: string
    ui: hidden

entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: "iter.iteration >= {{inputs.max_turns}}"
    ref: builtin://agent-turn
    args:
      chat_id: "{{inputs.chat_id}}"
      thread: "{{workflow.thread}}"
      workflow_id: "{{inputs.workflow_id}}"
`)

		// Only provide max_turns - chat_id and workflow_id are optional hidden inputs
		inputs := map[string]interface{}{"max_turns": 25}

		// This should NOT fail
		wf, err := ResolveAndParseWorkflow(yaml, inputs)
		require.NoError(t, err, "ResolveAndParseWorkflow should not try to resolve node inputs")

		// while should be resolved
		assert.Equal(t, "loop", wf.Nodes[0].GetType())
		loop := wf.Nodes[0].GetLoop()
		require.NotNil(t, loop)
		assert.Equal(t, "iter.iteration >= 25", model.DirectCelExpr(loop.GetWhile()), "while should be resolved")

		// Node args should remain as template strings
		args := loop.GetArgs()
		require.NotNil(t, args)
		assert.Equal(t, "{{inputs.chat_id}}", args["chat_id"].GetStringValue(),
			"node args should remain as template strings")
		assert.Equal(t, "{{workflow.thread}}", args["thread"].GetStringValue(),
			"node args should remain as template strings")
		assert.Equal(t, "{{inputs.workflow_id}}", args["workflow_id"].GetStringValue(),
			"node args should remain as template strings")
	})

	t.Run("save_message also preserved", func(t *testing.T) {
		// Node save_message can also have templates that reference runtime context
		raw := map[string]interface{}{
			"name": "test-workflow",
			"nodes": []interface{}{
				map[string]interface{}{
					"id":   "step1",
					"type": "call_llm",
					"save_message": map[string]interface{}{
						"role":    "{{output.role}}",
						"content": "{{output.content}}",
					},
				},
			},
		}

		inputs := map[string]interface{}{}

		resolved, err := ResolveWorkflowTemplates(raw, inputs)
		require.NoError(t, err, "save_message should not be resolved")

		nodes := resolved["nodes"].([]interface{})
		saveMsg := nodes[0].(map[string]interface{})["save_message"].(map[string]interface{})
		assert.Equal(t, "{{output.role}}", saveMsg["role"], "save_message should remain as template")
		assert.Equal(t, "{{output.content}}", saveMsg["content"], "save_message should remain as template")
	})

	t.Run("yield field preserved as template", func(t *testing.T) {
		// Test that the yield field on loop nodes is preserved as a template
		// (not replaced with __TEMPLATE_PLACEHOLDER__) so it can be evaluated
		// at runtime by evaluateYieldCondition.
		raw := map[string]interface{}{
			"name": "test-workflow",
			"nodes": []interface{}{
				map[string]interface{}{
					"id":    "agent_loop",
					"type":  "loop",
					"while": "outputs.tool_calls != null",
					"yield": "{{inputs.yield}}",
					"ref":   "builtin://agent-turn",
				},
			},
		}

		// Even with inputs provided, the yield field should stay as template
		// because it's in the preserve list (evaluated at runtime, not load time)
		inputs := map[string]interface{}{"yield": true}

		resolved, err := ResolveWorkflowTemplates(raw, inputs)
		require.NoError(t, err, "yield template should not cause errors")

		nodes := resolved["nodes"].([]interface{})
		yieldVal := nodes[0].(map[string]interface{})["yield"]
		assert.Equal(t, "{{inputs.yield}}", yieldVal, "yield should remain as template for runtime evaluation")
	})

}
