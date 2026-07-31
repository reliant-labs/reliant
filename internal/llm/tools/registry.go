// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/features"
)

// Tool name constants
const (
	// File tools
	ToolView           = "view"
	ToolWrite          = "write"
	ToolEdit           = "edit"
	ToolFindReplace    = "find_replace"
	ToolReadAttachment = "read_attachment"

	// Execution tools
	// Note: ToolShell is not a constant - use ShellToolName from shell_name_*.go
	ToolBashList   = "bash_list"
	ToolBashOutput = "bash_output"
	ToolBashKill   = "bash_kill"

	// Network tools
	ToolFetch     = "fetch"
	ToolWebSearch = "websearch"

	// Planning tools
	ToolCreatePlan = "create_plan"
	ToolUpdatePlan = "update_plan"
	ToolGetPlan    = "get_plan"

	// Task tools
	ToolListTasks        = "list_tasks"
	ToolAddTask          = "add_task"
	ToolUpdateTask       = "update_task"
	ToolCreateSubtask    = "create_subtask"
	ToolAddDependency    = "add_dependency"
	ToolRemoveDependency = "remove_dependency"
	ToolListReadyTasks   = "list_ready_tasks"

	// Analysis tools
	ToolProjectAnalyzer = "project_analyzer"
	ToolSourcegraph     = "sourcegraph"

	// Build tools
	ToolBuild = "build"

	// State tools
	ToolStateTransition = "state_transition"

	// Agent tools (v2)
	ToolAgent = "agent"

	// Worktree tools
	ToolWorktree = "worktree"

	// Note tools
	ToolNotes = "notes"

	// Skill tools
	ToolSkill    = "skill"
	ToolLoadTool = "load_tool"

	// Code manipulation tools
	ToolMoveCode = "move_code"

	// Recommendations tools
	ToolSaveRecommendations = "save_recommendations"

	// Metadata tools
	ToolMetadataWriter = "metadata_writer"

	// Component tools
	ToolComponentLibrary = "component_library"

	// Workflow editing tools
	ToolCreateWorkflow = "create_workflow"
	ToolEditWorkflow   = "edit_workflow"
	ToolWriteWorkflow  = "write_workflow"

	// Workflow discovery tools
	ToolGetSchema              = "get_schema"
	ToolGetCELReference        = "get_cel_reference"
	ToolListWorkflows          = "list_workflows"
	ToolGetWorkflow            = "get_workflow"
	ToolGetWorkflowSuggestions = "get_workflow_suggestions"
	ToolListPresets            = "list_presets"
	ToolGetPreset              = "get_preset"

	// Scenario tools
	ToolListScenarios  = "list_scenarios"
	ToolViewScenario   = "view_scenario"
	ToolEditScenario   = "edit_scenario"
	ToolWriteScenario  = "write_scenario"
	ToolDeleteScenario = "delete_scenario"
	ToolRunScenario    = "run_scenario"

	// Interaction tools
	ToolAskUser = "ask_user"
)

// ToolLocation specifies where a tool executes.
type ToolLocation string

const (
	// ToolRunsOnDaemon means the tool needs daemon primitives (filesystem, shell, git).
	// In distributed mode, it runs on the server but calls daemon for FS/exec ops.
	ToolRunsOnDaemon ToolLocation = "daemon"

	// ToolRunsOnServer means the tool only needs the database/repo.
	// It runs entirely on the server with no daemon interaction.
	ToolRunsOnServer ToolLocation = "server"

	// ToolRunsAnywhere means the tool has no special requirements.
	// It can run on either side (e.g., network-only tools like fetch/websearch).
	ToolRunsAnywhere ToolLocation = "any"
)

// ToolTag represents a flexible label for tools
// Tools can have multiple tags for cross-cutting concerns
type ToolTag string

const (
	TagReadOnly  ToolTag = "readonly"  // Read-only tools (don't modify files/code)
	TagPlan      ToolTag = "plan"      // Tools available in planning mode
	TagFile      ToolTag = "file"      // File operations
	TagSearch    ToolTag = "search"    // Search operations
	TagExecution ToolTag = "execution" // Command execution
	TagShell     ToolTag = "shell"     // Shell tools (bash on Unix, powershell on Windows)
	TagWeb       ToolTag = "web"       // Web operations
	TagPlanning  ToolTag = "planning"  // Planning and task management tools
	TagAnalysis  ToolTag = "analysis"  // Analysis tools
	TagWorkflow  ToolTag = "workflow"  // Workflow builder tools
	TagMCP       ToolTag = "mcp"       // All MCP tools
	TagDefault   ToolTag = "default"   // Default toolset (commonly used tools)
)

