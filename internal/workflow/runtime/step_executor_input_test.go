package runtime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOutput_MergesSnakeCaseDefaults(t *testing.T) {
	executor := &StepExecutor{}
	rawOutput := map[string]interface{}{
		"response_text": "hello",
		"token_count":   float64(42),
		"tool_calls":    []interface{}{},
	}

	normalized := executor.normalizeOutput(rawOutput, "CallLLM")

	assert.Equal(t, "hello", normalized["response_text"])
	assert.Equal(t, float64(42), normalized["token_count"])
	require.Contains(t, normalized, "tool_calls")
	require.Contains(t, normalized, "message")
	// message gets default nested keys (role, text) from withRequiredActivityOutputFields
	assert.Equal(t, map[string]interface{}{"role": "", "text": ""}, normalized["message"])
}

func TestNormalizeOutput_CallLLMAddsMissingToolCallsField(t *testing.T) {
	executor := &StepExecutor{}
	rawOutput := map[string]interface{}{
		"message":      map[string]interface{}{"role": "assistant", "text": "ok"},
		"responseText": "ok",
		"tokenCount":   float64(10),
		"thinking":     map[string]interface{}{},
	}

	normalized := executor.normalizeOutput(rawOutput, "CallLLM")

	require.Contains(t, normalized, "tool_calls")
	assert.Equal(t, []interface{}{}, normalized["tool_calls"])
}

func TestEnsureStepEventRoutable(t *testing.T) {
	t.Run("nil step event returns explicit error", func(t *testing.T) {
		err := EnsureStepEventRoutable(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "step event is nil")
	})

	t.Run("failed step event returns underlying step error", func(t *testing.T) {
		stepErr := errors.New("execute_tools failed")
		err := EnsureStepEventRoutable(&StepEvent{Error: stepErr})
		require.Error(t, err)
		assert.ErrorIs(t, err, stepErr)
	})

	t.Run("successful step event is routable", func(t *testing.T) {
		err := EnsureStepEventRoutable(&StepEvent{StepID: "execute_tools", Data: map[string]interface{}{"ok": true}})
		require.NoError(t, err)
	})
}
