// Copyright (c) 2025 Reliant Labs
package wfcel_test

import (
	"testing"

	"github.com/google/cel-go/cel"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/require"
)

func newSpawnTestCELEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv(
		cel.StdLib(),
		cel.Variable("presets", cel.ListType(cel.StringType)),
		wfcel.CelSpawnFunction(),
	)
	require.NoError(t, err)
	return env
}

func evalSpawnCEL(t *testing.T, env *cel.Env, expr string, vars map[string]interface{}) interface{} {
	t.Helper()
	ast, issues := env.Compile(expr)
	require.NoError(t, issues.Err(), "compile failed for: %s", expr)

	prg, err := env.Program(ast)
	require.NoError(t, err)

	result, _, err := prg.Eval(vars)
	require.NoError(t, err, "eval failed for: %s", expr)

	return result.Value()
}

func TestCelSpawn_MultiplePresets(t *testing.T) {
	env := newSpawnTestCELEnv(t)

	result := evalSpawnCEL(t, env, `spawn("builtin://agent", ["general", "researcher"])`, map[string]interface{}{})
	require.Equal(t, "spawn:builtin://agent(general,researcher)", result)
}

func TestCelSpawn_SinglePreset(t *testing.T) {
	env := newSpawnTestCELEnv(t)

	result := evalSpawnCEL(t, env, `spawn("builtin://auditing-agent", ["general"])`, map[string]interface{}{})
	require.Equal(t, "spawn:builtin://auditing-agent(general)", result)
}

func TestCelSpawn_EmptyPresets(t *testing.T) {
	env := newSpawnTestCELEnv(t)

	result := evalSpawnCEL(t, env, `spawn("builtin://agent", [])`, map[string]interface{}{})
	require.Equal(t, "", result)
}

func TestCelSpawn_EmptyPresetsFromVariable(t *testing.T) {
	env := newSpawnTestCELEnv(t)

	result := evalSpawnCEL(t, env, `spawn("builtin://agent", presets)`, map[string]interface{}{
		"presets": []string{},
	})
	require.Equal(t, "", result)
}

func TestCelSpawn_PresetsFromVariable(t *testing.T) {
	env := newSpawnTestCELEnv(t)

	result := evalSpawnCEL(t, env, `spawn("builtin://agent", presets)`, map[string]interface{}{
		"presets": []string{"general", "researcher", "code_reviewer"},
	})
	require.Equal(t, "spawn:builtin://agent(general,researcher,code_reviewer)", result)
}

func TestCelSpawn_RegisteredInCustomFunctions(t *testing.T) {
	// Verify spawn() is available when using the full CustomFunctions set
	env, err := cel.NewEnv(
		cel.StdLib(),
	)
	require.NoError(t, err)

	for _, opt := range wfcel.CustomFunctions() {
		env, err = env.Extend(opt)
		require.NoError(t, err)
	}

	ast, issues := env.Compile(`spawn("builtin://agent", ["general"])`)
	require.NoError(t, issues.Err())

	prg, err := env.Program(ast)
	require.NoError(t, err)

	result, _, err := prg.Eval(map[string]interface{}{})
	require.NoError(t, err)

	require.Equal(t, "spawn:builtin://agent(general)", result.Value())
}
