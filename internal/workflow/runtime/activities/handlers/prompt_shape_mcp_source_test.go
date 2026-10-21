// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestPromptShape_LogFieldsNameTheMCPToolsSource(t *testing.T) {
	shape := shapeOf([]string{"s"}, baseHistory())
	shape.mcpToolsSource = tools.MCPToolsStale
	out := kv(shape.logFields(shapeDiff{}))
	require.Contains(t, out, "mcpToolsSource=stale-last-known-good")
}
