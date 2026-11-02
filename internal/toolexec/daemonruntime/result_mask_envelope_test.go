package daemonruntime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A secret whose value is one of the shell envelope's field names must not
// rename the envelope: masked as text, {"exit_code":3} became {"***":3}, and
// the worker decoded it as exit 0. Only string values are masked, so the
// output is still masked and the verdict survives.
func TestMaskToolResultKeepsTheShellEnvelopeIntact(t *testing.T) {
	envelope, err := json.Marshal(map[string]any{
		"stdout":    "printed exit_code and stdout & more",
		"stderr":    "",
		"exit_code": 3,
	})
	require.NoError(t, err)
	result := &toolexec.ExecutionResult{Content: string(envelope), Metadata: `{"exit_code":3,"note":"stdout"}`, ErrorMessage: "exit_code"}
	maskToolResult(map[string]interface{}{toolexec.SecretMaskContextKey: []interface{}{"exit_code", "stdout"}}, result)

	var got struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &got), result.Content)
	assert.Equal(t, 3, got.ExitCode, "the verdict did not survive masking: %s", result.Content)
	assert.NotContains(t, got.Stdout, "exit_code")
	assert.NotContains(t, got.Stdout, "stdout")
	assert.Contains(t, got.Stdout, "& more", "masking re-encoded the output it did not need to touch")

	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Metadata), &meta), result.Metadata)
	assert.EqualValues(t, 3, meta["exit_code"])
	assert.NotEqual(t, "stdout", meta["note"])
	assert.NotContains(t, result.ErrorMessage, "exit_code")
}

// Text that is not one JSON document is masked as text.
func TestMaskPreservingJSONMasksNonJSONAsText(t *testing.T) {
	result := &toolexec.ExecutionResult{Content: `{"a":"s3cret"} trailing s3cret`, Metadata: "plain s3cret"}
	maskToolResult(map[string]interface{}{toolexec.SecretMaskContextKey: []interface{}{"s3cret"}}, result)
	assert.NotContains(t, result.Content, "s3cret")
	assert.NotContains(t, result.Metadata, "s3cret")
}

// HasMaskValues reads both forms the context map carries the mask in.
func TestHasMaskValues(t *testing.T) {
	assert.True(t, toolexec.HasMaskValues(map[string]interface{}{toolexec.SecretMaskContextKey: []interface{}{"x"}}))
	assert.True(t, toolexec.HasMaskValues(map[string]interface{}{toolexec.SecretMaskContextKey: []string{"x"}}))
	assert.False(t, toolexec.HasMaskValues(map[string]interface{}{toolexec.SecretMaskContextKey: []interface{}{}}))
	assert.False(t, toolexec.HasMaskValues(map[string]interface{}{}))
}
