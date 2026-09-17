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
	ToolSaveAttachment = "save_attachment"

	// Execution tools
	// Note: the shell tool's own name is ShellToolName, in shell_platform.go.
	// It is one static name on every platform; the daemon's OS selects the
	// tool's DESCRIPTION at request time, not its name.
	ToolShellList   = "shell_list"
	ToolShellOutput = "shell_output"
	ToolShellWait   = "shell_wait"
	ToolShellKill   = "shell_kill"

	// Network tools
	ToolFetch     = "fetch"
	ToolWebSearch = "websearch"

	// Media tools
	ToolGenerateImage = "generate_image"

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
	ToolCodeContext     = "code_context"

	// Build tools
	ToolBuild = "build"

	// State tools
	ToolStateTransition = "state_transition"

	// Agent tools (v2)
	ToolAgent = "agent"

	// Spawn observability/messaging tools
	ToolSpawnStatus = "spawn_status"
	ToolSpawnSend   = "spawn_send"

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

// ToolTag is a label a workflow can name to reach a group of tools at once.
// A tool may carry several.
//
// TAGS CLASSIFY. THEY DO NOT APPLY THEMSELVES. A workflow gets a tag's tools
// because it named the tag — never because the engine decided the tag was a
// sensible starting point. That rule is what keeps this registry usable by
// more than one product, and it is worth stating because the alternative was
// shipped and broke everything: `default` was auto-reachable and became the
// implicit answer to "what tools does an agent get", which is a question only
// a product can answer.
//
// Two kinds of tag live here, and the difference is in the name.
//
// DESCRIPTIVE tags are facts about a tool: it touches files, it runs a
// process, it makes network calls. They are bare (`file`, `shell`, `web`) and
// are true regardless of what is being built. Any product, and any future
// catalog of a thousand third-party tools, can classify against them.
//
// CURATED tags are somebody's opinion about which tools go together for a
// particular kind of work. They carry a namespace (`coding:default`,
// `coding:plan`) so that reading one tells you whose opinion it is. The
// namespace is the honest part: it admits the bundle is a product's editorial
// choice rather than a property of the tools, and it leaves room for the next
// bundle — `support:default`, `ops:default` — without either pretending to be
// THE default.
//
// A curated tag is still just a name. `tag:coding:default` grants exactly
// what a workflow that lists it asked for, the same as `tag:file` does.
type ToolTag string

const (
	// Descriptive — properties of the tool itself.
	TagReadOnly  ToolTag = "readonly"  // Does not modify files/code
	TagFile      ToolTag = "file"      // File operations
	TagSearch    ToolTag = "search"    // Search operations
	TagExecution ToolTag = "execution" // Command execution
	TagShell     ToolTag = "shell"     // Shell tools (bash on Unix, powershell on Windows)
	TagWeb       ToolTag = "web"       // Web operations
	TagPlanning  ToolTag = "planning"  // Planning and task management tools
	TagAnalysis  ToolTag = "analysis"  // Analysis tools
	TagWorkflow  ToolTag = "workflow"  // Workflow builder tools
	TagMCP       ToolTag = "mcp"       // All MCP tools
	TagMedia     ToolTag = "media"     // Media generation (images, and later audio/video)

	// Curated — a product's editorial bundles. Namespaced so no bundle can
	// masquerade as a universal default. Named explicitly or not granted.
	TagCodingDefault ToolTag = "coding:default" // The coding agent's starting bundle
	TagCodingPlan    ToolTag = "coding:plan"    // Safe for the coding agent's plan mode
)

