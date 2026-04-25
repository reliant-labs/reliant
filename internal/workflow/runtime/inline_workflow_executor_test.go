package runtime

import (
	"errors"
	"strings"
	"testing"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// presetTestLogger is a minimal log.Logger implementation capturing log calls for assertions.
type presetTestLogger struct {
	debugs []presetLogEntry
	infos  []presetLogEntry
	warns  []presetLogEntry
	errors []presetLogEntry
}

type presetLogEntry struct {
	msg     string
	keyvals []interface{}
}

func (l *presetTestLogger) Debug(msg string, keyvals ...interface{}) {
	l.debugs = append(l.debugs, presetLogEntry{msg, keyvals})
}
func (l *presetTestLogger) Info(msg string, keyvals ...interface{}) {
	l.infos = append(l.infos, presetLogEntry{msg, keyvals})
}
func (l *presetTestLogger) Warn(msg string, keyvals ...interface{}) {
	l.warns = append(l.warns, presetLogEntry{msg, keyvals})
}
func (l *presetTestLogger) Error(msg string, keyvals ...interface{}) {
	l.errors = append(l.errors, presetLogEntry{msg, keyvals})
}

// hasPresetLogMessage returns true if any entry's msg contains substr.
func hasPresetLogMessage(entries []presetLogEntry, substr string) bool {
	for _, e := range entries {
		if strings.Contains(e.msg, substr) {
			return true
		}
	}
	return false
}

func TestApplyPresets(t *testing.T) {
	t.Run("literal preset name still works", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{}
		presets := map[string]string{DefaultPresetGroup: "general"}
		evalCtx := &wfcel.EdgeEvalContext{Inputs: map[string]interface{}{}}

		loaderCalls := []string{}
		loader := func(name string) (map[string]interface{}, error) {
			loaderCalls = append(loaderCalls, name)
			return map[string]interface{}{"model": "claude-sonnet"}, nil
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"general"}, loaderCalls)
		assert.Equal(t, "claude-sonnet", subInputs["model"])
		// Literal should NOT emit "Resolved preset template" log.
		assert.False(t, hasPresetLogMessage(logger.infos, "Resolved preset template"))
	})

	t.Run("templated preset name resolves from inputs and merges params", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{}
		presets := map[string]string{DefaultPresetGroup: "{{inputs.preset_name}}"}
		evalCtx := &wfcel.EdgeEvalContext{
			Inputs: map[string]interface{}{"preset_name": "general"},
		}

		loaderCalls := []string{}
		loader := func(name string) (map[string]interface{}, error) {
			loaderCalls = append(loaderCalls, name)
			assert.Equal(t, "general", name, "loader should receive resolved preset name")
			return map[string]interface{}{"model": "claude-sonnet", "temp": 0.3}, nil
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"general"}, loaderCalls)
		assert.Equal(t, "claude-sonnet", subInputs["model"])
		assert.Equal(t, 0.3, subInputs["temp"])
		// Template resolution should have been logged.
		assert.True(t, hasPresetLogMessage(logger.infos, "Resolved preset template"),
			"expected 'Resolved preset template' info log")
	})

	t.Run("non-existent resolved preset logs warning and does not crash", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{"existing": "value"}
		presets := map[string]string{DefaultPresetGroup: "missing-preset"}
		evalCtx := &wfcel.EdgeEvalContext{Inputs: map[string]interface{}{}}

		loader := func(name string) (map[string]interface{}, error) {
			return nil, errors.New("preset not found: " + name)
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err, "applyPresets must not return error for missing preset")
		assert.Equal(t, "value", subInputs["existing"], "existing inputs preserved")
		assert.True(t, hasPresetLogMessage(logger.warns, "Failed to load preset"),
			"expected 'Failed to load preset' warning")
	})

	t.Run("empty string after template eval is skipped", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{}
		presets := map[string]string{DefaultPresetGroup: "{{inputs.preset_name}}"}
		evalCtx := &wfcel.EdgeEvalContext{
			Inputs: map[string]interface{}{"preset_name": ""},
		}

		loaderCalls := 0
		loader := func(name string) (map[string]interface{}, error) {
			loaderCalls++
			return map[string]interface{}{"should": "not-load"}, nil
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err)
		assert.Equal(t, 0, loaderCalls, "loader must not be invoked for empty resolved preset")
		assert.Empty(t, subInputs, "no params should be merged")
		assert.True(t, hasPresetLogMessage(logger.infos, "Skipping empty preset name"),
			"expected skip-log for empty resolved preset")
	})

	t.Run("template eval failure logs warning and skips", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{}
		// Reference an input that doesn't exist → CEL eval error.
		presets := map[string]string{DefaultPresetGroup: "{{inputs.nope}}"}
		evalCtx := &wfcel.EdgeEvalContext{Inputs: map[string]interface{}{}}

		loaderCalls := 0
		loader := func(name string) (map[string]interface{}, error) {
			loaderCalls++
			return nil, nil
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err, "template eval failure should not crash workflow")
		assert.Equal(t, 0, loaderCalls, "loader must not be called when resolution fails")
		assert.True(t, hasPresetLogMessage(logger.warns, "Failed to resolve preset template"),
			"expected resolution-failure warning")
	})

	t.Run("named group nests params under groupName", func(t *testing.T) {
		logger := &presetTestLogger{}
		subInputs := map[string]interface{}{}
		presets := map[string]string{"Implementer": "fast"}
		evalCtx := &wfcel.EdgeEvalContext{Inputs: map[string]interface{}{}}

		loader := func(name string) (map[string]interface{}, error) {
			return map[string]interface{}{"model": "haiku"}, nil
		}

		err := applyPresets(presets, subInputs, evalCtx, loader, logger, "node-1")
		require.NoError(t, err)
		group, ok := subInputs["Implementer"].(map[string]interface{})
		require.True(t, ok, "Implementer group should be a nested map")
		assert.Equal(t, "haiku", group["model"])
	})
}

func TestMergePresetParams(t *testing.T) {
	t.Run("default group flattens to top-level", func(t *testing.T) {
		subInputs := map[string]interface{}{"keep": "me"}
		mergePresetParams(subInputs, DefaultPresetGroup, map[string]interface{}{"a": 1, "b": 2})
		assert.Equal(t, "me", subInputs["keep"])
		assert.Equal(t, 1, subInputs["a"])
		assert.Equal(t, 2, subInputs["b"])
	})

	t.Run("named group creates nested map", func(t *testing.T) {
		subInputs := map[string]interface{}{}
		mergePresetParams(subInputs, "Grp", map[string]interface{}{"k": "v"})
		nested, ok := subInputs["Grp"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "v", nested["k"])
	})

	t.Run("named group reuses existing nested map", func(t *testing.T) {
		subInputs := map[string]interface{}{
			"Grp": map[string]interface{}{"existing": "x"},
		}
		mergePresetParams(subInputs, "Grp", map[string]interface{}{"added": "y"})
		nested := subInputs["Grp"].(map[string]interface{})
		assert.Equal(t, "x", nested["existing"])
		assert.Equal(t, "y", nested["added"])
	})
}
