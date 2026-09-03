// Copyright (c) 2025 Reliant Labs
package tools

import (
	"sort"
	"testing"
)

func TestExpandToolFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		filter            []string
		mcpTools          []string
		expectContains    []string
		expectNotContains []string
	}{
		{
			name:              "empty filter returns no tools",
			filter:            []string{},
			mcpTools:          []string{"mcp__test__foo"},
			expectContains:    []string{},
			expectNotContains: []string{"view", "edit", ShellToolName, "mcp__test__foo"},
		},
		{
			name:           "tag:default returns default tools",
			filter:         []string{"tag:default"},
			mcpTools:       []string{"mcp__test__foo"},
			expectContains: []string{"view", "edit", ShellToolName},
		},
		{
			name:              "tag:file expands to file tools",
			filter:            []string{"tag:file"},
			mcpTools:          []string{},
			expectContains:    []string{"view", "write", "edit", "find_replace", "move_code"},
			expectNotContains: []string{ShellToolName},
		},
		{
			// With the dedicated grep/glob tools removed, the shell IS the search
			// path, so tag:search must resolve to it — otherwise the tag expands
			// to nothing and every preset referencing it silently loses search.
			// What keeps this from re-becoming a disk scan is shell_search_guard,
			// not the tag.
			name:              "tag:search resolves to the shell",
			filter:            []string{"tag:search"},
			mcpTools:          []string{},
			expectContains:    []string{ShellToolName},
			expectNotContains: []string{"view", "write", "edit"},
		},
		{
			// The shell is deliberately NOT in tag:readonly. The shell is
			// readonly-TIER for permission purposes (it is the only search path,
			// so readonly agents must be able to load it), but the readonly TAG
			// still means "cannot mutate", and a shell plainly can. Keeping the
			// tag honest is what lets `tag:readonly` stay a meaningful filter.
			name:              "tag:readonly excludes write tools",
			filter:            []string{"tag:readonly"},
			mcpTools:          []string{},
			expectContains:    []string{"view", "fetch", "websearch", "get_plan", "list_tasks"},
			expectNotContains: []string{"write", "edit", "find_replace", ShellToolName, "worktree", "move_code"},
		},
		{
			name:              "tag:plan includes planning mode tools",
			filter:            []string{"tag:plan"},
			mcpTools:          []string{},
			expectContains:    []string{"view", "fetch", "websearch", "create_plan", "update_plan", "get_plan", "list_tasks", "add_task", "update_task", "add_dependency", "remove_dependency", "list_ready_tasks"},
			expectNotContains: []string{"write", "edit", "find_replace", ShellToolName, "worktree", "move_code"},
		},
		{
			name:              "tag:mcp includes MCP tools",
			filter:            []string{"tag:mcp"},
			mcpTools:          []string{"mcp__serena__find", "mcp__chrome__click"},
			expectContains:    []string{"mcp__serena__find", "mcp__chrome__click"},
			expectNotContains: []string{"view", ShellToolName},
		},
		{
			name:              "glob pattern matches tools",
			filter:            []string{"mcp__serena__*"},
			mcpTools:          []string{"mcp__serena__find", "mcp__serena__search", "mcp__chrome__click"},
			expectContains:    []string{"mcp__serena__find", "mcp__serena__search"},
			expectNotContains: []string{"mcp__chrome__click"},
		},
		{
			name:              "negation excludes tools",
			filter:            []string{"tag:file", "!write", "!edit"},
			mcpTools:          []string{},
			expectContains:    []string{"view", "find_replace"},
			expectNotContains: []string{"write", "edit"},
		},
		{
			name:              "mix tags and specific tools",
			filter:            []string{"tag:search", ShellToolName, "view"},
			mcpTools:          []string{},
			expectContains:    []string{ShellToolName, "view"},
			expectNotContains: []string{"write", "edit"},
		},
		{
			name:              "specific tools only",
			filter:            []string{"view", "edit", ShellToolName},
			mcpTools:          []string{},
			expectContains:    []string{"view", "edit", ShellToolName},
			expectNotContains: []string{"write"},
		},
		{
			name:              "duplicate tool from tag and explicit name",
			filter:            []string{"tag:file", "view", "edit"},
			mcpTools:          []string{},
			expectContains:    []string{"view", "edit", "write", "find_replace", "move_code"},
			expectNotContains: []string{ShellToolName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandToolFilter(tt.filter, tt.mcpTools)

			// Convert to map for easy lookup
			resultMap := make(map[string]bool)
			for _, name := range result {
				resultMap[name] = true
			}

			// Check expected inclusions
			for _, expected := range tt.expectContains {
				if !resultMap[expected] {
					t.Errorf("Expected tool %q to be included but it wasn't. Got: %v", expected, result)
				}
			}

			// Check expected exclusions
			for _, notExpected := range tt.expectNotContains {
				if resultMap[notExpected] {
					t.Errorf("Expected tool %q to be excluded but it was included. Got: %v", notExpected, result)
				}
			}
		})
	}
}

