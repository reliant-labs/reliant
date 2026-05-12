// Copyright (c) 2025 Reliant Labs
package message

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test marshalling of tool results
func TestMarshallParts_ToolResults(t *testing.T) {
	toolResults := []ContentPart{
		ToolResult{
			ToolCallID: "tool1",
			Content:    "result 1",
			IsError:    false,
		},
		ToolResult{
			ToolCallID: "tool2",
			Content:    "error message",
			IsError:    true,
		},
	}

	marshalled, err := MarshallParts(toolResults)
	require.NoError(t, err)
	assert.NotEmpty(t, marshalled)
	assert.NotEqual(t, "[]", string(marshalled))
	assert.NotEqual(t, "null", string(marshalled))

	// Verify it can be unmarshalled
	unmarshalled, err := UnmarshallParts(marshalled)
	require.NoError(t, err)
	assert.Len(t, unmarshalled, 2)

	// Check the content
	for i, part := range unmarshalled {
		tr, ok := part.(ToolResult)
		assert.True(t, ok, "Part %d should be a ToolResult", i)
		if i == 0 {
			assert.Equal(t, "tool1", tr.ToolCallID)
			assert.Equal(t, "result 1", tr.Content)
			assert.False(t, tr.IsError)
		} else {
			assert.Equal(t, "tool2", tr.ToolCallID)
			assert.Equal(t, "error message", tr.Content)
			assert.True(t, tr.IsError)
		}
	}
}

// Test edge cases
func TestToolResult_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		result    ToolResult
		expectErr bool
	}{
		{
			name: "empty content",
			result: ToolResult{
				ToolCallID: "tool1",
				Content:    "",
				IsError:    false,
			},
			expectErr: false, // Empty content is technically valid
		},
		{
			name: "very long content",
			result: ToolResult{
				ToolCallID: "tool1",
				Content:    string(make([]byte, 10000)),
				IsError:    false,
			},
			expectErr: false,
		},
		{
			name: "special characters in content",
			result: ToolResult{
				ToolCallID: "tool1",
				Content:    `{"test": "value", "special": "chars: \n\t\r"}`,
				IsError:    false,
			},
			expectErr: false,
		},
		{
			name: "error result",
			result: ToolResult{
				ToolCallID: "tool1",
				Content:    "Error: Command failed",
				IsError:    true,
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal the tool result
			parts := []ContentPart{tt.result}
			marshalled, err := MarshallParts(parts)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, marshalled)

				// Verify it can be unmarshalled
				unmarshalled, err := UnmarshallParts(marshalled)
				assert.NoError(t, err)
				assert.Len(t, unmarshalled, 1)

				// Verify content is preserved
				tr, ok := unmarshalled[0].(ToolResult)
				assert.True(t, ok)
				assert.Equal(t, tt.result.ToolCallID, tr.ToolCallID)
				assert.Equal(t, tt.result.Content, tr.Content)
				assert.Equal(t, tt.result.IsError, tr.IsError)
			}
		})
	}
}
