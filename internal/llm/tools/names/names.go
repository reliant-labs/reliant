// Package names provides tool name and tag constants for validation.
// This package is separate from the main tools package to avoid import cycles
// with workflow/parser (which needs to validate tool names, but tools imports parser).
package names

// Tool name constants - these must match the tools registered in tools/registry.go
const (
	// File tools
	ToolView        = "view"
	ToolWrite       = "write"
	ToolEdit        = "edit"
	ToolFindReplace = "find_replace"

	// Execution tools
	// Platform-specific shell tools - use tag:shell to get the appropriate one
	ToolBash       = "bash"       // Unix/macOS/Linux
	ToolPowerShell = "powershell" // Windows
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
	ToolListTasks     = "list_tasks"
	ToolAddTask       = "add_task"
	ToolUpdateTask    = "update_task"
	ToolCreateSubtask = "create_subtask"

	// Analysis tools
	ToolProjectAnalyzer = "project_analyzer"
	ToolSourcegraph     = "sourcegraph"

	// Build tools
	ToolBuild = "build"

	// State tools
	ToolStateTransition = "state_transition"

	// Agent tools
	ToolAgent = "agent"

	// Worktree tools
	ToolWorktree = "worktree"

	// Skill tools
	ToolSkill        = "skill"
	ToolInstallSkill = "install_skill"
	ToolLoadTool     = "load_tool"

	// Note tools
	ToolNotes = "notes"

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
	ToolListWorkflows          = "list_workflows"
	ToolGetWorkflow            = "get_workflow"
	ToolGetWorkflowSuggestions = "get_workflow_suggestions"
	ToolListPresets            = "list_presets"
	ToolGetPreset              = "get_preset"
	ToolGetSchema              = "get_schema"
	ToolGetCELRef              = "get_cel_reference"

	// Scenario tools
	ToolListScenarios  = "list_scenarios"
	ToolViewScenario   = "view_scenario"
	ToolEditScenario   = "edit_scenario"
	ToolWriteScenario  = "write_scenario"
	ToolDeleteScenario = "delete_scenario"
	ToolRunScenario    = "run_scenario"
)

// Tag constants for tool filtering
const (
	TagReadOnly  = "readonly"
	TagPlan      = "plan"
	TagFile      = "file"
	TagSearch    = "search"
	TagExecution = "execution"
	TagShell     = "shell" // Shell tools (bash on Unix, powershell on Windows)
	TagWeb       = "web"
	TagPlanning  = "planning"
	TagAnalysis  = "analysis"
	TagWorkflow  = "workflow"
	TagMCP       = "mcp"
	TagDefault   = "default"
)

// AllToolNames returns all known tool names for validation.
var AllToolNames = []string{
	ToolView, ToolWrite, ToolEdit, ToolFindReplace,
	ToolBash, ToolPowerShell, ToolBashList, ToolBashOutput, ToolBashKill,
	ToolFetch, ToolWebSearch,
	ToolCreatePlan, ToolUpdatePlan, ToolGetPlan,
	ToolListTasks, ToolAddTask, ToolUpdateTask, ToolCreateSubtask,
	ToolProjectAnalyzer, ToolSourcegraph,
	ToolBuild,
	ToolStateTransition,
	ToolAgent,
	ToolWorktree,
	ToolSkill,
	ToolInstallSkill,
	ToolLoadTool,
	ToolNotes,
	ToolMoveCode,
	ToolSaveRecommendations,
	ToolMetadataWriter,
	ToolComponentLibrary,
	// Workflow editing
	ToolCreateWorkflow, ToolEditWorkflow, ToolWriteWorkflow,
	// Workflow discovery
	ToolListWorkflows, ToolGetWorkflow, ToolGetWorkflowSuggestions, ToolListPresets, ToolGetPreset, ToolGetSchema, ToolGetCELRef,
	// Scenarios
	ToolListScenarios, ToolViewScenario, ToolEditScenario, ToolWriteScenario, ToolDeleteScenario, ToolRunScenario,
}

// AllToolTags returns all known tool tags for validation.
var AllToolTags = []string{
	TagReadOnly, TagPlan, TagFile, TagSearch, TagExecution, TagShell, TagWeb,
	TagPlanning, TagAnalysis, TagWorkflow, TagMCP, TagDefault,
}

// toolNameSet for O(1) lookup
var toolNameSet = make(map[string]bool)

// toolTagSet for O(1) lookup
var toolTagSet = make(map[string]bool)

func init() {
	for _, name := range AllToolNames {
		toolNameSet[name] = true
	}
	for _, tag := range AllToolTags {
		toolTagSet[tag] = true
	}
}

// IsValidToolName checks if a name is a known tool.
func IsValidToolName(name string) bool {
	return toolNameSet[name]
}

// IsValidToolTag checks if a tag is a known tool tag.
func IsValidToolTag(tag string) bool {
	return toolTagSet[tag]
}
