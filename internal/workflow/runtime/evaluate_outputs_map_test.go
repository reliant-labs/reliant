package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A router's declared outputs are evaluated against wfcel.RouterOutputContext:
// `outputs` is the selected workflow's outputs, plus inputs and workflow.
func TestEvaluateRouterOutputs(t *testing.T) {
	t.Parallel()
	scope := &wfcel.RouterOutputContext{
		Inputs:   map[string]interface{}{"mode": "auto"},
		Workflow: &model.WorkflowContext{ID: "wf-1", Name: "router-wf"},
	}
	child := map[string]interface{}{
		"response_text": "hello",
		"nested":        map[string]interface{}{"inner": "deep_value"},
		"age":           int64(30),
	}

	t.Run("bare, templated and YAML-folded expressions", func(t *testing.T) {
		result, err := evaluateRouterOutputs(map[string]string{
			"bare":     "outputs.response_text",
			"template": "{{ outputs.response_text }}",
			"folded":   "{{ outputs.response_text }}\n",
			"deep":     "outputs.nested.inner",
			"age":      "outputs.age",
		}, child, scope)
		require.NoError(t, err)
		assert.Equal(t, "hello", result["bare"])
		assert.Equal(t, "hello", result["template"])
		assert.Equal(t, "hello", result["folded"])
		assert.Equal(t, "deep_value", result["deep"])
		assert.Equal(t, int64(30), result["age"])
	})

	t.Run("inputs and workflow are in scope", func(t *testing.T) {
		result, err := evaluateRouterOutputs(map[string]string{
			"mode": "inputs.mode",
			"wf":   "{{workflow.id}}",
		}, child, scope)
		require.NoError(t, err)
		assert.Equal(t, "auto", result["mode"])
		assert.Equal(t, "wf-1", result["wf"])
	})

	t.Run("empty declaration yields empty result", func(t *testing.T) {
		result, err := evaluateRouterOutputs(map[string]string{}, child, scope)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	// Previously the failure was logged and the output silently set to nil.
	t.Run("a failing expression fails the evaluation", func(t *testing.T) {
		_, err := evaluateRouterOutputs(map[string]string{
			"missing": "outputs.nonexistent_field",
		}, child, scope)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `output "missing"`)
	})

	t.Run("names outside the declared namespaces do not compile", func(t *testing.T) {
		_, err := evaluateRouterOutputs(map[string]string{"wf": "selected_workflow"}, child, scope)
		require.Error(t, err)
	})
}
