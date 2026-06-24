// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// TestApplyDefaultsNilValues verifies that ApplyDefaults treats nil values
// as missing and applies schema defaults. This prevents CEL "no such overload"
// errors when comparing nil against numeric types.
func TestApplyDefaultsNilValues(t *testing.T) {
	intDefault := int64(185000)
	strDefault := "default_value"

	schema := map[string]*reliantv1.Input{
		"compaction_threshold": {
			Type: "integer",
			Config: &reliantv1.Input_IntegerInput{
				IntegerInput: &reliantv1.IntegerInputConfig{Default: &intDefault},
			},
		},
		"system_prompt": {
			Type: "string",
			Config: &reliantv1.Input_StringInput{
				StringInput: &reliantv1.StringInputConfig{Default: &strDefault},
			},
		},
	}

	t.Run("nil value gets default applied", func(t *testing.T) {
		inputs := map[string]interface{}{
			"compaction_threshold": nil, // e.g. from structpb.NullValue
			"system_prompt":        nil,
		}

		result := ApplyDefaults(inputs, schema)

		assert.Equal(t, int64(185000), result["compaction_threshold"])
		assert.Equal(t, "default_value", result["system_prompt"])
	})

	t.Run("nil value with no default remains missing before validation", func(t *testing.T) {
		noDefaultSchema := map[string]*reliantv1.Input{
			"compaction_threshold": {
				Type: "integer",
				Config: &reliantv1.Input_IntegerInput{
					IntegerInput: &reliantv1.IntegerInputConfig{},
				},
			},
		}

		inputs := map[string]interface{}{
			"compaction_threshold": nil,
		}

		result := ApplyDefaults(inputs, noDefaultSchema)

		assert.Nil(t, result["compaction_threshold"])
	})

	t.Run("nil value with no default gets runtime zero value after validation", func(t *testing.T) {
		noDefaultSchema := map[string]*reliantv1.Input{
			"compaction_threshold": {
				Type: "integer",
				Config: &reliantv1.Input_IntegerInput{
					IntegerInput: &reliantv1.IntegerInputConfig{},
				},
			},
		}

		inputs := map[string]interface{}{
			"compaction_threshold": nil,
		}

		result := ApplyDefaultsForRuntime(inputs, noDefaultSchema)

		assert.Equal(t, int64(0), result["compaction_threshold"])
	})

	t.Run("nil value in CEL comparison works", func(t *testing.T) {
		inputs := map[string]interface{}{
			"compaction_threshold": nil,
		}

		result := ApplyDefaultsForRuntime(inputs, schema)

		// Simulate the edge condition that was failing
		ctx := &wfcel.EdgeEvalContext{
			Nodes: map[string]interface{}{
				"execute_tools": map[string]interface{}{
					"thread_token_count": float64(15000), // from JSON roundtrip
				},
			},
			Inputs: result,
		}

		// This was previously failing with "no such overload"
		matched, err := wfcel.EvaluateBool(
			"nodes.execute_tools.thread_token_count > inputs.compaction_threshold",
			ctx,
		)
		require.NoError(t, err, "CEL comparison should not fail after ApplyDefaults fixes nil")
		assert.False(t, matched, "15000 should not be > 185000")
	})

	t.Run("nil value in group input gets default", func(t *testing.T) {
		groupDefault := int64(10)
		groupSchema := map[string]*reliantv1.Input{
			"agent": {
				Type: "group",
				Config: &reliantv1.Input_GroupInput{
					GroupInput: &reliantv1.GroupInputConfig{
						Inputs: map[string]*reliantv1.Input{
							"max_turns": {
								Type: "integer",
								Config: &reliantv1.Input_IntegerInput{
									IntegerInput: &reliantv1.IntegerInputConfig{Default: &groupDefault},
								},
							},
						},
					},
				},
			},
		}

		inputs := map[string]interface{}{
			"agent": map[string]interface{}{
				"max_turns": nil,
			},
		}

		result := ApplyDefaults(inputs, groupSchema)

		agent, ok := result["agent"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, int64(10), agent["max_turns"])
	})
}

