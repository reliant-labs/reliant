// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"os"
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

	// Queries the live DuckDuckGo endpoint, so it is gated behind the same
	// explicit opt-in as TestWebSearchDemo. A short-mode skip is not enough:
	// CI runs the full suite, so this executed on every PR and failed on
	// DuckDuckGo's bot challenge for reasons unrelated to the change under
	// test. The offline coverage in websearch_mock_test.go is what actually
	// pins parsing, formatting and count limits.
	//
	//	RELIANT_TEST_NETWORK=1 go test ./internal/llm/tools/ -run TestWebSearchTool
	t.Run("ActualSearch", func(t *testing.T) {
		if os.Getenv("RELIANT_TEST_NETWORK") == "" {
			t.Skip("Skipping live-network test; set RELIANT_TEST_NETWORK=1 to run")
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
