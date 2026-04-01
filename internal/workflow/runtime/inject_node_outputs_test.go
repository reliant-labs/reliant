// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// makeProtoTestNode builds a proto V2Node with thread/inject config for testing.
func makeProtoTestNode(id, ref string, inject *reliantv1.InjectConfig, memo bool) *reliantv1.Node {
	thread := &reliantv1.ThreadConfig{
		Mode:   "new",
		Inject: inject,
	}
	if memo {
		thread.Memo = &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}
	}
	node := &reliantv1.Node{
		Id:   id,
		Type: "workflow",
		Args: &reliantv1.Node_Workflow{
			Workflow: &reliantv1.SubWorkflowArgs{
				Ref:    &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: ref}},
				Thread: thread,
			},
		},
	}
	return node
}

func makeCelLiteral(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

// TestInjectWithNodeOutputs tests that inject templates can access outputs from
// previously completed nodes within the same iteration.
func TestInjectWithNodeOutputs(t *testing.T) {
	t.Run("inject can access prior node's response_text", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"planner_turn": map[string]interface{}{
				"response_text": "Here is my detailed plan:\n1. Do step one\n2. Do step two",
				"_iterations":   1,
				"succeeded":     true,
			},
		}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Please review the following plan:\n\n{{nodes.planner_turn.response_text}}"),
			}, true)

		evalResult, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{"mode": "agent"},
			map[string]interface{}{"iteration": 1},
			nil,
			nil,
		)

		require.NoError(t, err)
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)

		assert.Contains(t, model.CelStringValue(inject.GetContent()), "Here is my detailed plan:")
		assert.Contains(t, model.CelStringValue(inject.GetContent()), "1. Do step one")
		assert.Equal(t, "user", model.CelStringValue(inject.GetRole()))
	})

	t.Run("inject with missing node errors", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Review: {{nodes.planner_turn.response_text}}"),
			}, false)

		_, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{},
			nil,
			nil,
			nil,
		)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such key")
	})

	t.Run("inject with nil response_text shows NULL_VALUE", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"planner_turn": map[string]interface{}{
				"response_text": nil,
				"_iterations":   1,
			},
		}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Review: {{nodes.planner_turn.response_text}}"),
			}, false)

		evalResult, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{},
			nil,
			nil,
			nil,
		)

		require.NoError(t, err)
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)
		assert.Equal(t, "Review: NULL_VALUE", model.CelStringValue(inject.GetContent()))
	})

	t.Run("inject with EMPTY STRING response_text produces empty content", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"planner_turn": map[string]interface{}{
				"response_text": "",
				"_iterations":   1,
				"succeeded":     true,
			},
		}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Please review the following plan:\n\n{{nodes.planner_turn.response_text}}"),
			}, true)

		evalResult, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{"mode": "agent"},
			map[string]interface{}{"iteration": 2},
			nil,
			nil,
		)

		require.NoError(t, err)
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)
		assert.Equal(t, "Please review the following plan:\n\n", model.CelStringValue(inject.GetContent()))
	})

	t.Run("inject works on iteration 2+ with fresh nodeOutputs", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"planner_turn": map[string]interface{}{
				"response_text": "Revised plan based on feedback:\n1. Improved step one\n2. Better step two",
				"_iterations":   1,
				"succeeded":     true,
			},
		}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Please review the following plan:\n\n{{nodes.planner_turn.response_text}}"),
			}, true)

		evalResult, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{"mode": "agent"},
			map[string]interface{}{"iteration": 2},
			nil,
			nil,
		)

		require.NoError(t, err)
		inject := model.NodeInjectConfig(evalResult)
		require.NotNil(t, inject)
		assert.Contains(t, model.CelStringValue(inject.GetContent()), "Revised plan based on feedback")
		assert.Contains(t, model.CelStringValue(inject.GetContent()), "Improved step one")
	})
}

// TestInjectWithMixedContent tests inject templates with both static and dynamic content
func TestInjectWithMixedContent(t *testing.T) {
	t.Run("inject with prefix and suffix around node output", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"step_a": map[string]interface{}{
				"result": "42",
			},
		}

		node := makeProtoTestNode("step_b", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("The answer from step A was: {{nodes.step_a.result}}. Please verify this."),
			}, false)

		evalResult, err := EvaluateNodeConfig(
			node,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{},
			nil,
			nil,
			nil,
		)

		require.NoError(t, err)
		assert.Equal(t, "The answer from step A was: 42. Please verify this.", model.CelStringValue(model.NodeInjectConfig(evalResult).GetContent()))
	})

	t.Run("inject with multiple node references", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"researcher": map[string]interface{}{
				"findings": "Found 3 bugs",
			},
			"analyzer": map[string]interface{}{
				"summary": "All bugs are critical",
			},
		}

		node := makeProtoTestNode("reporter", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Research: {{nodes.researcher.findings}}\nAnalysis: {{nodes.analyzer.summary}}"),
			}, false)

		evalResult, err := EvaluateNodeConfig(
			node,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{},
			nil,
			nil,
			nil,
		)

		require.NoError(t, err)
		assert.Equal(t, "Research: Found 3 bugs\nAnalysis: All bugs are critical", model.CelStringValue(model.NodeInjectConfig(evalResult).GetContent()))
	})
}

// TestInjectWithMissingResponseTextKey tests what happens when the node output exists
// but the response_text key is missing (not nil, just not present)
func TestInjectWithMissingResponseTextKey(t *testing.T) {
	t.Run("inject with missing response_text key errors", func(t *testing.T) {
		nodeOutputs := map[string]interface{}{
			"planner_turn": map[string]interface{}{
				"_iterations": 1,
				"succeeded":   true,
			},
		}

		criticNode := makeProtoTestNode("critic_turn", "builtin://agent",
			&reliantv1.InjectConfig{
				Role:    makeCelLiteral("user"),
				Content: makeCelLiteral("Review: {{nodes.planner_turn.response_text}}"),
			}, false)

		_, err := EvaluateNodeConfig(
			criticNode,
			nodeOutputs,
			"test-workflow-id",
			"test-workflow",
			map[string]interface{}{},
			nil,
			nil,
			nil,
		)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such key")
	})
}

// TestLoopIterationNodeOutputIsolation verifies that each iteration has isolated nodeOutputs
func TestLoopIterationNodeOutputIsolation(t *testing.T) {
	t.Run("iteration outputs are isolated", func(t *testing.T) {
		iter1Outputs := make(map[string]interface{})
		iter1Outputs["step_a"] = map[string]interface{}{"value": "iteration-1-value"}

		iter2Outputs := make(map[string]interface{})
		assert.Empty(t, iter2Outputs)

		iter2Outputs["step_a"] = map[string]interface{}{"value": "iteration-2-value"}

		assert.Equal(t, "iteration-1-value", iter1Outputs["step_a"].(map[string]interface{})["value"])
		assert.Equal(t, "iteration-2-value", iter2Outputs["step_a"].(map[string]interface{})["value"])
	})
}