// ToolDefinition defines a tool's factory function and metadata
type ToolDefinition struct {
	Name    string
	Factory func(f *ToolsFactory) Tool
	Tags    []ToolTag
	RunsOn  ToolLocation // Where this tool executes
}

// SpawnFilterConfig represents a spawn tool configuration parsed from tool_filter.
// Syntax: spawn:workflow(preset1,preset2)
// Example: spawn:builtin://agent(general,researcher)
type SpawnFilterConfig struct {
	Workflow string   // Workflow to spawn (e.g., "builtin://agent")
	Presets  []string // Presets to enable (e.g., ["general", "researcher"])
}

// ToolFilterResult contains the expanded tool names and any spawn configurations.
type ToolFilterResult struct {
	ToolNames    []string            // Expanded tool names
	SpawnConfigs []SpawnFilterConfig // Spawn configurations parsed from filter
}

// ExpandToolFilterWithSpawn expands a tool filter and extracts spawn configurations.
// This is the preferred method when spawn configs are needed.
//
// Supported filter syntax:
//   - tag:X - Expands to all tools with that tag (e.g., "tag:file", "tag:readonly")
//   - glob patterns - Matches tool names (e.g., "mcp_*", "*search")
//   - !name - Excludes a specific tool (must come after inclusions)
//   - plain names - Direct tool names (e.g., "bash", "view")
//   - spawn:workflow(preset1,preset2) - Spawn tool configuration
//
// Examples:
//   - ["tag:default", "spawn:builtin://agent(general,researcher)"] -> Default tools + agent spawn
//   - ["tag:core", "spawn:builtin://agent()"] -> Core tools, spawn disabled (empty presets)
func ExpandToolFilterWithSpawn(filter []string, mcpToolNames []string) ToolFilterResult {
	result := ToolFilterResult{
		ToolNames:    []string{},
		SpawnConfigs: []SpawnFilterConfig{},
	}

	if len(filter) == 0 {
		return result
	}

	// Separate spawn configs and special tools from regular filter items
	var regularFilter []string
	for _, spec := range filter {
		if strings.HasPrefix(spec, "spawn:") {
			if spawnConfig := parseSpawnFilter(spec); spawnConfig != nil {
				result.SpawnConfigs = append(result.SpawnConfigs, *spawnConfig)
			}
		} else {
			regularFilter = append(regularFilter, spec)
		}
	}

	// Expand regular filter items
	result.ToolNames = ExpandToolFilter(regularFilter, mcpToolNames)
	return result
}

// ParseSpawnEntry parses a spawn entry specification.
// Format: spawn:workflow(preset1,preset2)
// Returns nil if the format is invalid or presets are empty (spawn disabled).
// Exported for use by the call_llm handler when reading from tools_config.spawn.
func ParseSpawnEntry(spec string) *SpawnFilterConfig {
	return parseSpawnFilter(spec)
}

// parseSpawnFilter parses a spawn filter specification.
// Format: spawn:workflow(preset1,preset2)
// Returns nil if the format is invalid or presets are empty (spawn disabled).
func parseSpawnFilter(spec string) *SpawnFilterConfig {
	// Remove "spawn:" prefix
	rest := strings.TrimPrefix(spec, "spawn:")
	if rest == spec {
		return nil // No prefix found
	}

	// Find the opening parenthesis
	parenIdx := strings.Index(rest, "(")
	if parenIdx == -1 {
		// No presets specified - treat as spawn disabled
		return nil
	}

	workflow := rest[:parenIdx]
	if workflow == "" {
		return nil // Empty workflow
	}

	// Extract presets from within parentheses
	if !strings.HasSuffix(rest, ")") {
		return nil // Malformed - no closing paren
	}

	presetsStr := rest[parenIdx+1 : len(rest)-1]
	if presetsStr == "" {
		// Empty presets - spawn disabled
		return nil
	}

	// Split presets by comma and trim whitespace
	presetParts := strings.Split(presetsStr, ",")
	var presets []string
	for _, p := range presetParts {
		p = strings.TrimSpace(p)
		if p != "" {
			presets = append(presets, p)
		}
	}

	if len(presets) == 0 {
		return nil // No valid presets
	}

	return &SpawnFilterConfig{
		Workflow: workflow,
		Presets:  presets,
	}
}

