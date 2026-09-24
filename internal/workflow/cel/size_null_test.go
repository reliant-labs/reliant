package wfcel

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// size(null) is 0 at every evaluation site. The sites build their environments
// from different namespace sets, so each one is exercised rather than assumed.
func TestSizeOfNullIsZeroAtEverySite(t *testing.T) {
	t.Parallel()

	nullData := map[string]interface{}{"items": nil, "text": nil}

	t.Run("template", func(t *testing.T) {
		t.Parallel()
		ctx := &NodeResolutionContext{Nodes: map[string]interface{}{"a": nullData}, Inputs: map[string]interface{}{}}
		got, err := EvaluateTemplate("{{size(nodes.a.items)}}", ctx)
		require.NoError(t, err)
		assert.EqualValues(t, 0, got)
	})

	t.Run("node condition", func(t *testing.T) {
		t.Parallel()
		ctx := &EdgeEvalContext{Nodes: map[string]interface{}{"a": nullData}, Inputs: map[string]interface{}{}}
		got, err := EvaluateBool("size(nodes.a.items) == 0 && nodes.a.text.size() == 0", ctx)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("loop while", func(t *testing.T) {
		t.Parallel()
		ctx := &LoopEvalContext{Iter: &model.IterContext{}, Outputs: nullData, Inputs: map[string]interface{}{}}
		got, err := EvaluateBool("size(outputs.items) > 0", ctx)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("save_message condition", func(t *testing.T) {
		t.Parallel()
		ctx := &PostActivityContext{Output: map[string]interface{}{"response_data": nil}, Inputs: map[string]interface{}{}}
		got, err := EvaluateBool("size(output.response_data) == 0", ctx)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("literal null", func(t *testing.T) {
		t.Parallel()
		got, err := EvaluateValue("size(null)", &EdgeEvalContext{})
		require.NoError(t, err)
		assert.EqualValues(t, 0, got)
	})
}

// Every preset environment — including loop while, which historically had no
// custom functions — compiles size(null) the same way.
func TestSizeOfNullCompilesInEveryPresetEnv(t *testing.T) {
	t.Parallel()
	presets := map[string]CELEnvConfig{
		"default":      DefaultCELEnvConfig(),
		"save_message": SaveMessageCELEnvConfig(),
		"loop_while":   LoopWhileCELEnvConfig(),
		"template":     TemplateResolutionCELEnvConfig(),
		"edge":         EdgeConditionCELEnvConfig(),
	}
	for name, config := range presets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env, err := NewEnv(config)
			require.NoError(t, err)
			ast, issues := env.Compile("size(null) == 0 && null.size() == 0")
			require.NoError(t, issues.Err())
			prg, err := env.Program(ast)
			require.NoError(t, err)
			out, _, err := prg.Eval(map[string]interface{}{})
			require.NoError(t, err)
			assert.Equal(t, true, out.Value())
		})
	}
}

// Only size() is forgiving. Every other size() keeps its meaning, and the rest
// of null's arithmetic stays an error.
func TestSizeSemanticsOtherwiseUnchanged(t *testing.T) {
	t.Parallel()
	ctx := &EdgeEvalContext{
		Nodes:  map[string]interface{}{"a": map[string]interface{}{"s": nil, "list": []interface{}{1, 2, 3}, "m": map[string]interface{}{"k": 1}}},
		Inputs: map[string]interface{}{},
	}

	for expr, want := range map[string]int64{
		"size([])":            0,
		"size('ab')":          2,
		"'ab'.size()":         2,
		"size(b'abc')":        3,
		"size(nodes.a.list)":  3,
		"nodes.a.list.size()": 3,
		"size(nodes.a.m)":     1,
		"size({})":            0,
	} {
		got, err := EvaluateValue(expr, ctx)
		require.NoError(t, err, expr)
		assert.EqualValues(t, want, got, expr)
	}

	for _, expr := range []string{
		"null + 'x'",           // compile-time: no overload
		"nodes.a.s + 'x'",      // run-time: null operand
		"nodes.a.s > 1",        // run-time: null comparison
		"size(1)",              // size of an int is still a type error
		"size(nodes.a.nope)",   // an ABSENT key is not null: no such key
		"size(inputs.missing)", // ditto for inputs
	} {
		_, err := EvaluateValue(expr, ctx)
		assert.Error(t, err, expr)
	}
}
