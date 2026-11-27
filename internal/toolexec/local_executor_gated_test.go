// Copyright (c) 2026 Reliant Labs
package toolexec

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// A feature-gated tool is not found for a call whose capability set does not
// entitle it, or that carries none: the executor answers exactly as it does
// for a name outside the registry. No real tool is gated yet, so websearch is
// gated for the test (tools.GateToolForTest: not parallel).
func TestLocalToolExecutor_HiddenGatedToolIsNotFound(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)
	const unknown = "zz_no_such_tool"
	executor := NewLocalToolExecutor(tools.NewToolsFactory(&tools.ToolsOptions{}))
	run := func(ctx context.Context, name string) *ExecutionResult {
		return executor.ExecuteTool(ctx, name, `{}`, "call-1", 0, map[string]interface{}{"chat_id": "chat-1", "thread": "thread-1"})
	}

	for name, ctx := range map[string]context.Context{
		"no set":       context.Background(),
		"not entitled": tools.WithCapabilities(context.Background(), &tools.Capabilities{Permission: tools.PermissionMutating, Offered: []string{tools.ToolWebSearch}}),
	} {
		want, got := run(ctx, unknown), run(ctx, tools.ToolWebSearch)
		swap := func(s string) string { return strings.ReplaceAll(s, unknown, tools.ToolWebSearch) }
		assert.Equal(t, "TOOL_NOT_FOUND", got.ErrorCode, name)
		assert.Equal(t, want.ErrorCode, got.ErrorCode, name)
		assert.Equal(t, swap(want.Content), got.Content, name)
		assert.Equal(t, swap(want.ErrorMessage), got.ErrorMessage, name)
		assert.Equal(t, want.IsError, got.IsError, name)
		assert.Equal(t, want.Success, got.Success, name)
	}
}
