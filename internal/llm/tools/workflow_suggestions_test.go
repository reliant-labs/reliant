// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWorkflowSuggestionsTool(t *testing.T) {
	t.Parallel()
	tool := NewGetWorkflowSuggestionsTool()
	typedTool := tool.(*ToolWrapper[GetWorkflowSuggestionsParams, ToolResponse])

	t.Run("tool metadata", func(t *testing.T) {
		assert.Equal(t, "get_workflow_suggestions", tool.Name())
		assert.Contains(t, tool.Description(), "static suggestions")
		assert.Contains(t, tool.Description(), "best practices")
	})

	t.Run("execute returns markdown", func(t *testing.T) {
		ctx := &rctx.ToolContext{}
		result, err := typedTool.tool.Execute(ctx, GetWorkflowSuggestionsParams{})
		require.NoError(t, err)

		content := result.Content
		require.NotEmpty(t, content)

		// Check for key sections
		assert.Contains(t, content, "# Workflow Design Suggestions")
		assert.Contains(t, content, "## Structure & Organization")
		assert.Contains(t, content, "## Edge Routing")
		assert.Contains(t, content, "## Joins")
		assert.Contains(t, content, "## Loops")
		assert.Contains(t, content, "## Testing with Scenarios")
		assert.Contains(t, content, "## Quick Reference")

		// Check for key recommendations
		assert.Contains(t, content, "Separate edge blocks for parallel")
		assert.Contains(t, content, "first-match-wins")
		assert.Contains(t, content, "node condition")
		assert.Contains(t, content, "Skipped nodes auto-satisfy join")
	})

	t.Run("static note is present", func(t *testing.T) {
		ctx := &rctx.ToolContext{}
		result, err := typedTool.tool.Execute(ctx, GetWorkflowSuggestionsParams{})
		require.NoError(t, err)

		content := result.Content
		assert.True(t, strings.Contains(content, "static suggestions"))
	})
}
