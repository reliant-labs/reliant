// Copyright (c) 2025 Reliant Labs
package validation

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAST_DirectAccess_Unsafe tests that direct access to conditional nodes is detected as unsafe.
func TestAST_DirectAccess_Unsafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expr     string
		expected []string // expected unsafe node IDs
	}{
		{
			name:     "simple field access",
			expr:     "nodes.conditional_node.output",
			expected: []string{"conditional_node"},
		},
		{
			name:     "nested field access",
			expr:     "nodes.conditional_node.message.content",
			expected: []string{"conditional_node"},
		},
		{
			name:     "multiple conditional nodes",
			expr:     "nodes.cond1.output + nodes.cond2.output",
			expected: []string{"cond1", "cond2"},
		},
		{
			name:     "complex expression",
			expr:     "size(nodes.conditional_node.message.content) > 10",
			expected: []string{"conditional_node"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			// Mark all referenced nodes as conditional
			conditionalNodes := make(map[string]bool)
			for _, nodeID := range tt.expected {
				conditionalNodes[nodeID] = true
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)

			require.Len(t, unsafe, len(tt.expected), "unexpected number of unsafe accesses")

			// Check that all expected nodes are detected
			foundNodes := make(map[string]bool)
			for _, access := range unsafe {
				foundNodes[access.NodeID] = true
			}

			for _, expectedNode := range tt.expected {
				assert.True(t, foundNodes[expectedNode], "expected to find unsafe access to node %s", expectedNode)
			}
		})
	}
}

// TestAST_OptionalChaining_Safe tests that optional chaining is correctly identified as safe.
// Note: CEL's optional chaining syntax uses macros and requires EnableMacroCallTracking
func TestAST_OptionalChaining_Safe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "optional field access",
			expr: "nodes.?conditional_node.output",
		},
		{
			name: "optional nested field access",
			expr: "nodes.?conditional_node.message.content",
		},
		{
			name: "optional with fallback",
			expr: "nodes.?conditional_node.output.orValue('default')",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
				cel.EnableMacroCallTracking(),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			if issues != nil && issues.Err() != nil {
				t.Skipf("Optional chaining not supported in this CEL version: %v", issues.Err())
				return
			}

			conditionalNodes := map[string]bool{
				"conditional_node": true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)
			assert.Empty(t, unsafe, "optional chaining should be safe")
		})
	}
}

// TestAST_HasCheck_Safe tests that has() checks are correctly identified as safe.
func TestAST_HasCheck_Safe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "has on node",
			expr: "has(nodes.conditional_node)",
		},

		{
			name: "has with logical or",
			expr: "has(nodes.conditional_node) || false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			conditionalNodes := map[string]bool{
				"conditional_node": true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)
			assert.Empty(t, unsafe, "has() check should make access safe")
		})
	}
}

// TestAST_NullComparison_Safe tests that null comparisons are correctly identified as safe.
func TestAST_NullComparison_Safe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "not equal to null",
			expr: "nodes.conditional_node != null",
		},
		{
			name: "equal to null",
			expr: "nodes.conditional_node == null",
		},
		{
			name: "reverse not equal",
			expr: "null != nodes.conditional_node",
		},
		{
			name: "reverse equal",
			expr: "null == nodes.conditional_node",
		},

		{
			name: "field null check",
			expr: "nodes.conditional_node.output != null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			conditionalNodes := map[string]bool{
				"conditional_node": true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)
			assert.Empty(t, unsafe, "null comparison should make access safe")
		})
	}
}

