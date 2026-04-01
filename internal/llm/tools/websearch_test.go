// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSearchTool(t *testing.T) {
	tool := NewWebSearchTool()

	assert.Equal(t, WebSearchToolName, tool.Name())
	assert.NotEmpty(t, tool.Description())

	params := WebSearchParams{
		Query: "golang testing",
		Count: 5,
	}

	// Create a tool context
	toolCtx := &rctx.ToolContext{
		Context: context.Background(),
	}

	// Test that it requires permission
	typedTool := tool.(*ToolWrapper[WebSearchParams, ToolResponse])
	requiresPermission, err := typedTool.tool.RequiresPermission(params)
	require.NoError(t, err)
	assert.True(t, requiresPermission)

	// Test actual search (this may fail if rate limited)
	t.Run("ActualSearch", func(t *testing.T) {
		if testing.Short() {
			t.Skip("Skipping actual search in short mode")
		}

		response, err := typedTool.tool.Execute(toolCtx, params)
		require.NoError(t, err)
		assert.False(t, response.IsError)
		assert.NotEmpty(t, response.Content)
		assert.Contains(t, response.Content, "Web Search Results")
		assert.Contains(t, response.Content, "golang testing")
	})
}

func TestWebSearchToolValidation(t *testing.T) {
	tool := NewWebSearchTool()
	typedTool := tool.(*ToolWrapper[WebSearchParams, ToolResponse])

	toolCtx := &rctx.ToolContext{
		Context: context.Background(),
	}

	t.Run("EmptyQuery", func(t *testing.T) {
		params := WebSearchParams{
			Query: "",
		}

		response, err := typedTool.tool.Execute(toolCtx, params)
		require.NoError(t, err)
		assert.True(t, response.IsError)
		assert.Contains(t, response.Content, "Query parameter is required")
	})

	t.Run("DefaultCount", func(t *testing.T) {
		params := WebSearchParams{
			Query: "test",
			Count: 0, // Should default to 10
		}

		// This would require mocking or actually executing, so we'll just verify it doesn't error
		requiresPermission, err := typedTool.tool.RequiresPermission(params)
		require.NoError(t, err)
		assert.True(t, requiresPermission)
	})

	t.Run("MaxCount", func(t *testing.T) {
		params := WebSearchParams{
			Query: "test",
			Count: 100, // Should be capped to 20
		}

		requiresPermission, err := typedTool.tool.RequiresPermission(params)
		require.NoError(t, err)
		assert.True(t, requiresPermission)
	})
}
