// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/require"
)

// Helper function to create a CEL environment with our custom functions
func newTestCELEnv(t *testing.T) *cel.Env {
	env, err := cel.NewEnv(
		cel.StdLib(),
		cel.Variable("data", cel.DynType),
		cel.Variable("items", cel.DynType),
		ext.Strings(),
	)
	require.NoError(t, err)

	// Add our custom functions
	for _, opt := range wfcel.CustomFunctions() {
		env, err = env.Extend(opt)
		require.NoError(t, err)
	}

	return env
}

func evalCEL(t *testing.T, env *cel.Env, expr string, vars map[string]interface{}) interface{} {
	ast, issues := env.Compile(expr)
	require.NoError(t, issues.Err(), "compile failed for: %s", expr)

	prg, err := env.Program(ast)
	require.NoError(t, err)

	result, _, err := prg.Eval(vars)
	require.NoError(t, err, "eval failed for: %s", expr)

	return result.Value()
}

// ============================================================================
// coalesce() tests
// ============================================================================

func TestCelCoalesce_ReturnsFirstNonNull(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	tests := []struct {
		name     string
		expr     string
		vars     map[string]interface{}
		expected interface{}
	}{
		{
			name:     "first is non-null",
			expr:     `coalesce("first", "second")`,
			vars:     map[string]interface{}{},
			expected: "first",
		},
		{
			name:     "first is null, return second",
			expr:     `coalesce(data, "default")`,
			vars:     map[string]interface{}{"data": nil},
			expected: "default",
		},
		{
			name:     "three args, first is null",
			expr:     `coalesce(data, items, "fallback")`,
			vars:     map[string]interface{}{"data": nil, "items": nil},
			expected: "fallback",
		},
		{
			name:     "three args, middle is non-null",
			expr:     `coalesce(data, items, "fallback")`,
			vars:     map[string]interface{}{"data": nil, "items": "middle"},
			expected: "middle",
		},
		{
			name:     "four args, last is used",
			expr:     `coalesce(data, data, data, "final")`,
			vars:     map[string]interface{}{"data": nil},
			expected: "final",
		},
		{
			name:     "works with numbers",
			expr:     `coalesce(data, 42)`,
			vars:     map[string]interface{}{"data": nil},
			expected: int64(42),
		},
		{
			name:     "zero is not null",
			expr:     `coalesce(0, 42)`,
			vars:     map[string]interface{}{},
			expected: int64(0),
		},
		{
			name:     "empty string is not null",
			expr:     `coalesce("", "default")`,
			vars:     map[string]interface{}{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evalCEL(t, env, tt.expr, tt.vars)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestCelCoalesce_AllNull_ReturnsNull(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	// When all values are null, coalesce returns the CEL null type
	// Test that the expression compiles and can be used in a condition
	result := evalCEL(t, env, `coalesce(data, items, data) == null`, map[string]interface{}{
		"data":  nil,
		"items": nil,
	})

	// Should return true because coalesce of all nulls returns null
	require.Equal(t, true, result)
}

// ============================================================================
// getOrDefault() tests
// ============================================================================

func TestCelGetOrDefault_KeyExists(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data, "name", "unknown")`, map[string]interface{}{
		"data": map[string]interface{}{
			"name": "Alice",
		},
	})

	require.Equal(t, "Alice", result)
}

func TestCelGetOrDefault_KeyMissing(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data, "missing", "default_value")`, map[string]interface{}{
		"data": map[string]interface{}{
			"name": "Alice",
		},
	})

	require.Equal(t, "default_value", result)
}

func TestCelGetOrDefault_ValueIsNull(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data, "value", "fallback")`, map[string]interface{}{
		"data": map[string]interface{}{
			"value": nil,
		},
	})

	require.Equal(t, "fallback", result)
}

func TestCelGetOrDefault_MapIsNull(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data, "key", "default")`, map[string]interface{}{
		"data": nil,
	})

	require.Equal(t, "default", result)
}

