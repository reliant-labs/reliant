// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/google/cel-go/cel"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCelParseJsonFunction(t *testing.T) {
	t.Run("parses simple object", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson('{"name": "test", "value": 42}')`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		assert.Equal(t, "test", result["name"])
		assert.Equal(t, float64(42), result["value"]) // JSON numbers are float64
	})

	t.Run("parses array of objects (tool_results format)", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `[
			{"tool_call_id": "abc123", "name": "view", "content": "file contents here", "is_error": false},
			{"tool_call_id": "def456", "name": "bash", "content": "command output", "is_error": false}
		]`

		ast, issues := env.Compile(`parseJson(input)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		result := out.Value().([]interface{})
		require.Len(t, result, 2)

		first := result[0].(map[string]interface{})
		assert.Equal(t, "abc123", first["tool_call_id"])
		assert.Equal(t, "view", first["name"])
		assert.Equal(t, "file contents here", first["content"])
		assert.Equal(t, false, first["is_error"])

		second := result[1].(map[string]interface{})
		assert.Equal(t, "def456", second["tool_call_id"])
		assert.Equal(t, "bash", second["name"])
	})

	t.Run("parses simple array", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson('[1, 2, 3]')`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{})
		require.NoError(t, err)

		result := out.Value().([]interface{})
		require.Len(t, result, 3)
		assert.Equal(t, float64(1), result[0])
		assert.Equal(t, float64(2), result[1])
		assert.Equal(t, float64(3), result[2])
	})

	t.Run("parses nested objects", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `{"outer": {"inner": {"value": "deep"}}}`

		ast, issues := env.Compile(`parseJson(input)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		outer := result["outer"].(map[string]interface{})
		inner := outer["inner"].(map[string]interface{})
		assert.Equal(t, "deep", inner["value"])
	})

	t.Run("access parsed array element", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `[{"id": "first"}, {"id": "second"}]`

		ast, issues := env.Compile(`parseJson(input)[0].id`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		assert.Equal(t, "first", out.Value())
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson('not valid json')`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		_, _, err = prg.Eval(map[string]interface{}{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parseJson() failed to parse JSON")
	})

	t.Run("rejects non-string input at compile time", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		// CEL catches type mismatch at compile time
		_, issues := env.Compile(`parseJson(42)`)
		require.NotNil(t, issues.Err())
		assert.Contains(t, issues.Err().Error(), "no matching overload")
	})

	t.Run("handles empty object", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson('{}')`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		assert.Empty(t, result)
	})

	t.Run("handles empty array", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib()}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		ast, issues := env.Compile(`parseJson('[]')`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{})
		require.NoError(t, err)

		result := out.Value().([]interface{})
		assert.Empty(t, result)
	})

	t.Run("handles boolean and null values", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `{"flag": true, "empty": null, "notFlag": false}`

		ast, issues := env.Compile(`parseJson(input)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		assert.Equal(t, true, result["flag"])
		assert.Equal(t, false, result["notFlag"])
		assert.Nil(t, result["empty"])
	})

	t.Run("handles string with escaped quotes", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `{"message": "He said \"hello\""}`

		ast, issues := env.Compile(`parseJson(input)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		result := out.Value().(map[string]interface{})
		assert.Equal(t, `He said "hello"`, result["message"])
	})
}

func TestParseJsonWithMemberFunctions(t *testing.T) {
	// Note: last() and first() are member overloads (myList.last(), not last(myList))
	t.Run("parseJson result with array indexing", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `[{"id": 1}, {"id": 2}, {"id": 3}]`

		// Access last element using array indexing
		ast, issues := env.Compile(`parseJson(input)[2].id`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		// JSON numbers are float64
		assert.Equal(t, float64(3), out.Value())
	})

	t.Run("parseJson with size function", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `[{"id": 1}, {"id": 2}, {"id": 3}]`

		ast, issues := env.Compile(`size(parseJson(input))`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		assert.Equal(t, int64(3), out.Value())
	})

	t.Run("parseJson with has function", func(t *testing.T) {
		env, err := cel.NewEnv(
			append([]cel.EnvOption{cel.StdLib(), cel.Variable("input", cel.StringType)}, wfcel.CustomFunctions()...)...,
		)
		require.NoError(t, err)

		jsonInput := `{"name": "test", "value": 42}`

		ast, issues := env.Compile(`has(parseJson(input).name)`)
		require.Nil(t, issues.Err())

		prg, err := env.Program(ast)
		require.NoError(t, err)

		out, _, err := prg.Eval(map[string]interface{}{"input": jsonInput})
		require.NoError(t, err)

		assert.Equal(t, true, out.Value())
	})
}
