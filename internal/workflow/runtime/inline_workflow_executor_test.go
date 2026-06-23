package runtime

import (
	"errors"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

func TestBuildSubWorkflowInputs_PassthroughNegative(t *testing.T) {
	makeExecutor := func(parentInputs, subInputs map[string]interface{}, passthrough []string) *InlineWorkflowExecutor {
		evalResult := &reliantv1.Node{
			Id:   "child",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref:         &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
				Passthrough: passthrough,
			}},
		}
		return &InlineWorkflowExecutor{
			nodeID:            "child",
			node:              evalResult,
			evalResult:        evalResult,
			workflowInputs:    parentInputs,
			subWorkflowInputs: subInputs,
			subWorkflow:       &reliantv1.Workflow{Name: "agent"},
			logger:            &presetTestLogger{},
		}
	}

	t.Run("passthrough value overridden by explicit arg wins at runtime", func(t *testing.T) {
		// Both passthrough and args specify "model". Args must win.
		exec := makeExecutor(
			map[string]interface{}{"model": "from-parent", "temperature": 0.5},
			map[string]interface{}{"model": "from-explicit-args"},
			[]string{"model", "temperature"},
		)
		result := exec.buildSubWorkflowInputs()
		assert.Equal(t, "from-explicit-args", result["model"],
			"explicit args must override passthrough at runtime")
		assert.Equal(t, 0.5, result["temperature"],
			"non-overridden passthrough value should be forwarded")
	})

	t.Run("passthrough with no matching parent input does not crash", func(t *testing.T) {
		// Parent has no inputs at all. Passthrough should silently skip.
		exec := makeExecutor(
			map[string]interface{}{},
			map[string]interface{}{"existing": "value"},
			[]string{"missing_input_1", "missing_input_2"},
		)
		result := exec.buildSubWorkflowInputs()
		_, has1 := result["missing_input_1"]
		_, has2 := result["missing_input_2"]
		assert.False(t, has1, "missing passthrough should not appear in result")
		assert.False(t, has2, "missing passthrough should not appear in result")
		assert.Equal(t, "value", result["existing"], "existing args should be preserved")
	})

	t.Run("passthrough with nil parent inputs does not crash", func(t *testing.T) {
		exec := makeExecutor(
			nil, // nil parent inputs
			map[string]interface{}{},
			[]string{"model"},
		)
		// Should not panic
		result := exec.buildSubWorkflowInputs()
		_, hasModel := result["model"]
		assert.False(t, hasModel, "nil parent inputs should result in no forwarding")
	})

	t.Run("passthrough empty list is no-op", func(t *testing.T) {
		exec := makeExecutor(
			map[string]interface{}{"model": "claude-4", "temp": 0.7},
			map[string]interface{}{},
			[]string{}, // explicit empty list
		)
		result := exec.buildSubWorkflowInputs()
		_, hasModel := result["model"]
		_, hasTemp := result["temp"]
		assert.False(t, hasModel, "empty passthrough should not forward any inputs")
		assert.False(t, hasTemp, "empty passthrough should not forward any inputs")
	})
}

func TestBuildSubWorkflowInputs_Passthrough(t *testing.T) {
	// Helper to create an executor with minimal state for buildSubWorkflowInputs testing.
	makeExecutor := func(parentInputs, subInputs map[string]interface{}, passthrough []string) *InlineWorkflowExecutor {
		evalResult := &reliantv1.Node{
			Id:   "child",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Ref:         &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://agent"}},
				Passthrough: passthrough,
			}},
		}
		return &InlineWorkflowExecutor{
			nodeID:            "child",
			node:              evalResult,
			evalResult:        evalResult,
			workflowInputs:    parentInputs,
			subWorkflowInputs: subInputs,
			subWorkflow:       &reliantv1.Workflow{Name: "agent"},
			logger:            &presetTestLogger{},
		}
	}

	t.Run("passthrough forwards parent inputs to child", func(t *testing.T) {
		exec := makeExecutor(
			map[string]interface{}{"model": "claude-4", "temperature": 0.7, "other": "ignored"},
			map[string]interface{}{},
			[]string{"model", "temperature"},
		)
		result := exec.buildSubWorkflowInputs()
		assert.Equal(t, "claude-4", result["model"])
		assert.Equal(t, 0.7, result["temperature"])
		_, hasOther := result["other"]
		assert.False(t, hasOther, "non-passthrough input should not be forwarded")
	})

	t.Run("explicit args override passthrough", func(t *testing.T) {
		exec := makeExecutor(
			map[string]interface{}{"model": "claude-4"},
			map[string]interface{}{"model": "gpt-5"},
			[]string{"model"},
		)
		result := exec.buildSubWorkflowInputs()
		assert.Equal(t, "gpt-5", result["model"], "explicit args should override passthrough")
	})

	t.Run("passthrough name not in parent inputs is skipped", func(t *testing.T) {
		exec := makeExecutor(
			map[string]interface{}{"model": "claude-4"},
			map[string]interface{}{},
			[]string{"model", "nonexistent"},
		)
		result := exec.buildSubWorkflowInputs()
		assert.Equal(t, "claude-4", result["model"])
		_, hasNonexistent := result["nonexistent"]
		assert.False(t, hasNonexistent, "nonexistent passthrough name should be silently skipped")
	})

	t.Run("empty passthrough means no forwarding", func(t *testing.T) {
		exec := makeExecutor(
			map[string]interface{}{"model": "claude-4"},
			map[string]interface{}{},
			nil,
		)
		result := exec.buildSubWorkflowInputs()
		_, hasModel := result["model"]
		assert.False(t, hasModel, "no passthrough means no parent input forwarding")
	})
}