func TestMatchGlob(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		name    string
		matches bool
	}{
		{"mcp_*", "mcp__serena__find", true},
		{"mcp_*", ShellToolName, false},
		{"*search", "websearch", true},
		{"*search", "view", false},
		{"mcp__serena__*", "mcp__serena__find", true},
		{"mcp__serena__*", "mcp__chrome__click", false},
		{"bas?", ShellToolName, true}, // "bash" matches "bas?" on Unix
		{"she?l", "shelf", false},
		{"view", "view", true},
		{"view", "viewer", false},
	}

	for _, tt := range tests {
		result := matchGlob(tt.pattern, tt.name)
		if result != tt.matches {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, result, tt.matches)
		}
	}
}

func TestExpandToolFilterDeduplication(t *testing.T) {
	t.Parallel()
	// Verify that tools are never duplicated even when specified both
	// explicitly and via tag expansion
	tests := []struct {
		name   string
		filter []string
	}{
		{
			name:   "explicit tool also in tag",
			filter: []string{"tag:file", "view", "edit"},
		},
		{
			name:   "same tag specified twice",
			filter: []string{"tag:file", "tag:file"},
		},
		{
			name:   "overlapping tags",
			filter: []string{"tag:file", "tag:default"},
		},
		{
			name:   "explicit tool specified twice",
			filter: []string{"view", "view", "edit", "edit"},
		},
		{
			name:   "mcp tool both explicit and via tag",
			filter: []string{"tag:mcp", "mcp__test__foo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mcpTools := []string{"mcp__test__foo", "mcp__test__bar"}
			result := ExpandToolFilter(tt.filter, mcpTools)

			// Check for duplicates by comparing slice length to unique count
			unique := make(map[string]bool)
			for _, name := range result {
				if unique[name] {
					t.Errorf("Duplicate tool found: %q. Filter: %v, Result: %v", name, tt.filter, result)
				}
				unique[name] = true
			}

			if len(result) != len(unique) {
				t.Errorf("Result has duplicates: len(result)=%d, unique=%d", len(result), len(unique))
			}
		})
	}
}

func TestTagDefault(t *testing.T) {
	t.Parallel()
	// Verify that tag:default includes the expected default tools
	mcpTools := []string{"mcp__test__foo"}
	result := ExpandToolFilter([]string{"tag:default"}, mcpTools)

	// Sort for consistent comparison
	sort.Strings(result)

	// Default tools should include the focused set for general-purpose work
	expectedDefaults := []string{
		"view", "write", "edit", "find_replace", // file
		ShellToolName,        // execution + search (platform-specific: bash on Unix, powershell on Windows)
		"fetch", "websearch", // web
		"create_plan",                           // planning (update_plan, get_plan deferred)
		"list_tasks", "add_task", "update_task", // tasks (dependency tools deferred)
		"bash_list", "bash_output", "bash_kill", // background process management
		"skill",     // skill loading
		"load_tool", // dynamic tool loading
	}

	resultMap := make(map[string]bool)
	for _, name := range result {
		resultMap[name] = true
	}

	for _, expected := range expectedDefaults {
		if !resultMap[expected] {
			t.Errorf("Expected default tool %q to be included", expected)
		}
	}

	// Verify deferred tools are NOT in defaults
	deferredTools := []string{
		"component_library", "worktree", "move_code",
		"update_plan", "get_plan",
		"add_dependency", "remove_dependency", "list_ready_tasks",
	}
	for _, deferred := range deferredTools {
		if resultMap[deferred] {
			t.Errorf("Deferred tool %q should NOT be in tag:default", deferred)
		}
	}

	t.Logf("Default tools (%d): %v", len(result), result)
}