// ExpandToolFilter expands a tool filter that may contain tags and patterns
// into a concrete list of tool names.
//
// NOTE: This function ignores spawn: entries. Use ExpandToolFilterWithSpawn
// if you need spawn configuration support.
//
// Supported filter syntax:
//   - tag:X - Expands to all tools with that tag (e.g., "tag:file", "tag:readonly")
//   - glob patterns - Matches tool names (e.g., "mcp_*", "*search")
//   - !name - Excludes a specific tool (must come after inclusions)
//   - plain names - Direct tool names (e.g., "bash", "view")
//
// Examples:
//   - ["tag:default"] -> All default tools
//   - ["tag:file", "tag:shell"] -> File tools + shell (platform-specific)
//   - ["tag:default", "!tag:shell"] -> Default tools minus shell
//   - ["tag:readonly", "tag:mcp"] -> All read-only and MCP tools (for planning mode)
//   - ["mcp__serena__*"] -> All Serena MCP tools
func ExpandToolFilter(filter []string, mcpToolNames []string) []string {
	// Empty filter means no tools - workflows should explicitly set defaults
	if len(filter) == 0 {
		return []string{}
	}

	registry := GetToolRegistry()

	// Build tag index
	tagIndex := make(map[ToolTag][]string)
	for _, def := range registry {
		for _, tag := range def.Tags {
			tagIndex[tag] = append(tagIndex[tag], def.Name)
		}
	}

	// Special handling for tag:mcp - use actual MCP tool names
	tagIndex[TagMCP] = mcpToolNames

	included := make(map[string]bool)
	excluded := make(map[string]bool)

	for _, spec := range filter {
		// Skip spawn: entries - handled by ExpandToolFilterWithSpawn
		if strings.HasPrefix(spec, "spawn:") {
			continue
		}

		// Handle exclusions (must come after inclusions)
		if len(spec) > 0 && spec[0] == '!' {
			excludeName := spec[1:]

			// Handle !tag:X syntax - expand tag to tool names
			if len(excludeName) > 4 && excludeName[:4] == "tag:" {
				tagName := ToolTag(excludeName[4:])
				if tools, ok := tagIndex[tagName]; ok {
					for _, name := range tools {
						excluded[name] = true
					}
				}
			} else {
				excluded[excludeName] = true
			}
			continue
		}

		// Handle tag:X syntax
		if len(spec) > 4 && spec[:4] == "tag:" {
			tagName := ToolTag(spec[4:])
			if tools, ok := tagIndex[tagName]; ok {
				for _, name := range tools {
					included[name] = true
				}
			}
			continue
		}

		// Handle glob patterns
		if containsGlobChars(spec) {
			// Match against registry tools
			for _, def := range registry {
				if matchGlob(spec, def.Name) {
					included[def.Name] = true
				}
			}
			// Match against MCP tools
			for _, mcpName := range mcpToolNames {
				if matchGlob(spec, mcpName) {
					included[mcpName] = true
				}
			}
			continue
		}

		// Plain tool name
		included[spec] = true
	}

	// Apply exclusions
	for name := range excluded {
		delete(included, name)
		// Also handle glob exclusions
		if containsGlobChars(name) {
			for includedName := range included {
				if matchGlob(name, includedName) {
					delete(included, includedName)
				}
			}
		}
	}

	// Convert to slice
	result := make([]string, 0, len(included))
	for name := range included {
		result = append(result, name)
	}

	return result
}

// containsGlobChars checks if a string contains glob pattern characters
func containsGlobChars(s string) bool {
	return strings.ContainsAny(s, "*?[]")
}

// matchGlob performs simple glob pattern matching
// Supports * (match any sequence) and ? (match single char)
func matchGlob(pattern, name string) bool {
	// Simple implementation - convert to regexp
	// * -> .*
	// ? -> .
	// Escape other special chars
	regexPattern := "^"
	for _, ch := range pattern {
		switch ch {
		case '*':
			regexPattern += ".*"
		case '?':
			regexPattern += "."
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			regexPattern += "\\" + string(ch)
		default:
			regexPattern += string(ch)
		}
	}
	regexPattern += "$"

	matched, _ := regexp.MatchString(regexPattern, name)
	return matched
}