// TagDescriptions is what a tag means, for anything that presents the tag
// vocabulary to a human — the generated tool reference, and any future tool
// browser that has to make sense of a catalog far larger than this one.
//
// It lives beside the constants because the alternative was a second list in
// the doc generator, which drifted exactly as you would expect: it had gone
// stale on `readonly` (still advertising it as "safe for planning mode", a
// guarantee the removed readonly tier never actually delivered) and had never
// heard of `media` at all. TestTagDescriptionsAreComplete keeps this honest.
var TagDescriptions = map[ToolTag]string{
	TagReadOnly:  "Does not modify files or code",
	TagFile:      "File operations",
	TagSearch:    "Search operations",
	TagExecution: "Command execution",
	TagShell:     "Shell tools (bash on Unix, powershell on Windows)",
	TagWeb:       "Web operations",
	TagPlanning:  "Planning and task management tools",
	TagAnalysis:  "Analysis tools",
	TagWorkflow:  "Workflow builder tools",
	TagMCP:       "Every tool from the chat's connected MCP servers",
	TagMedia:     "Media generation (images, and later audio/video)",

	TagCodingDefault: "The coding agent's starting bundle — one product's editorial grouping, granted only when named",
	TagCodingPlan:    "Tools the coding agent's plan mode starts with",
}

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
//   - plain names - Direct tool names (e.g., "shell", "view")
//   - spawn:workflow(preset1,preset2) - Spawn tool configuration
//
// Examples:
//   - ["tag:coding:default", "spawn:builtin://agent(general,researcher)"] -> Default tools + agent spawn
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
//   - plain names - Direct tool names (e.g., "shell", "view")
//
// Examples:
//   - ["tag:coding:default"] -> All default tools
//   - ["tag:file", "tag:shell"] -> File tools + shell (platform-specific)
//   - ["tag:coding:default", "!tag:shell"] -> Default tools minus shell
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
		{ToolView, (*ToolsFactory).View, []ToolTag{TagFile, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsAnywhere},
		{ToolReadAttachment, (*ToolsFactory).ReadAttachment, []ToolTag{TagFile, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnServer},
		// TagCodingDefault, unlike generate_image. The argument that keeps
		// generate_image out of every workflow is cost: it spends real money
		// on a provider the user may not have configured. This tool spends
		// nothing — it moves bytes we already hold — and it is the only way to
		// turn ANY attachment into a file, which is a file operation every
		// workflow can want. Withholding it is what forced a model to
		// regenerate an image it already had. Server-located because the bytes
		// are in the database; the file still reaches the user's disk through
		// the daemon client on the tool context.
		{ToolSaveAttachment, (*ToolsFactory).SaveAttachment, []ToolTag{TagFile, TagCodingDefault}, ToolRunsOnServer},
		{ToolWrite, (*ToolsFactory).Write, []ToolTag{TagFile, TagCodingDefault}, ToolRunsAnywhere},
		{ToolEdit, (*ToolsFactory).Edit, []ToolTag{TagFile, TagCodingDefault}, ToolRunsAnywhere},
		{ToolFindReplace, (*ToolsFactory).FindAndReplace, []ToolTag{TagFile, TagCodingDefault}, ToolRunsAnywhere},

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
		// ... You can then use shell_output to check output, shell_kill to terminate, and
		// shell_list to see all running processes." Granting the shell without those
		// three therefore hands an agent instructions for tools it does not have —
		// which is exactly how a reviewer holding `tag:shell` was ordered to read a
		// dev server's port out of `shell_output`, found no such tool, and filed an
		// evidence-free `stuck`. They are also useless on their own: nothing can be
		// in `shell_list` for an agent that cannot start a process. Keeping the tag
		// whole is what stops the next prompt from drifting the same way — and
		// `!tag:shell` correspondingly removes the family, which is what an author
		// excluding the shell means.
		{ShellToolName, (*ToolsFactory).Shell, []ToolTag{TagExecution, TagShell, TagSearch, TagCodingDefault}, ToolRunsOnDaemon},
		{ToolShellList, (*ToolsFactory).ShellList, []ToolTag{TagExecution, TagShell, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnDaemon},
		{ToolShellOutput, (*ToolsFactory).ShellOutput, []ToolTag{TagExecution, TagShell, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnDaemon},
		{ToolShellWait, (*ToolsFactory).ShellWait, []ToolTag{TagExecution, TagShell, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnDaemon},
		{ToolShellKill, (*ToolsFactory).ShellKill, []ToolTag{TagExecution, TagShell, TagCodingDefault}, ToolRunsOnDaemon},

		// Network tools. Both are pure net/http plus HTML parsing — no filesystem,
		// no subprocess — so they carry no daemon requirement. They were daemon-routed
		// so outbound requests would originate from the user's machine; that is now
		// paid for elsewhere, because RequiresDaemon treats a daemon-located tool in a
		// node's filter as proof the whole workflow needs a daemon. With TagCodingDefault on
		// both, `tag:coding:default` alone was enough to fire the preflight gate and refuse a
		// workflow that never touches the user's machine.
		{ToolFetch, (*ToolsFactory).Fetch, []ToolTag{TagWeb, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsAnywhere},
		{ToolWebSearch, (*ToolsFactory).WebSearch, []ToolTag{TagWeb, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsAnywhere},

		// Media tools. Deliberately NOT TagCodingDefault: generating an image costs
		// real money on a provider the user may not have configured, and it is
		// irrelevant to the coding workflows that make up most of the product.
		// Opt in with `tag:media` or by naming generate_image in tool_filter.
		//
		// Server-located because it needs the database and an outbound API
		// call, neither of which the daemon has. save_to still reaches the
		// user's disk — through the daemon client on the tool context, the
		// same way every other server-run tool does.
		{ToolGenerateImage, (*ToolsFactory).GenerateImage, []ToolTag{TagMedia}, ToolRunsOnServer},

		// Planning tools
		{ToolCreatePlan, (*ToolsFactory).CreatePlan, []ToolTag{TagPlanning, TagCodingPlan, TagCodingDefault}, ToolRunsOnServer},
		{ToolUpdatePlan, (*ToolsFactory).UpdatePlan, []ToolTag{TagPlanning, TagCodingPlan}, ToolRunsOnServer},
		{ToolGetPlan, (*ToolsFactory).GetPlan, []ToolTag{TagPlanning, TagReadOnly, TagCodingPlan}, ToolRunsOnServer},

		// Task tools
		{ToolListTasks, (*ToolsFactory).ListTasks, []ToolTag{TagPlanning, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnServer},
		{ToolAddTask, (*ToolsFactory).AddTask, []ToolTag{TagPlanning, TagCodingPlan, TagCodingDefault}, ToolRunsOnServer},
		{ToolUpdateTask, (*ToolsFactory).UpdateTask, []ToolTag{TagPlanning, TagCodingPlan, TagCodingDefault}, ToolRunsOnServer},
		{ToolCreateSubtask, (*ToolsFactory).CreateSubtask, []ToolTag{TagPlanning, TagCodingPlan}, ToolRunsOnServer},
		{ToolAddDependency, (*ToolsFactory).AddDependency, []ToolTag{TagPlanning, TagCodingPlan}, ToolRunsOnServer},
		{ToolRemoveDependency, (*ToolsFactory).RemoveDependency, []ToolTag{TagPlanning, TagCodingPlan}, ToolRunsOnServer},
		{ToolListReadyTasks, (*ToolsFactory).ListReadyTasks, []ToolTag{TagPlanning, TagReadOnly, TagCodingPlan}, ToolRunsOnServer},

		// Spawn observability/messaging tools. An agent that already holds a
		// handle to a sub-agent it spawned needs no extra privilege to look
		// at it or talk to it, so this is NOT gated at orchestrator tier —
		// see MinimumPermissionForTool.
		{ToolSpawnStatus, (*ToolsFactory).SpawnStatus, []ToolTag{TagReadOnly}, ToolRunsOnServer},
		{ToolSpawnSend, (*ToolsFactory).SpawnSend, []ToolTag{}, ToolRunsOnServer},

		// Analysis tools - conditionally add project analyzer
		{ToolSourcegraph, (*ToolsFactory).Sourcegraph, []ToolTag{TagAnalysis, TagReadOnly, TagCodingPlan}, ToolRunsAnywhere},

		// code_context is a SYMBOL-graph tool, not a text-search tool, which is
		// why it exists where the grep/glob tools above were deleted. It answers
		// "who calls this / what implements this" — questions ripgrep cannot
		// compute at all, because a call site never names its receiver's type.
		// It is TagCodingDefault because its value is in replacing a multi-turn grep
		// walk, and a tool an agent must first discover does not get used.
		// Daemon-located: it needs the real checkout and a language server.
		{ToolCodeContext, (*ToolsFactory).CodeContext, []ToolTag{TagAnalysis, TagSearch, TagReadOnly, TagCodingPlan, TagCodingDefault}, ToolRunsOnDaemon},

		// State tools
		// Note: StateTransition is registered dynamically with flow context

		// Metadata tools
		{ToolMetadataWriter, (*ToolsFactory).MetadataWriter, []ToolTag{}, ToolRunsAnywhere},

		// Component tools
		{ToolComponentLibrary, (*ToolsFactory).ComponentLibrary, []ToolTag{TagReadOnly, TagCodingPlan}, ToolRunsAnywhere},

		// Worktree tools
		{ToolWorktree, (*ToolsFactory).Worktree, []ToolTag{}, ToolRunsAnywhere},

		// Skill tools
		{ToolSkill, (*ToolsFactory).Skill, []ToolTag{TagCodingDefault, TagReadOnly, TagCodingPlan}, ToolRunsAnywhere},

		// Load tool (dynamic tool loading)
		{ToolLoadTool, (*ToolsFactory).LoadTool, []ToolTag{TagCodingDefault, TagReadOnly, TagCodingPlan}, ToolRunsAnywhere},

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
			Tags:    []ToolTag{TagAnalysis, TagReadOnly, TagCodingPlan},
			RunsOn:  ToolRunsAnywhere,
		})
	}

	return tools
}