func TestTagReadOnly(t *testing.T) {
	t.Parallel()
	// Verify that tag:readonly includes only truly read-only tools (don't modify files/code/state)
	mcpTools := []string{"mcp__test__readonly"}
	result := ExpandToolFilter([]string{"tag:readonly"}, mcpTools)

	resultMap := make(map[string]bool)
	for _, name := range result {
		resultMap[name] = true
	}

	// Truly read-only tools that should be included
	expectedReadOnly := []string{
		"view",                     // file reading
		"bash_list", "bash_output", // execution listing
		"fetch", "websearch", // web reading
		"get_plan", "list_tasks", // planning reading (not create/update)
		"list_ready_tasks",  // dependency reading
		"sourcegraph",       // analysis
		"component_library", // component library reading
	}

	// Non read-only tools that should NOT be included
	notExpectedReadOnly := []string{
		"write", "edit", "find_replace", "move_code", // file modification
		ShellToolName, "bash_kill", // execution (shell is platform-specific)
		"worktree",                   // git modification
		"metadata_writer",            // metadata writing
		"create_plan", "update_plan", // planning modification
		"add_task", "update_task", "create_subtask", // task modification
		"add_dependency", "remove_dependency", // dependency modification
	}

	for _, expected := range expectedReadOnly {
		if !resultMap[expected] && expected != "project_analyzer" { // project_analyzer may be disabled
			t.Errorf("Expected read-only tool %q to be included in tag:readonly", expected)
		}
	}

	for _, notExpected := range notExpectedReadOnly {
		if resultMap[notExpected] {
			t.Errorf("Non read-only tool %q should NOT be included in tag:readonly", notExpected)
		}
	}

	t.Logf("Read-only tools (%d): %v", len(result), result)
}

func TestTagPlan(t *testing.T) {
	t.Parallel()
	// Verify that tag:plan includes all tools available in planning mode
	mcpTools := []string{}
	result := ExpandToolFilter([]string{"tag:plan"}, mcpTools)

	resultMap := make(map[string]bool)
	for _, name := range result {
		resultMap[name] = true
	}

	// Tools that should be available in planning mode
	expectedPlan := []string{
		"view",                     // file reading
		"bash_list", "bash_output", // execution listing
		"fetch", "websearch", // web reading
		"create_plan", "update_plan", "get_plan", // planning tools
		"list_tasks", "add_task", "update_task", "create_subtask", // task tools
		"add_dependency", "remove_dependency", "list_ready_tasks", // dependency tools
		"sourcegraph",       // analysis
		"component_library", // component library reading
	}

	// Tools that should NOT be available in planning mode
	notExpectedPlan := []string{
		"write", "edit", "find_replace", "move_code", // file modification
		ShellToolName, "bash_kill", // execution (shell is platform-specific)
		"worktree",        // git modification
		"metadata_writer", // metadata writing
	}

	for _, expected := range expectedPlan {
		if !resultMap[expected] && expected != "project_analyzer" { // project_analyzer may be disabled
			t.Errorf("Expected planning tool %q to be included in tag:plan", expected)
		}
	}

	for _, notExpected := range notExpectedPlan {
		if resultMap[notExpected] {
			t.Errorf("Non-planning tool %q should NOT be included in tag:plan", notExpected)
		}
	}

	t.Logf("Planning mode tools (%d): %v", len(result), result)
}

// ============================================================================
// Spawn Filter Tests
// ============================================================================

func TestParseSpawnFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		spec string
		want *SpawnFilterConfig
	}{
		{
			name: "valid spawn with multiple presets",
			spec: "spawn:builtin://agent(general,researcher,code_reviewer)",
			want: &SpawnFilterConfig{
				Workflow: "builtin://agent",
				Presets:  []string{"general", "researcher", "code_reviewer"},
			},
		},
		{
			name: "valid spawn with single preset",
			spec: "spawn:builtin://agent(general)",
			want: &SpawnFilterConfig{
				Workflow: "builtin://agent",
				Presets:  []string{"general"},
			},
		},
		{
			name: "spawn with empty presets returns nil (disabled)",
			spec: "spawn:builtin://agent()",
			want: nil,
		},
		{
			name: "spawn without parentheses returns nil",
			spec: "spawn:builtin://agent",
			want: nil,
		},
		{
			name: "spawn with whitespace in presets",
			spec: "spawn:builtin://agent( general , researcher )",
			want: &SpawnFilterConfig{
				Workflow: "builtin://agent",
				Presets:  []string{"general", "researcher"},
			},
		},
		{
			name: "not a spawn prefix",
			spec: "tag:default",
			want: nil,
		},
		{
			name: "malformed - no closing paren",
			spec: "spawn:builtin://agent(general",
			want: nil,
		},
		{
			name: "empty workflow",
			spec: "spawn:(general)",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSpawnFilter(tt.spec)
			if tt.want == nil {
				if got != nil {
					t.Errorf("parseSpawnFilter(%q) = %+v, want nil", tt.spec, got)
				}
				return
			}
			if got == nil {
				t.Errorf("parseSpawnFilter(%q) = nil, want %+v", tt.spec, tt.want)
				return
			}
			if got.Workflow != tt.want.Workflow {
				t.Errorf("parseSpawnFilter(%q).Workflow = %q, want %q", tt.spec, got.Workflow, tt.want.Workflow)
			}
			if len(got.Presets) != len(tt.want.Presets) {
				t.Errorf("parseSpawnFilter(%q).Presets = %v, want %v", tt.spec, got.Presets, tt.want.Presets)
				return
			}
			for i, p := range got.Presets {
				if p != tt.want.Presets[i] {
					t.Errorf("parseSpawnFilter(%q).Presets[%d] = %q, want %q", tt.spec, i, p, tt.want.Presets[i])
				}
			}
		})
	}
}

