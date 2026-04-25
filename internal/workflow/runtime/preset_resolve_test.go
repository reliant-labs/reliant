package runtime

import (
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePresetName_Literal verifies literal preset names pass through untouched.
// Regression: the existing literal-preset-name behavior must not change.
func TestResolvePresetName_Literal(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{"foo": "bar"},
	}

	got, err := ResolvePresetName("claude-default", evalCtx)
	require.NoError(t, err)
	assert.Equal(t, "claude-default", got)
}

// TestResolvePresetName_LiteralEmpty verifies empty literal stays empty.
func TestResolvePresetName_LiteralEmpty(t *testing.T) {
	got, err := ResolvePresetName("", nil)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestResolvePresetName_FromInputs verifies templated preset name resolves via inputs.*.
func TestResolvePresetName_FromInputs(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{
			"implementer_preset": "codex-high",
		},
	}

	got, err := ResolvePresetName("{{inputs.implementer_preset}}", evalCtx)
	require.NoError(t, err)
	assert.Equal(t, "codex-high", got)
}

// TestResolvePresetName_PerIteration_Sequential simulates the sequential loop
// case: a preset name that differs per iteration via iter.iteration index.
// NOTE: Sequential loops do not expose iter.item; this test uses inputs keyed
// by iteration to model per-iteration variation.
func TestResolvePresetName_PerIteration_Sequential(t *testing.T) {
	iterations := []struct {
		iter     int
		expected string
	}{
		{0, "claude-default"},
		{1, "codex-high"},
		{2, "gemini-pro"},
	}

	inputs := map[string]interface{}{
		"presets_by_iter": []interface{}{
			"claude-default",
			"codex-high",
			"gemini-pro",
		},
	}

	for _, tc := range iterations {
		evalCtx := &wfcel.EdgeEvalContext{
			Inputs: inputs,
			Iter:   &model.IterContext{Iteration: tc.iter, Index: tc.iter},
		}

		got, err := ResolvePresetName("{{inputs.presets_by_iter[iter.iteration]}}", evalCtx)
		require.NoError(t, err, "iteration %d", tc.iter)
		assert.Equal(t, tc.expected, got, "iteration %d", tc.iter)
	}
}

// TestResolvePresetName_PerIteration_Parallel simulates the parallel loop
// case: preset name driven by iter.item.preset where item is a map.
func TestResolvePresetName_PerIteration_Parallel(t *testing.T) {
	items := []map[string]interface{}{
		{"name": "auth", "preset": "claude-default"},
		{"name": "billing", "preset": "codex-high"},
		{"name": "ui", "preset": "gemini-pro"},
	}

	for index, item := range items {
		evalCtx := &wfcel.EdgeEvalContext{
			Inputs: map[string]interface{}{},
			Iter: &model.IterContext{
				Iteration: index,
				Index:     index,
				Item:      item,
			},
		}

		got, err := ResolvePresetName("{{iter.item.preset}}", evalCtx)
		require.NoError(t, err, "index %d", index)
		assert.Equal(t, item["preset"], got, "index %d", index)
	}
}

// TestResolvePresetName_FailedEval ensures missing references return an error
// (so callers can log + skip rather than crash the loop).
func TestResolvePresetName_FailedEval(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{},
	}

	_, err := ResolvePresetName("{{inputs.missing_preset}}", evalCtx)
	require.Error(t, err, "missing input reference should produce an error")
}

// TestResolvePresetName_EmptyStringFromEval verifies that a template resolving
// to an empty string yields "" (caller will skip).
func TestResolvePresetName_EmptyStringFromEval(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{
			"preset_name": "",
		},
	}

	got, err := ResolvePresetName("{{inputs.preset_name}}", evalCtx)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestResolvePresetName_EmbeddedTemplateInterpolates verifies that a mixed
// string with {{...}} is interpolated into the surrounding literal text.
func TestResolvePresetName_EmbeddedTemplateInterpolates(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{
			"variant": "fast",
		},
	}

	got, err := ResolvePresetName("preset-{{inputs.variant}}", evalCtx)
	require.NoError(t, err)
	assert.Equal(t, "preset-fast", got)
}

// TestResolvePresetName_PureNonStringReturnsError verifies a pure template
// resolving to a non-string returns an error (callers will skip).
func TestResolvePresetName_PureNonStringReturnsError(t *testing.T) {
	evalCtx := &wfcel.EdgeEvalContext{
		Inputs: map[string]interface{}{
			"idx": int64(42),
		},
	}

	_, err := ResolvePresetName("{{inputs.idx}}", evalCtx)
	require.Error(t, err)
}
