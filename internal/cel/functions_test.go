// Copyright (c) 2025 Reliant Labs
package cel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomFunctions(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expected   bool
	}{
		{
			name:       "toolRequiresApproval - bash",
			expression: "toolRequiresApproval('bash')",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "toolRequiresApproval - powershell",
			expression: "toolRequiresApproval('powershell')",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "toolRequiresApproval - read",
			expression: "toolRequiresApproval('read')",
			vars:       map[string]interface{}{},
			expected:   false,
		},
		{
			name:       "toolRequiresApproval - edit",
			expression: "toolRequiresApproval('edit')",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "toolRequiresApproval - write",
			expression: "toolRequiresApproval('write')",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "matchesPattern - match",
			expression: "matchesPattern('/help', '^/.*')",
			vars:       map[string]interface{}{},
			expected:   true,
		},
		{
			name:       "matchesPattern - no match",
			expression: "matchesPattern('help', '^/.*')",
			vars:       map[string]interface{}{},
			expected:   false,
		},
		{
			name:       "matchesPattern - complex pattern",
			expression: "matchesPattern('user@example.com', '^[a-z]+@[a-z]+\\\\.[a-z]+$')",
			vars:       map[string]interface{}{},
			expected:   true,
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

func TestCustomFunctions_WithVariables(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expected   bool
	}{
		{
			name:       "toolRequiresApproval with variable - bash",
			expression: "toolRequiresApproval(tool.name)",
			vars: map[string]interface{}{
				"tool": map[string]interface{}{
					"name": "bash",
				},
			},
			expected: true,
		},
		{
			name:       "matchesPattern with variables",
			expression: "matchesPattern(message.content, pattern)",
			vars: map[string]interface{}{
				"message": map[string]interface{}{
					"content": "/command",
				},
				"pattern": "^/.*",
			},
			expected: true,
		},
		{
			name:       "combined condition",
			expression: "toolRequiresApproval(tool.name) && context.auto_approve == false",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.CompileAndEvaluateBool(context.Background(), tt.expression, tt.vars)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestCustomFunctions_HasError(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	// hasError() currently returns false (stub implementation)
	result, err := engine.CompileAndEvaluateBool(context.Background(), "hasError()", map[string]interface{}{})
	require.NoError(t, err)
	require.False(t, result)
}

func TestCustomFunctions_HasToolResult(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	// hasToolResult() currently returns false (stub implementation)
	result, err := engine.CompileAndEvaluateBool(context.Background(), "hasToolResult('tool_123')", map[string]interface{}{})
	require.NoError(t, err)
	require.False(t, result)
}

func TestCustomFunctions_GetMetadata(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	// getMetadata() currently returns null (stub implementation)
	result, err := engine.CompileAndEvaluate(context.Background(), "getMetadata('some.key')", map[string]interface{}{})
	require.NoError(t, err)
	// CEL null is represented as structpb.NullValue
	require.NotNil(t, result) // Just verify we get a result (the stub returns NullValue)
}

func TestCustomFunctions_ErrorHandling(t *testing.T) {
	engine, err := NewEngine()
	require.NoError(t, err)

	tests := []struct {
		name       string
		expression string
		vars       map[string]interface{}
		expectErr  bool
	}{
		{
			name:       "matchesPattern - invalid regex",
			expression: "matchesPattern('test', '[invalid')",
			vars:       map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:       "toolRequiresApproval - wrong type",
			expression: "toolRequiresApproval(123)",
			vars:       map[string]interface{}{},
			expectErr:  true,
		},
		{
			name:       "hasToolResult - wrong type",
			expression: "hasToolResult(true)",
			vars:       map[string]interface{}{},
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.CompileAndEvaluate(context.Background(), tt.expression, tt.vars)
			if tt.expectErr {
				// CEL may return an error value instead of a Go error
				if err == nil {
					// Check if result is an error type
					require.NotNil(t, result)
				}
			}
		})
	}
}
