// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMalformedJSONError(t *testing.T) {
	t.Run("Error message format", func(t *testing.T) {
		err := &MalformedJSONError{
			ToolName: "test_tool",
			ToolID:   "call_123",
			Input:    `{"incomplete`,
		}

		errMsg := err.Error()
		assert.Contains(t, errMsg, "malformed JSON in tool input (transient)")
		assert.Contains(t, errMsg, "test_tool")
		assert.Contains(t, errMsg, "call_123")
		// The %q format adds quotes around the string
		assert.Contains(t, errMsg, "incomplete")
	})

	t.Run("IsMalformedJSONError helper", func(t *testing.T) {
		mjErr := &MalformedJSONError{
			ToolName: "test_tool",
			ToolID:   "call_123",
			Input:    `{"incomplete`,
		}

		assert.True(t, IsMalformedJSONError(mjErr))

		// String concatenation doesn't create an errors.As chain
		strConcatErr := errors.New("wrapped: " + mjErr.Error())
		assert.False(t, IsMalformedJSONError(strConcatErr))

		// Proper wrapping with Unwrap support
		properlyWrapped := &wrappedMJErr{cause: mjErr}
		assert.True(t, IsMalformedJSONError(properlyWrapped))

		// Regular errors
		assert.False(t, IsMalformedJSONError(errors.New("some other error")))
		assert.False(t, IsMalformedJSONError(nil))
	})
}

// wrappedMJErr wraps MalformedJSONError for testing errors.As
type wrappedMJErr struct {
	cause error
}

func (e *wrappedMJErr) Error() string { return "wrapped: " + e.cause.Error() }
func (e *wrappedMJErr) Unwrap() error { return e.cause }

func TestJSONValidation(t *testing.T) {
	// These tests verify the behavior of json.Valid which is used
	// in the toolCalls function to detect malformed JSON

	t.Run("Valid JSON patterns", func(t *testing.T) {
		validCases := []string{
			`{}`,
			`{"key": "value"}`,
			`{"nested": {"key": "value"}}`,
			`{"array": [1, 2, 3]}`,
			`{"number": 123.45}`,
			`{"bool": true}`,
			`{"null": null}`,
		}

		for _, tc := range validCases {
			assert.True(t, json.Valid([]byte(tc)), "Expected valid: %s", tc)
		}
	})

	t.Run("Invalid JSON patterns - typical streaming corruption", func(t *testing.T) {
		invalidCases := []struct {
			name  string
			input string
		}{
			{"truncated object", `{"key": "/test/fi`},
			{"truncated string", `{"key`},
			{"truncated value", `{"key":`},
			{"missing closing brace", `{"key": "value"`},
			{"invalid character in key", `{"key s`},
			{"completely invalid", `not json at all`},
			{"empty string", ``},
			{"just opening brace", `{`},
		}

		for _, tc := range invalidCases {
			t.Run(tc.name, func(t *testing.T) {
				assert.False(t, json.Valid([]byte(tc.input)), "Expected invalid: %s", tc.input)
			})
		}
	})
}

func TestMalformedJSONErrorTruncation(t *testing.T) {
	// Test that long inputs are truncated in error messages for logging

	t.Run("Short input not truncated", func(t *testing.T) {
		shortInput := `{"key": "short"}`
		err := &MalformedJSONError{
			ToolName: "test",
			ToolID:   "id",
			Input:    shortInput,
		}
		// Input is included in the error message (with %q escaping)
		assert.Contains(t, err.Error(), "key")
		assert.Contains(t, err.Error(), "short")
	})

	t.Run("Long input would need truncation in toolCalls", func(t *testing.T) {
		// The truncation happens in the toolCalls function, not in the error type
		// This test documents that long inputs should be truncated before creating the error
		longInput := string(make([]byte, 200))
		truncatedInput := longInput
		if len(truncatedInput) > 100 {
			truncatedInput = truncatedInput[:100] + "..."
		}

		err := &MalformedJSONError{
			ToolName: "test",
			ToolID:   "id",
			Input:    truncatedInput,
		}

		// The error message should contain the truncated input
		assert.LessOrEqual(t, len(err.Input), 103) // 100 + "..."
	})
}

func TestSDKJSONMarshalBehavior(t *testing.T) {
	// This test documents how json.RawMessage behaves during marshaling
	// to understand the error patterns we need to catch

	t.Run("Marshal validates RawMessage content", func(t *testing.T) {
		type TestBlock struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}

		// Valid JSON - should succeed
		validBlock := TestBlock{
			Name:  "test",
			Input: json.RawMessage(`{"valid": true}`),
		}
		data, err := json.Marshal(validBlock)
		require.NoError(t, err)
		assert.Contains(t, string(data), "valid")

		// Invalid JSON - should fail
		invalidBlock := TestBlock{
			Name:  "test",
			Input: json.RawMessage(`{"incomplete`),
		}
		_, err = json.Marshal(invalidBlock)
		require.Error(t, err)
		// This is the error pattern the workflow classifier keys on to decide a
		// streaming glitch is retryable. Only the stable half is asserted: the
		// failing type's name is NOT stable across Go releases (1.26 renders
		// "json.RawMessage", 1.27 "*jsontext.Value", since RawMessage became an
		// alias for jsontext.Value), which is exactly why the classifier now
		// matches the prefix instead of the type name.
		assert.Contains(t, err.Error(), "error calling MarshalJSON")
		assert.Contains(t, err.Error(), "unexpected end of JSON input")
	})
}
