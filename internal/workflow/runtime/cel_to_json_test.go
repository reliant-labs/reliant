// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/google/cel-go/cel"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCelToJsonFunction(t *testing.T) {
	t.Run("serializes simple object", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(data)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": map[string]interface{}{
				"name":  "test",
				"value": 42,
			},
		})
		require.NoError(t, err)

		result := out.Value().(string)
		assert.Contains(t, result, `"name":"test"`)
		assert.Contains(t, result, `"value":42`)
	})

	t.Run("serializes array of tool results", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("tool_results", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(tool_results)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		toolResults := []interface{}{
			map[string]interface{}{
				"tool_call_id": "abc123",
				"name":         "view",
				"content":      "file contents here",
				"is_error":     false,
			},
			map[string]interface{}{
				"tool_call_id": "def456",
				"name":         "bash",
				"content":      "command output",
				"is_error":     false,
			},
		}

		out, _, err := prg.Eval(map[string]interface{}{
			"tool_results": toolResults,
		})
		require.NoError(t, err)

		result := out.Value().(string)
		assert.Contains(t, result, `"tool_call_id":"abc123"`)
		assert.Contains(t, result, `"name":"view"`)
		assert.Contains(t, result, `"tool_call_id":"def456"`)
		assert.Contains(t, result, `"name":"bash"`)
	})

	t.Run("member function syntax works", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		// Using member function syntax: data.toJson()
		ast, issues := env.Compile(`data.toJson()`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": map[string]interface{}{
				"key": "value",
			},
		})
		require.NoError(t, err)

		result := out.Value().(string)
		assert.Contains(t, result, `"key":"value"`)
	})

	t.Run("serializes simple array", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(data)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": []interface{}{1, 2, 3},
		})
		require.NoError(t, err)

		assert.Equal(t, "[1,2,3]", out.Value().(string))
	})

	t.Run("serializes string", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(data)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": "hello world",
		})
		require.NoError(t, err)

		assert.Equal(t, `"hello world"`, out.Value().(string))
	})

	t.Run("serializes boolean", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(data)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": true,
		})
		require.NoError(t, err)

		assert.Equal(t, "true", out.Value().(string))
	})

	t.Run("serializes nested structures", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`toJson(data)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{
			"data": map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": map[string]interface{}{
						"value": "deep",
					},
				},
			},
		})
		require.NoError(t, err)

		result := out.Value().(string)
		assert.Contains(t, result, `"outer"`)
		assert.Contains(t, result, `"inner"`)
		assert.Contains(t, result, `"value":"deep"`)
	})
}

func TestToJsonAndParseJsonRoundTrip(t *testing.T) {
	t.Run("parseJson(toJson(data)) returns original structure", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("data", cel.DynType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson(toJson(data))`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		original := map[string]interface{}{
			"name":  "test",
			"value": float64(42), // Use float64 since JSON numbers are float64
			"items": []interface{}{"a", "b", "c"},
		}

		out, _, err := prg.Eval(map[string]interface{}{
			"data": original,
		})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		assert.Equal(t, "test", result["name"])
		assert.Equal(t, float64(42), result["value"])

		items := result["items"].([]interface{})
		assert.Equal(t, []interface{}{"a", "b", "c"}, items)
	})
}