func TestExpandToolFilterWithSpawn(t *testing.T) {
	t.Parallel()
	mcpTools := []string{"mcp__test__tool"}

	t.Run("extracts spawn configs and expands tools", func(t *testing.T) {
		filter := []string{
			"tag:file",
			"spawn:builtin://agent(general,researcher)",
			ShellToolName,
		}

		result := ExpandToolFilterWithSpawn(filter, mcpTools)

		// Should have spawn config
		if len(result.SpawnConfigs) != 1 {
			t.Errorf("Expected 1 spawn config, got %d", len(result.SpawnConfigs))
		} else {
			if result.SpawnConfigs[0].Workflow != "builtin://agent" {
				t.Errorf("Expected workflow 'builtin://agent', got %q", result.SpawnConfigs[0].Workflow)
			}
			if len(result.SpawnConfigs[0].Presets) != 2 {
				t.Errorf("Expected 2 presets, got %d", len(result.SpawnConfigs[0].Presets))
			}
		}

		// Should have expanded tools (no spawn: in tool names)
		for _, name := range result.ToolNames {
			if name == "spawn:builtin://agent(general,researcher)" {
				t.Error("spawn: should not appear in tool names")
			}
		}

		// Should include shell tool (bash on Unix, powershell on Windows)
		found := false
		for _, name := range result.ToolNames {
			if name == ShellToolName {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected %q in tool names", ShellToolName)
		}
	})

	t.Run("multiple spawn configs", func(t *testing.T) {
		filter := []string{
			"spawn:builtin://agent(general)",
			"spawn:builtin://researcher(deep)",
		}

		result := ExpandToolFilterWithSpawn(filter, mcpTools)

		if len(result.SpawnConfigs) != 2 {
			t.Errorf("Expected 2 spawn configs, got %d", len(result.SpawnConfigs))
		}
	})

	t.Run("empty spawn presets are ignored", func(t *testing.T) {
		filter := []string{
			"spawn:builtin://agent()",
			ShellToolName,
		}

		result := ExpandToolFilterWithSpawn(filter, mcpTools)

		if len(result.SpawnConfigs) != 0 {
			t.Errorf("Expected 0 spawn configs for empty presets, got %d", len(result.SpawnConfigs))
		}
	})

	t.Run("empty filter returns empty result", func(t *testing.T) {
		result := ExpandToolFilterWithSpawn([]string{}, mcpTools)

		if len(result.ToolNames) != 0 {
			t.Errorf("Expected 0 tool names, got %d", len(result.ToolNames))
		}
		if len(result.SpawnConfigs) != 0 {
			t.Errorf("Expected 0 spawn configs, got %d", len(result.SpawnConfigs))
		}
	})
}

func TestExpandToolFilterWithSpawn_AskUser(t *testing.T) {
	t.Parallel()
	mcpTools := []string{}

	t.Run("ask_user in filter appears in ToolNames", func(t *testing.T) {
		filter := []string{"tag:default", "ask_user"}
		result := ExpandToolFilterWithSpawn(filter, mcpTools)

		found := false
		for _, name := range result.ToolNames {
			if name == "ask_user" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected ask_user in ToolNames when explicitly in filter")
		}
	})

	t.Run("ask_user not in ToolNames when not in filter", func(t *testing.T) {
		filter := []string{"tag:default"}
		result := ExpandToolFilterWithSpawn(filter, mcpTools)

		for _, name := range result.ToolNames {
			if name == "ask_user" {
				t.Error("ask_user should not be in ToolNames when not in filter")
			}
		}
	})
}

func TestExpandToolFilterIgnoresSpawn(t *testing.T) {
	t.Parallel()
	// ExpandToolFilter should ignore spawn: entries
	mcpTools := []string{}
	filter := []string{
		"spawn:builtin://agent(general)",
		ShellToolName,
	}

	result := ExpandToolFilter(filter, mcpTools)

	// Should only include shell tool (bash on Unix, powershell on Windows), not the spawn: entry
	if len(result) != 1 {
		t.Errorf("Expected 1 tool, got %d: %v", len(result), result)
	}
	if len(result) > 0 && result[0] != ShellToolName {
		t.Errorf("Expected %q, got %q", ShellToolName, result[0])
	}
}

func TestExpandToolFilterMCPTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		filter       []string
		mcpToolNames []string
		wantIncluded []string
		wantExcluded []string
	}{
		{
			name:         "include tag:mcp",
			filter:       []string{"tag:mcp"},
			mcpToolNames: []string{"mcp__server__tool1", "mcp__server__tool2"},
			wantIncluded: []string{"mcp__server__tool1", "mcp__server__tool2"},
		},
		{
			name:         "exclude tag:mcp",
			filter:       []string{"tag:default", "tag:mcp", "!tag:mcp"},
			mcpToolNames: []string{"mcp__server__tool1", "mcp__server__tool2"},
			wantExcluded: []string{"mcp__server__tool1", "mcp__server__tool2"},
		},
		{
			name:         "include default exclude mcp",
			filter:       []string{"tag:default", "!tag:mcp"},
			mcpToolNames: []string{"mcp__server__tool1"},
			wantIncluded: []string{"view", "edit", ShellToolName}, // some default tools
			wantExcluded: []string{"mcp__server__tool1"},
		},
		{
			name:         "exclude specific mcp tool",
			filter:       []string{"tag:mcp", "!mcp__server__tool1"},
			mcpToolNames: []string{"mcp__server__tool1", "mcp__server__tool2"},
			wantIncluded: []string{"mcp__server__tool2"},
			wantExcluded: []string{"mcp__server__tool1"},
		},
		{
			name:         "glob exclusion",
			filter:       []string{"tag:mcp", "!mcp__server__*"},
			mcpToolNames: []string{"mcp__server__tool1", "mcp__other__tool"},
			wantIncluded: []string{"mcp__other__tool"},
			wantExcluded: []string{"mcp__server__tool1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandToolFilter(tt.filter, tt.mcpToolNames)
			resultSet := make(map[string]bool)
			for _, name := range result {
				resultSet[name] = true
			}

			// Check expected inclusions
			for _, want := range tt.wantIncluded {
				if !resultSet[want] {
					t.Errorf("expected %q to be included, but it wasn't", want)
				}
			}

			// Check expected exclusions
			for _, unwant := range tt.wantExcluded {
				if resultSet[unwant] {
					t.Errorf("expected %q to be excluded, but it was included", unwant)
				}
			}
		})
	}
}
