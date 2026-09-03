// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

// ============================================================================
// NUMERIC TYPE PRESERVATION TESTS
// ============================================================================
//
// These tests ensure that numeric values in workflow configs are preserved
// as the correct type (int, float) and not accidentally converted to strings.
//
// Background:
// - YAML parses "0" (quoted) as string, 0 (unquoted) as int
// - JSON unmarshaling into Go structs requires correct types
// - Temporal activity inputs must have correct types for deserialization
// ============================================================================

// TestYAMLNumericParsing verifies YAML parsing behavior for numeric values
func TestYAMLNumericParsing(t *testing.T) {
	t.Parallel()
	t.Run("unquoted_numbers_are_parsed_as_integers", func(t *testing.T) {
		yamlContent := `
input_tokens: 0
output_tokens: 100
cache_creation_tokens: 50
`
		var result map[string]interface{}
		err := yamlv3.Unmarshal([]byte(yamlContent), &result)
		require.NoError(t, err)

		// YAML parses integers as int
		assert.IsType(t, int(0), result["input_tokens"], "unquoted 0 should be int")
		assert.IsType(t, int(0), result["output_tokens"], "unquoted 100 should be int")
		assert.IsType(t, int(0), result["cache_creation_tokens"], "unquoted 50 should be int")
	})

	t.Run("quoted_numbers_are_parsed_as_strings", func(t *testing.T) {
		yamlContent := `
input_tokens: "0"
output_tokens: "100"
`
		var result map[string]interface{}
		err := yamlv3.Unmarshal([]byte(yamlContent), &result)
		require.NoError(t, err)

		// Quoted values are strings
		assert.IsType(t, "", result["input_tokens"], "quoted 0 should be string")
		assert.IsType(t, "", result["output_tokens"], "quoted 100 should be string")
	})

	t.Run("unquoted_null_is_parsed_as_nil", func(t *testing.T) {
		yamlContent := `
tool_calls: null
tool_results: null
attachments: ~
`
		var result map[string]interface{}
		err := yamlv3.Unmarshal([]byte(yamlContent), &result)
		require.NoError(t, err)

		// Unquoted null and ~ are parsed as nil
		assert.Nil(t, result["tool_calls"], "unquoted null should be nil")
		assert.Nil(t, result["tool_results"], "unquoted null should be nil")
		assert.Nil(t, result["attachments"], "tilde ~ should be nil")
	})

	t.Run("quoted_null_is_parsed_as_string", func(t *testing.T) {
		// This is the BAD pattern that causes Temporal deserialization errors
		yamlContent := `
tool_calls: "null"
`
		var result map[string]interface{}
		err := yamlv3.Unmarshal([]byte(yamlContent), &result)
		require.NoError(t, err)

		// Quoted "null" is a string, NOT nil - this causes errors!
		assert.IsType(t, "", result["tool_calls"], "quoted null should be string (BAD)")
		assert.Equal(t, "null", result["tool_calls"], "quoted null should be literal string 'null'")
	})
}

// SaveMessageInputTest is a minimal struct to test JSON deserialization behavior
// without importing the handlers package (to avoid import cycles)
type SaveMessageInputTest struct {
	ChatID              string `json:"chat_id"`
	Thread              string `json:"thread"`
	Role                string `json:"role"`
	Content             string `json:"content,omitempty"`
	InputTokens         int    `json:"input_tokens,omitempty"`
	OutputTokens        int    `json:"output_tokens,omitempty"`
	CacheCreationTokens int    `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int    `json:"cache_read_tokens,omitempty"`
}

// TestSaveMessageInputNumericTypes verifies that SaveMessageInput correctly
// deserializes numeric fields from workflow config
func TestSaveMessageInputNumericTypes(t *testing.T) {
	t.Parallel()
	t.Run("integer_values_deserialize_correctly", func(t *testing.T) {
		// Simulate config with proper integer types
		config := map[string]interface{}{
			"chat_id":               "test-chat",
			"thread":                "main",
			"role":                  "assistant",
			"content":               "Hello",
			"input_tokens":          100,
			"output_tokens":         50,
			"cache_creation_tokens": 25,
			"cache_read_tokens":     10,
		}

		// Marshal to JSON (simulates workflow passing data)
		jsonData, err := json.Marshal(config)
		require.NoError(t, err)

		// Unmarshal to SaveMessageInputTest
		var input SaveMessageInputTest
		err = json.Unmarshal(jsonData, &input)
		require.NoError(t, err, "should deserialize correctly with int values")

		assert.Equal(t, 100, input.InputTokens)
		assert.Equal(t, 50, input.OutputTokens)
		assert.Equal(t, 25, input.CacheCreationTokens)
		assert.Equal(t, 10, input.CacheReadTokens)
	})

	t.Run("string_values_fail_to_deserialize", func(t *testing.T) {
		// Simulate config with string types (the bug)
		config := map[string]interface{}{
			"chat_id":               "test-chat",
			"thread":                "main",
			"role":                  "assistant",
			"content":               "Hello",
			"input_tokens":          "100", // STRING - will fail
			"output_tokens":         "50",
			"cache_creation_tokens": "25",
			"cache_read_tokens":     "10",
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(config)
		require.NoError(t, err)

		// Unmarshal to SaveMessageInputTest - should fail
		var input SaveMessageInputTest
		err = json.Unmarshal(jsonData, &input)
		assert.Error(t, err, "should fail when numeric fields are strings")
		assert.Contains(t, err.Error(), "cannot unmarshal string")
	})

	t.Run("float64_values_from_json_deserialize_correctly", func(t *testing.T) {
		// JSON numbers are float64 by default when unmarshaled to interface{}
		config := map[string]interface{}{
			"chat_id":               "test-chat",
			"thread":                "main",
			"role":                  "assistant",
			"content":               "Hello",
			"input_tokens":          float64(100),
			"output_tokens":         float64(50),
			"cache_creation_tokens": float64(25),
			"cache_read_tokens":     float64(10),
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(config)
		require.NoError(t, err)

		// Unmarshal to SaveMessageInputTest - float64 can convert to int
		var input SaveMessageInputTest
		err = json.Unmarshal(jsonData, &input)
		require.NoError(t, err, "float64 should convert to int during unmarshal")

		assert.Equal(t, 100, input.InputTokens)
		assert.Equal(t, 50, input.OutputTokens)
	})
}