// TestAST_StringLiteral_NoFalsePositive tests that string literals don't trigger false positives.
func TestAST_StringLiteral_NoFalsePositive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "string literal with nodes",
			expr: `"nodes.conditional_node.output"`,
		},
		{
			name: "string with template-like syntax",
			expr: `"Result: nodes.x.y"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv()
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			conditionalNodes := map[string]bool{
				"conditional_node": true,
				"x":                true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)
			assert.Empty(t, unsafe, "string literals should not trigger warnings")
		})
	}
}

// TestAST_MultipleNodes_MixedSafety tests expressions with both safe and unsafe accesses.
func TestAST_MultipleNodes_MixedSafety(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		expr           string
		expectedUnsafe []string
	}{
		{
			name:           "safe and unsafe mixed",
			expr:           "has(nodes.safe_node) && nodes.unsafe_node.output",
			expectedUnsafe: []string{"unsafe_node"},
		},
		{
			name:           "null check for one, direct for another",
			expr:           "nodes.checked != null && nodes.unchecked.output",
			expectedUnsafe: []string{"unchecked"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			conditionalNodes := map[string]bool{
				"safe_node":     true,
				"unsafe_node":   true,
				"checked":       true,
				"unchecked":     true,
				"optional_node": true,
				"direct_node":   true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)

			require.Len(t, unsafe, len(tt.expectedUnsafe), "unexpected number of unsafe accesses")

			foundNodes := make(map[string]bool)
			for _, access := range unsafe {
				foundNodes[access.NodeID] = true
			}

			for _, expectedNode := range tt.expectedUnsafe {
				assert.True(t, foundNodes[expectedNode], "expected to find unsafe access to node %s", expectedNode)
			}
		})
	}
}

// TestAST_NestedAccess tests deeply nested field access patterns.
func TestAST_NestedAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		expr     string
		expected []string
	}{
		{
			name:     "deeply nested unsafe",
			expr:     "nodes.cond.message.content.text.value",
			expected: []string{"cond"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			require.Nil(t, issues)

			conditionalNodes := map[string]bool{
				"cond": true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)

			if len(tt.expected) == 0 {
				assert.Empty(t, unsafe)
			} else {
				require.Len(t, unsafe, len(tt.expected))
				foundNodes := make(map[string]bool)
				for _, access := range unsafe {
					foundNodes[access.NodeID] = true
				}
				for _, expectedNode := range tt.expected {
					assert.True(t, foundNodes[expectedNode])
				}
			}
		})
	}
}

// TestAST_ComplexExpressions tests complex real-world expressions.
func TestAST_ComplexExpressions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		expr           string
		expectedUnsafe []string
	}{
		{
			name:           "ternary with unsafe",
			expr:           "nodes.cond1.output > 10 ? 'high' : 'low'",
			expectedUnsafe: []string{"cond1"},
		},

		{
			name:           "list comprehension unsafe",
			expr:           "[1, 2, 3].map(x, x * nodes.cond1.multiplier)",
			expectedUnsafe: []string{"cond1"},
		},
		{
			name:           "complex boolean logic",
			expr:           "(nodes.cond1.value > 5 || nodes.cond2.value < 10) && nodes.regular.flag",
			expectedUnsafe: []string{"cond1", "cond2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := cel.NewEnv(
				cel.Variable("nodes", cel.DynType),
			)
			require.NoError(t, err)

			compiledAst, issues := env.Compile(tt.expr)
			if issues != nil && issues.Err() != nil {
				t.Skipf("Expression doesn't compile (may use unsupported syntax): %v", issues.Err())
				return
			}

			conditionalNodes := map[string]bool{
				"cond1": true,
				"cond2": true,
			}

			unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)

			if len(tt.expectedUnsafe) == 0 {
				assert.Empty(t, unsafe, "expected no unsafe accesses")
			} else {
				require.Len(t, unsafe, len(tt.expectedUnsafe), "unexpected number of unsafe accesses")
				foundNodes := make(map[string]bool)
				for _, access := range unsafe {
					foundNodes[access.NodeID] = true
				}
				for _, expectedNode := range tt.expectedUnsafe {
					assert.True(t, foundNodes[expectedNode], "expected to find unsafe access to node %s", expectedNode)
				}
			}
		})
	}
}

// TestAST_UnconditionalNodes tests that unconditional nodes don't trigger warnings.
func TestAST_UnconditionalNodes(t *testing.T) {
	t.Parallel()
	expr := "nodes.regular_node.output + nodes.another_regular.value"

	env, err := cel.NewEnv(
		cel.Variable("nodes", cel.DynType),
	)
	require.NoError(t, err)

	compiledAst, issues := env.Compile(expr)
	require.Nil(t, issues)

	// Empty conditional nodes map
	conditionalNodes := map[string]bool{}

	unsafe := detectConditionalNodeAccess(compiledAst, conditionalNodes)
	assert.Empty(t, unsafe, "unconditional nodes should not trigger warnings")
}

// TestAST_EmptyExpression tests handling of empty/nil expressions.
func TestAST_EmptyExpression(t *testing.T) {
	t.Parallel()
	conditionalNodes := map[string]bool{
		"cond": true,
	}

	// Test with nil AST
	unsafe := detectConditionalNodeAccess(nil, conditionalNodes)
	assert.Empty(t, unsafe, "nil AST should return empty result")

	// Test with empty conditional nodes
	env, err := cel.NewEnv(
		cel.Variable("nodes", cel.DynType),
	)
	require.NoError(t, err)

	compiledAst, issues := env.Compile("nodes.cond.output")
	require.Nil(t, issues)

	unsafe = detectConditionalNodeAccess(compiledAst, map[string]bool{})
	assert.Empty(t, unsafe, "empty conditional nodes should return empty result")
}