// GetToolRegistry returns all tool definitions
func GetToolRegistry() []ToolDefinition {
	tools := []ToolDefinition{
		// File tools
		{ToolView, (*ToolsFactory).View, []ToolTag{TagFile, TagReadOnly, TagPlan, TagDefault}, ToolRunsAnywhere},
		{ToolReadAttachment, (*ToolsFactory).ReadAttachment, []ToolTag{TagFile, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnServer},
		{ToolWrite, (*ToolsFactory).Write, []ToolTag{TagFile, TagDefault}, ToolRunsAnywhere},
		{ToolEdit, (*ToolsFactory).Edit, []ToolTag{TagFile, TagDefault}, ToolRunsAnywhere},
		{ToolFindReplace, (*ToolsFactory).FindAndReplace, []ToolTag{TagFile, TagDefault}, ToolRunsAnywhere},

		// Search: there are no dedicated grep/glob LLM tools. Agents search with
		// the shell (ripgrep preferred, degrading to grep -r/find). The shell
		// therefore carries TagSearch so `tag:search` still resolves to a real
		// search path for existing presets and user configs.
		//
		// The scoped-search guarantees the deleted tools provided — repo-rooted,
		// node_modules/.git/dist/vendor excluded, bounded results — are NOT
		// provided by the shell. What survives is shell_search_guard.go, which
		// refuses filesystem-wide and home-wide scans before dispatch; that guard
		// is the only thing standing between an agent and a `find /`, so it must
		// stay whether or not the search tools exist.

		// Execution tools - platform-specific shell tool (bash on Unix, PowerShell on Windows)
		// Use tag:shell to get the appropriate shell tool for any platform.
		// These MUST run on the daemon — they need the user's filesystem/project context.
		//
		// `tag:shell` carries the whole SHELL FAMILY, not just the shell tool. The
		// shell tool's own description tells the model: "Use 'run_in_background: true'
		// ... You can then use BashOutput to check output, BashKill to terminate, and
		// BashList to see all running processes." Granting the shell without those
		// three therefore hands an agent instructions for tools it does not have —
		// which is exactly how a reviewer holding `tag:shell` was ordered to read a
		// dev server's port out of `bash_output`, found no such tool, and filed an
		// evidence-free `stuck`. They are also useless on their own: nothing can be
		// in `bash_list` for an agent that cannot start a process. Keeping the tag
		// whole is what stops the next prompt from drifting the same way — and
		// `!tag:shell` correspondingly removes the family, which is what an author
		// excluding the shell means.
		{ShellToolName, (*ToolsFactory).Shell, []ToolTag{TagExecution, TagShell, TagSearch, TagDefault}, ToolRunsOnDaemon},
		{ToolBashList, (*ToolsFactory).BashList, []ToolTag{TagExecution, TagShell, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnDaemon},
		{ToolBashOutput, (*ToolsFactory).BashOutput, []ToolTag{TagExecution, TagShell, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnDaemon},
		{ToolBashKill, (*ToolsFactory).BashKill, []ToolTag{TagExecution, TagShell, TagDefault}, ToolRunsOnDaemon},

		// Network tools — routed to daemon so HTTP requests originate from the user's machine
		{ToolFetch, (*ToolsFactory).Fetch, []ToolTag{TagWeb, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnDaemon},
		{ToolWebSearch, (*ToolsFactory).WebSearch, []ToolTag{TagWeb, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnDaemon},

		// Planning tools
		{ToolCreatePlan, (*ToolsFactory).CreatePlan, []ToolTag{TagPlanning, TagPlan, TagDefault}, ToolRunsOnServer},
		{ToolUpdatePlan, (*ToolsFactory).UpdatePlan, []ToolTag{TagPlanning, TagPlan}, ToolRunsOnServer},
		{ToolGetPlan, (*ToolsFactory).GetPlan, []ToolTag{TagPlanning, TagReadOnly, TagPlan}, ToolRunsOnServer},

		// Task tools
		{ToolListTasks, (*ToolsFactory).ListTasks, []ToolTag{TagPlanning, TagReadOnly, TagPlan, TagDefault}, ToolRunsOnServer},
		{ToolAddTask, (*ToolsFactory).AddTask, []ToolTag{TagPlanning, TagPlan, TagDefault}, ToolRunsOnServer},
		{ToolUpdateTask, (*ToolsFactory).UpdateTask, []ToolTag{TagPlanning, TagPlan, TagDefault}, ToolRunsOnServer},
		{ToolCreateSubtask, (*ToolsFactory).CreateSubtask, []ToolTag{TagPlanning, TagPlan}, ToolRunsOnServer},
		{ToolAddDependency, (*ToolsFactory).AddDependency, []ToolTag{TagPlanning, TagPlan}, ToolRunsOnServer},
		{ToolRemoveDependency, (*ToolsFactory).RemoveDependency, []ToolTag{TagPlanning, TagPlan}, ToolRunsOnServer},
		{ToolListReadyTasks, (*ToolsFactory).ListReadyTasks, []ToolTag{TagPlanning, TagReadOnly, TagPlan}, ToolRunsOnServer},

		// Analysis tools - conditionally add project analyzer
		{ToolSourcegraph, (*ToolsFactory).Sourcegraph, []ToolTag{TagAnalysis, TagReadOnly, TagPlan}, ToolRunsAnywhere},

		// State tools
		// Note: StateTransition is registered dynamically with flow context

		// Metadata tools
		{ToolMetadataWriter, (*ToolsFactory).MetadataWriter, []ToolTag{}, ToolRunsAnywhere},

		// Component tools
		{ToolComponentLibrary, (*ToolsFactory).ComponentLibrary, []ToolTag{TagReadOnly, TagPlan}, ToolRunsAnywhere},

		// Worktree tools
		{ToolWorktree, (*ToolsFactory).Worktree, []ToolTag{}, ToolRunsAnywhere},

		// Skill tools
		{ToolSkill, (*ToolsFactory).Skill, []ToolTag{TagDefault, TagReadOnly, TagPlan}, ToolRunsAnywhere},

		// Load tool (dynamic tool loading)
		{ToolLoadTool, (*ToolsFactory).LoadTool, []ToolTag{TagDefault, TagReadOnly, TagPlan}, ToolRunsAnywhere},

		// Code manipulation tools
		{ToolMoveCode, (*ToolsFactory).MoveCode, []ToolTag{TagFile}, ToolRunsAnywhere},

		// Workflow editing tools
		{ToolCreateWorkflow, (*ToolsFactory).CreateWorkflow, []ToolTag{TagWorkflow}, ToolRunsOnServer},
		{ToolEditWorkflow, (*ToolsFactory).EditWorkflow, []ToolTag{TagWorkflow}, ToolRunsOnServer},
		{ToolWriteWorkflow, (*ToolsFactory).WriteWorkflow, []ToolTag{TagWorkflow}, ToolRunsOnServer},

		// Workflow discovery tools
		{ToolGetSchema, (*ToolsFactory).GetSchema, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsAnywhere},
		{ToolGetCELReference, (*ToolsFactory).GetCELReference, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsAnywhere},
		{ToolListWorkflows, (*ToolsFactory).ListWorkflows, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolGetWorkflow, (*ToolsFactory).GetWorkflow, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolGetWorkflowSuggestions, (*ToolsFactory).GetWorkflowSuggestions, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolListPresets, (*ToolsFactory).ListPresets, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolGetPreset, (*ToolsFactory).GetPreset, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},

		// Interaction tools
		// ask_user is a schema-only tool — execution is intercepted by the workflow
		// runtime (splitProtoToolCalls → executeAskUserInline), not the normal tool path.
		{ToolAskUser, (*ToolsFactory).AskUser, nil, ToolRunsOnServer},

		// Scenario tools
		{ToolListScenarios, (*ToolsFactory).ListScenarios, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolViewScenario, (*ToolsFactory).ViewScenario, []ToolTag{TagWorkflow, TagReadOnly}, ToolRunsOnServer},
		{ToolEditScenario, (*ToolsFactory).EditScenario, []ToolTag{TagWorkflow}, ToolRunsOnServer},
		{ToolWriteScenario, (*ToolsFactory).WriteScenario, []ToolTag{TagWorkflow}, ToolRunsOnServer},
		{ToolDeleteScenario, (*ToolsFactory).DeleteScenario, []ToolTag{TagWorkflow}, ToolRunsOnServer},
		{ToolRunScenario, (*ToolsFactory).RunScenario, []ToolTag{TagWorkflow}, ToolRunsOnServer},
	}

	// Only add project analyzer if not disabled
	if !features.GetGlobalRegistry().EvaluateBool(context.Background(), "project_analyzer_disabled", false) {
		tools = append(tools, ToolDefinition{
			Name:    ToolProjectAnalyzer,
			Factory: (*ToolsFactory).ProjectAnalyzer,
			Tags:    []ToolTag{TagAnalysis, TagReadOnly, TagPlan},
			RunsOn:  ToolRunsAnywhere,
		})
	}

	return tools
}