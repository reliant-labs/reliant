package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateOutputsMap(t *testing.T) {
	t.Run("basic field access via outputs namespace", func(t *testing.T) {
		outputsMap := map[string]string{
			"message": "outputs.response_text",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"response_text": "hello",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "hello", result["message"])
	})

	t.Run("template syntax unwraps correctly", func(t *testing.T) {
		outputsMap := map[string]string{
			"message": "{{ outputs.response_text }}",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"response_text": "from template",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "from template", result["message"])
	})

	t.Run("template syntax with trailing newline from YAML", func(t *testing.T) {
		outputsMap := map[string]string{
			"message": "{{ outputs.response_text }}\n",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"response_text": "trimmed",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "trimmed", result["message"])
	})

	t.Run("multiple outputs all evaluate", func(t *testing.T) {
		outputsMap := map[string]string{
			"name":  "outputs.user_name",
			"email": "outputs.user_email",
			"age":   "outputs.user_age",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"user_name":  "Alice",
				"user_email": "alice@example.com",
				"user_age":   int64(30),
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "Alice", result["name"])
		assert.Equal(t, "alice@example.com", result["email"])
		assert.Equal(t, int64(30), result["age"])
	})

	t.Run("empty map returns empty map", func(t *testing.T) {
		outputsMap := map[string]string{}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{"x": "y"},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("missing field sets nil without fatal error", func(t *testing.T) {
		outputsMap := map[string]string{
			"missing": "outputs.nonexistent_field",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"response_text": "hello",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		// The function sets nil on evaluation error rather than failing
		assert.Nil(t, result["missing"])
		_, exists := result["missing"]
		assert.True(t, exists, "key should exist in result even if nil")
	})

	t.Run("unrecognized top-level field sets nil", func(t *testing.T) {
		// CEL only recognizes specific namespaces (inputs, outputs, nodes, etc.)
		// Arbitrary top-level keys are not valid CEL identifiers, so evaluation
		// fails gracefully and the output is set to nil.
		outputsMap := map[string]string{
			"wf": "selected_workflow",
		}
		celContext := map[string]interface{}{
			"selected_workflow": "my-workflow",
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		// Fails CEL compilation → nil
		assert.Nil(t, result["wf"])
	})

	t.Run("mixed template and bare expressions", func(t *testing.T) {
		outputsMap := map[string]string{
			"bare":     "outputs.a",
			"template": "{{ outputs.b }}",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"a": "value_a",
				"b": "value_b",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "value_a", result["bare"])
		assert.Equal(t, "value_b", result["template"])
	})

	t.Run("nested map access", func(t *testing.T) {
		outputsMap := map[string]string{
			"deep": "outputs.nested.inner",
		}
		celContext := map[string]interface{}{
			"outputs": map[string]interface{}{
				"nested": map[string]interface{}{
					"inner": "deep_value",
				},
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "deep_value", result["deep"])
	})

	t.Run("router context accesses child outputs", func(t *testing.T) {
		// Simulates the router output context shape from RouterExecutor.Execute.
		// Only the `outputs` namespace is a valid CEL identifier; the fixed
		// fields (selected_workflow, reasoning, etc.) are plain strings that
		// CEL cannot resolve.
		outputsMap := map[string]string{
			"response": "outputs.response_text",
			"summary":  "outputs.summary",
		}
		celContext := map[string]interface{}{
			"selected_workflow": "builtin://agent",
			"selected_preset":   "general",
			"prompt":            "rewrite this",
			"reasoning":         "best fit for task",
			"outputs": map[string]interface{}{
				"response_text": "hello world",
				"summary":       "a brief summary",
			},
		}

		result, err := evaluateOutputsMap(outputsMap, celContext, nilLogger{})
		require.NoError(t, err)
		assert.Equal(t, "hello world", result["response"])
		assert.Equal(t, "a brief summary", result["summary"])
	})
}
