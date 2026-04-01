// Copyright (c) 2025 Reliant Labs
package cel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEngine_BasicExpressions(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expected   interface{}
	}{
		{
			name:       "simple boolean",
			expression: "true",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "comparison",
			expression: "x > 5",
			vars:       map[string]interface{}{"x": 10},
			expected:   true,
		},
		{
			name:       "string equality",
			expression: "name == 'test'",
			vars:       map[string]interface{}{"name": "test"},
			expected:   true,
		},
		{
			name:       "logical AND",
			expression: "x > 5 && y < 10",
			vars:       map[string]interface{}{"x": 10, "y": 5},
			expected:   true,
		},
		{
			name:       "nested object",
			expression: "tool.name == 'bash'",
			vars: map[string]interface{}{
				"tool": map[string]interface{}{
					"name": "bash",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.CompileAndEvaluate(context.Background(), tt.expression, tt.vars)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEngine_CompileError(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	_, err = engine.Compile("invalid syntax +++")
	require.Error(t, err)
}

func TestCompiledExpression_EvaluateBool(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	vars := map[string]interface{}{"x": 10}
	compiled, err := engine.CompileWithVars("x > 5", vars)
	require.NoError(t, err)

	result, err := compiled.EvaluateBool(context.Background(), vars)
	require.NoError(t, err)
	require.True(t, result)
}

func TestCompiledExpression_EvaluateBool_TypeError(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	vars := map[string]interface{}{"x": 10}
	compiled, err := engine.CompileWithVars("x + 5", vars)
	require.NoError(t, err)

	_, err = compiled.EvaluateBool(context.Background(), vars)
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not return boolean")
}

func TestEngine_NestedObjectAccess(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expected   bool
	}{
		{
			name:       "nested map access",
			expression: "context.auto_approve == true",
			vars: map[string]interface{}{
				"context": map[string]interface{}{
					"auto_approve": true,
				},
			},
			expected: true,
		},
		{
			name:       "deeply nested access",
			expression: "workflow.state.running == true",
			vars: map[string]interface{}{
				"workflow": map[string]interface{}{
					"state": map[string]interface{}{
						"running": true,
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.CompileAndEvaluateBool(context.Background(), tt.expression, tt.vars)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEngine_ComplexConditions(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expected   bool
	}{
		{
			name:       "workflow condition example",
			expression: "tool.name == 'bash' && context.auto_approve == false",
			vars: map[string]interface{}{
				"tool": map[string]interface{}{
					"name": "bash",
				},
				"context": map[string]interface{}{
					"auto_approve": false,
				},
			},
			expected: true,
		},
		{
			name:       "edge condition example",
			expression: "tool_result == null",
			vars: map[string]interface{}{
				"tool_result": nil,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.CompileAndEvaluateBool(context.Background(), tt.expression, tt.vars)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}
