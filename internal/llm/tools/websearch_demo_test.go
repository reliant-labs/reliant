// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebSearchDemo demonstrates the web search tool with actual output.
//
// This queries the live DuckDuckGo endpoint, so it is gated behind an explicit
// opt-in rather than run by default. DuckDuckGo answers a share of automated
// requests with a bot challenge — CI saw `status 202` on one query while the
// other two in this same test succeeded — which fails the run for a reason
// that has nothing to do with the change under test.
//
// Nothing is lost by gating it. The parsing, formatting, metadata and count
// limits are all covered offline in websearch_mock_test.go; what only this
// test can tell you is whether the live endpoint still answers us, and that is
// a question about DuckDuckGo rather than about a pull request.
//
//	RELIANT_TEST_NETWORK=1 go test ./internal/llm/tools/ -run TestWebSearchDemo
func TestWebSearchDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping demo test in short mode")
	}
	if os.Getenv("RELIANT_TEST_NETWORK") == "" {
		t.Skip("Skipping live-network test; set RELIANT_TEST_NETWORK=1 to run")
	}

	tool := NewWebSearchTool()
	typedTool := tool.(*ToolWrapper[WebSearchParams, ToolResponse])

	toolCtx := &rctx.ToolContext{
		Context: context.Background(),
	}

	t.Run("SearchForGoTesting", func(t *testing.T) {
		params := WebSearchParams{
			Query: "golang unit testing",
			Count: 5,
		}

		response, err := typedTool.tool.Execute(toolCtx, params)
		require.NoError(t, err, "Search should not return an error")
		require.False(t, response.IsError, "Response should not be an error")

		// Verify the response contains expected elements
		assert.Contains(t, response.Content, "# Web Search Results")
		assert.Contains(t, response.Content, "Query: **golang unit testing**")
		assert.Contains(t, response.Content, "Source: DuckDuckGo")

		// Verify we got results
		resultCount := strings.Count(response.Content, "## ")
		assert.Greater(t, resultCount, 0, "Should have at least one result")
		assert.LessOrEqual(t, resultCount, 5, "Should have at most 5 results")

		// Verify each result has the expected structure
		assert.Contains(t, response.Content, "**URL:**")

		// Parse metadata
		require.NotEmpty(t, response.Metadata, "Metadata should not be empty")
		var metadata WebSearchResponseMetadata
		err = json.Unmarshal([]byte(response.Metadata), &metadata)
		require.NoError(t, err, "Should be able to parse metadata")

		assert.Equal(t, "golang unit testing", metadata.Query)
		assert.Equal(t, "DuckDuckGo", metadata.Source)
		assert.Greater(t, metadata.NumberOfResults, 0, "Should have at least one result")
		assert.LessOrEqual(t, metadata.NumberOfResults, 5, "Should have at most 5 results")

		// Print the first result for demonstration
		contentPreview := response.Content
		if len(contentPreview) > 500 {
			contentPreview = contentPreview[:500] + "...\n[truncated]"
		}
		t.Logf("\n=== Sample Search Result ===\n%s\n", contentPreview)
	})

	t.Run("SearchWithSiteFilter", func(t *testing.T) {
		params := WebSearchParams{
			Query: "site:pkg.go.dev testing",
			Count: 3,
		}

		response, err := typedTool.tool.Execute(toolCtx, params)
		require.NoError(t, err)
		require.False(t, response.IsError)

		// Verify the query is preserved
		assert.Contains(t, response.Content, "site:pkg.go.dev testing")

		// Verify we got some results
		resultCount := strings.Count(response.Content, "## ")
		assert.Greater(t, resultCount, 0, "Should have at least one result")
	})

	t.Run("SearchForErrorMessage", func(t *testing.T) {
		params := WebSearchParams{
			Query: "golang context deadline exceeded",
			Count: 3,
		}

		response, err := typedTool.tool.Execute(toolCtx, params)
		require.NoError(t, err)
		require.False(t, response.IsError)

		assert.Contains(t, response.Content, "golang context deadline exceeded")
		resultCount := strings.Count(response.Content, "## ")
		assert.Greater(t, resultCount, 0, "Should have at least one result")
	})
}

// TestWebSearchToolSchema verifies the tool schema is properly generated
func TestWebSearchToolSchema(t *testing.T) {
	tool := NewWebSearchTool()

	// Verify the tool has a schema
	schema := tool.ParamSchema()
	require.NotNil(t, schema, "Tool should have a parameter schema")

	// Convert to JSON to inspect
	schemaJSON, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(t, err, "Should be able to marshal schema to JSON")

	schemaStr := string(schemaJSON)

	// Verify required fields are in the schema
	assert.Contains(t, schemaStr, "query", "Schema should include 'query' field")
	assert.Contains(t, schemaStr, "count", "Schema should include 'count' field")
	assert.Contains(t, schemaStr, "required", "Schema should mark required fields")

	t.Logf("\n=== Tool Schema ===\n%s\n", schemaStr)
}
