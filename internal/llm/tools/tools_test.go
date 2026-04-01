// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// mockTool implements Tool interface for testing
type mockTool struct {
	name string
}

func (m *mockTool) Name() string {
	return m.name
}

func (m *mockTool) Description() string {
	return "mock tool for testing"
}

func (m *mockTool) ParamSchema() *jsonschema.Schema {
	return nil
}

func (m *mockTool) RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error) {
	return false, nil
}

func (m *mockTool) Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	return NewTextResponse("mock response"), nil
}

// mockReadOnlyTool implements ReadOnlyTool interface
type mockReadOnlyTool struct {
	mockTool
	readOnly bool
}

func (m *mockReadOnlyTool) IsReadOnly() bool {
	return m.readOnly
}

func TestIsToolReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		tool     Tool
		expected bool
	}{
		// Explicit read-only declaration
		{
			name:     "explicit read-only true",
			tool:     &mockReadOnlyTool{mockTool: mockTool{name: "some_tool"}, readOnly: true},
			expected: true,
		},
		{
			name:     "explicit read-only false",
			tool:     &mockReadOnlyTool{mockTool: mockTool{name: "some_tool"}, readOnly: false},
			expected: false,
		},

		// Built-in modifying tools
		{
			name:     "write tool",
			tool:     &mockTool{name: "write"},
			expected: false,
		},
		{
			name:     "edit tool",
			tool:     &mockTool{name: "edit"},
			expected: false,
		},

		// Explicitly modifying tools (execution/command tools)
		{
			name:     "shell tool - executes commands",
			tool:     &mockTool{name: ShellToolName}, // bash on Unix, powershell on Windows
			expected: false,
		},
		{
			name:     "bash_kill tool - kills processes",
			tool:     &mockTool{name: "bash_kill"},
			expected: false,
		},
		{
			name:     "worktree tool - creates/modifies worktrees",
			tool:     &mockTool{name: "worktree"},
			expected: false,
		},
		{
			name:     "find_replace tool - modifies files",
			tool:     &mockTool{name: "find_replace"},
			expected: false,
		},

		// MCP Serena tools
		{
			name:     "serena create file",
			tool:     &mockTool{name: "mcp_serena_create_text_file"},
			expected: false,
		},
		{
			name:     "serena replace",
			tool:     &mockTool{name: "mcp_serena_replace_symbol_body"},
			expected: false,
		},
		{
			name:     "serena insert",
			tool:     &mockTool{name: "mcp_serena_insert_after_symbol"},
			expected: false,
		},
		{
			name:     "serena delete",
			tool:     &mockTool{name: "mcp_serena_delete_memory"},
			expected: false,
		},
		{
			name:     "serena read (read-only)",
			tool:     &mockTool{name: "mcp_serena_read_file"},
			expected: true,
		},
		{
			name:     "serena find (read-only)",
			tool:     &mockTool{name: "mcp_serena_find_symbol"},
			expected: true,
		},

		// MCP Supabase tools
		{
			name:     "supabase apply migration",
			tool:     &mockTool{name: "mcp_supabase_apply_migration"},
			expected: false,
		},
		{
			name:     "supabase execute sql",
			tool:     &mockTool{name: "mcp_supabase_execute_sql"},
			expected: false,
		},
		{
			name:     "supabase deploy",
			tool:     &mockTool{name: "mcp_supabase_deploy_edge_function"},
			expected: false,
		},
		{
			name:     "supabase get (read-only)",
			tool:     &mockTool{name: "mcp_supabase_get_advisors"},
			expected: true,
		},
		{
			name:     "supabase list (read-only)",
			tool:     &mockTool{name: "mcp_supabase_list_migrations"},
			expected: true,
		},

		// Planning tools (allowed in planning mode)
		{
			name:     "create_plan tool",
			tool:     &mockTool{name: "create_plan"},
			expected: true,
		},
		{
			name:     "update_plan tool",
			tool:     &mockTool{name: "update_plan"},
			expected: true,
		},
		{
			name:     "add_task tool",
			tool:     &mockTool{name: "add_task"},
			expected: true,
		},
		{
			name:     "update_task tool",
			tool:     &mockTool{name: "update_task"},
			expected: true,
		},
		{
			name:     "create_subtask tool",
			tool:     &mockTool{name: "create_subtask"},
			expected: true,
		},

		// Explicitly modifying tools
		{
			name:     "metadata_writer tool",
			tool:     &mockTool{name: "metadata_writer"},
			expected: false,
		},

		// Read-only tools
		{
			name:     "view tool",
			tool:     &mockTool{name: "view"},
			expected: true,
		},
		{
			name:     "grep tool",
			tool:     &mockTool{name: "grep"},
			expected: true,
		},
		{
			name:     "glob tool",
			tool:     &mockTool{name: "glob"},
			expected: true,
		},

		// Edge cases
		{
			name:     "tool with write in middle",
			tool:     &mockTool{name: "foo_write_bar"},
			expected: false,
		},
		{
			name:     "tool with write at end",
			tool:     &mockTool{name: "foo_bar_write"},
			expected: false,
		},
		{
			name:     "unknown tool (defaults to read-only)",
			tool:     &mockTool{name: "custom_unknown_tool"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsToolReadOnly(tt.tool)
			if result != tt.expected {
				t.Errorf("IsToolReadOnly(%q) = %v, want %v", tt.tool.Name(), result, tt.expected)
			}
		})
	}
}