// TestApplyDefaultsWithGroupInputs verifies that ApplyDefaults creates nested
// structures for group inputs, enabling CEL access like inputs.agent.model.
func TestApplyDefaultsWithGroupInputs(t *testing.T) {
	strDefault := "gpt-4"
	numDefault := 0.7
	intDefault := int64(10)
	strDefault2 := "default_value"

	// Schema with a group input (similar to auditing-agent.yaml)
	schema := map[string]*reliantv1.Input{
		"agent": {
			Type: "group",
			Config: &reliantv1.Input_GroupInput{
				GroupInput: &reliantv1.GroupInputConfig{
					Inputs: map[string]*reliantv1.Input{
						"model": {
							Type: "string",
							Config: &reliantv1.Input_StringInput{
								StringInput: &reliantv1.StringInputConfig{Default: &strDefault},
							},
						},
						"temperature": {
							Type: "number",
							Config: &reliantv1.Input_NumberInput{
								NumberInput: &reliantv1.NumberInputConfig{Default: &numDefault},
							},
						},
						"max_turns": {
							Type: "integer",
							Config: &reliantv1.Input_IntegerInput{
								IntegerInput: &reliantv1.IntegerInputConfig{Default: &intDefault},
							},
						},
					},
				},
			},
		},
		"other_input": {
			Type: "string",
			Config: &reliantv1.Input_StringInput{
				StringInput: &reliantv1.StringInputConfig{Default: &strDefault2},
			},
		},
	}

	t.Run("creates nested structure for group inputs", func(t *testing.T) {
		inputs := map[string]interface{}{
			"agent": map[string]interface{}{
				"model": "claude-3",
			},
		}

		result := ApplyDefaults(inputs, schema)

		// Check that agent is a nested map
		agent, ok := result["agent"].(map[string]interface{})
		require.True(t, ok, "agent should be a map, got %T", result["agent"])

		// Check that provided value is preserved
		assert.Equal(t, "claude-3", agent["model"])

		// Check that defaults are applied for missing group fields
		assert.Equal(t, 0.7, agent["temperature"])
		assert.Equal(t, int64(10), agent["max_turns"])

		// Check non-group input
		assert.Equal(t, "default_value", result["other_input"])
	})

	t.Run("creates group from scratch when defaults exist", func(t *testing.T) {
		inputs := map[string]interface{}{
			"other_input": "provided_value",
		}

		result := ApplyDefaults(inputs, schema)

		// Agent should be created with all defaults
		agent, ok := result["agent"].(map[string]interface{})
		require.True(t, ok, "agent should be created as a map")

		assert.Equal(t, "gpt-4", agent["model"])
		assert.Equal(t, 0.7, agent["temperature"])
		assert.Equal(t, int64(10), agent["max_turns"])

		// Provided value preserved
		assert.Equal(t, "provided_value", result["other_input"])
	})

	t.Run("does not create group for missing required nested inputs", func(t *testing.T) {
		requiredSchema := map[string]*reliantv1.Input{
			"agent": {
				Type: "group",
				Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
					Inputs: map[string]*reliantv1.Input{
						"model": {
							Type: "model",
							Config: &reliantv1.Input_ModelInput{
								ModelInput: &reliantv1.ModelInputConfig{},
							},
						},
					},
				}},
			},
		}

		result := ApplyDefaults(map[string]interface{}{}, requiredSchema)

		assert.NotContains(t, result, "agent")
	})

	t.Run("runtime defaults create group for missing required nested inputs", func(t *testing.T) {
		requiredSchema := map[string]*reliantv1.Input{
			"agent": {
				Type: "group",
				Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{
					Inputs: map[string]*reliantv1.Input{
						"max_turns": {
							Type: "integer",
							Config: &reliantv1.Input_IntegerInput{
								IntegerInput: &reliantv1.IntegerInputConfig{},
							},
						},
					},
				}},
			},
		}

		result := ApplyDefaultsForRuntime(map[string]interface{}{}, requiredSchema)
		agent, ok := result["agent"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, int64(0), agent["max_turns"])
	})

	t.Run("CEL can access nested group values", func(t *testing.T) {
		inputs := map[string]interface{}{
			"agent": map[string]interface{}{
				"model":       "claude-3",
				"temperature": 0.5,
			},
		}

		result := ApplyDefaults(inputs, schema)

		// Build typed CEL context and verify access patterns
		ctx := &wfcel.NodeResolutionContext{
			Inputs:   result,
			Nodes:    map[string]interface{}{},
			Workflow: &model.WorkflowContext{ID: "test-id", Name: "test-workflow"},
		}

		// CEL should be able to evaluate inputs.agent.model
		expr := "inputs.agent.model"
		value, err := wfcel.EvaluateValue(expr, ctx)
		require.NoError(t, err, "CEL should evaluate %s without error", expr)
		assert.Equal(t, "claude-3", value)

		// CEL should be able to evaluate inputs.agent.temperature
		expr = "inputs.agent.temperature"
		value, err = wfcel.EvaluateValue(expr, ctx)
		require.NoError(t, err, "CEL should evaluate %s without error", expr)
		assert.Equal(t, 0.5, value)

		// CEL should access default values too
		expr = "inputs.agent.max_turns"
		value, err = wfcel.EvaluateValue(expr, ctx)
		require.NoError(t, err, "CEL should evaluate %s without error", expr)
		assert.Equal(t, int64(10), value)
	})
}