func TestCelGetOrDefault_NumericValue(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data, "count", 0)`, map[string]interface{}{
		"data": map[string]interface{}{
			"count": 42,
		},
	})

	require.Equal(t, int64(42), result)
}

func TestCelGetOrDefault_NestedMap(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `getOrDefault(data.nested, "key", "default")`, map[string]interface{}{
		"data": map[string]interface{}{
			"nested": map[string]interface{}{
				"key": "found",
			},
		},
	})

	require.Equal(t, "found", result)
}

// ============================================================================
// parseDuration() tests
// ============================================================================

func TestCelParseDuration_ParsesMinutes(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("5m")`, map[string]interface{}{})

	require.Equal(t, float64(300), result)
}

func TestCelParseDuration_ParsesHours(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("2h")`, map[string]interface{}{})

	require.Equal(t, float64(7200), result)
}

func TestCelParseDuration_ParsesComplex(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("1h30m")`, map[string]interface{}{})

	require.Equal(t, float64(5400), result)
}

func TestCelParseDuration_ParsesSeconds(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("45s")`, map[string]interface{}{})

	require.Equal(t, float64(45), result)
}

func TestCelParseDuration_ParsesMilliseconds(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("500ms")`, map[string]interface{}{})

	require.Equal(t, float64(0.5), result)
}

func TestCelParseDuration_ParsesFractional(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `parseDuration("1.5h")`, map[string]interface{}{})

	require.Equal(t, float64(5400), result)
}

func TestCelParseDuration_InvalidFormat(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	ast, issues := env.Compile(`parseDuration("invalid")`)
	require.NoError(t, issues.Err())

	prg, err := env.Program(ast)
	require.NoError(t, err)

	_, _, err = prg.Eval(map[string]interface{}{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to parse")
}

// ============================================================================
// join() tests
// ============================================================================

func TestCelJoin_JoinsStrings(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `["a", "b", "c"].join(",")`, map[string]interface{}{})
	require.Equal(t, "a,b,c", result)
}

func TestCelJoin_EmptyList(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `items.join(",")`, map[string]interface{}{"items": []string{}})
	require.Equal(t, "", result)
}

func TestCelJoin_SingleElement(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `["only"].join(",")`, map[string]interface{}{})
	require.Equal(t, "only", result)
}

func TestCelJoin_CustomDelimiter(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `["a", "b", "c"].join(" | ")`, map[string]interface{}{})
	require.Equal(t, "a | b | c", result)
}

func TestCelJoin_FromVariable(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `items.join(",")`, map[string]interface{}{
		"items": []interface{}{"general", "researcher", "code_reviewer"},
	})
	require.Equal(t, "general,researcher,code_reviewer", result)
}

func TestCelJoin_MixedTypes(t *testing.T) {
	t.Parallel()
	// ext.Strings().join() only supports string lists; mixed-type lists are not supported.
	// Verify that join on a string-coerced list works by converting each element to string first.
	env := newTestCELEnv(t)

	result := evalCEL(t, env, `["str", string(42), string(true)].join(",")`, map[string]interface{}{})
	require.Equal(t, "str,42,true", result)
}

// ============================================================================
// Integration tests - combining new functions
// ============================================================================

func TestCelNewFunctions_Integration(t *testing.T) {
	t.Parallel()
	env := newTestCELEnv(t)

	// Test coalesce with getOrDefault
	result := evalCEL(t, env, `coalesce(getOrDefault(data, "missing", null), "fallback")`, map[string]interface{}{
		"data": map[string]interface{}{},
	})
	require.Equal(t, "fallback", result)

	// Test parseDuration comparison
	result2 := evalCEL(t, env, `parseDuration("5m") > parseDuration("4m")`, map[string]interface{}{})
	require.Equal(t, true, result2)

	// Test parseDuration with arithmetic
	result3 := evalCEL(t, env, `parseDuration("1h") / 60.0`, map[string]interface{}{})
	require.Equal(t, float64(60), result3) // 3600 seconds / 60 = 60 minutes
}
