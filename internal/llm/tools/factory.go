// Copyright (c) 2025 Reliant Labs
package tools

import (
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// ToolsOptions contains all potential dependency injection parameters for tools
type ToolsOptions struct {
	MCPProjectPath string // Optional MCP project scope path for MCP tool discovery/calls
	// Repository
	Repo db.Repository
	// Skills are injected from the config sync pipeline (daemon → DB → provider).
	// The skill tool operates on this slice exclusively — no filesystem access.
	Skills []config.StoredSkill
}

// ToolsFactory is a global factory for creating tool instances
type ToolsFactory struct {
	opts        *ToolsOptions
	mcpProvider *MCPToolProvider // Project-specific MCP tool provider
}

var (
	// globalFactory can be set for convenience access
	globalFactory *ToolsFactory
)

// NewToolsFactory creates a new tools factory with the given options
func NewToolsFactory(opts *ToolsOptions) *ToolsFactory {
	if opts == nil {
		opts = &ToolsOptions{}
	}

	f := &ToolsFactory{opts: opts}

	// Initialize MCP provider lazily; execution-time MCP runtime is resolved from tool context.
	if opts.MCPProjectPath != "" {
		f.mcpProvider = NewProjectMCPToolProvider(opts.MCPProjectPath)
	} else {
		f.mcpProvider = NewMCPToolProvider()
	}

	return f
}

// GetMCPTools returns MCP tools from this factory's provider using the given runtime.
func (f *ToolsFactory) GetMCPTools(runtime MCPRuntime) []Tool {
	if f.mcpProvider == nil {
		return []Tool{}
	}
	return f.mcpProvider.GetTools(runtime)
}

// GetRepo returns the repository dependency used by this factory.
func (f *ToolsFactory) GetRepo() db.Repository {
	if f.opts == nil {
		return nil
	}
	return f.opts.Repo
}

// WithMCPProjectPath returns a cloned factory scoped to the provided MCP project path.
func (f *ToolsFactory) WithMCPProjectPath(projectPath string) *ToolsFactory {
	if f == nil {
		return nil
	}
	if f.opts == nil {
		return NewToolsFactory(&ToolsOptions{MCPProjectPath: projectPath})
	}
	if f.opts.MCPProjectPath == projectPath {
		return f
	}

	return NewToolsFactory(&ToolsOptions{
		Repo:           f.opts.Repo,
		MCPProjectPath: projectPath,
		Skills:         f.opts.Skills,
	})
}

// WithSkills returns a cloned factory carrying the provided skills. Callers in
// the activity layer build this from a project config load so the skill tool
// (and any future skill-aware tools) never touches the filesystem.
func (f *ToolsFactory) WithSkills(skills []config.StoredSkill) *ToolsFactory {
	if f == nil {
		return nil
	}
	if f.opts == nil {
		return NewToolsFactory(&ToolsOptions{Skills: skills})
	}
	return NewToolsFactory(&ToolsOptions{
		Repo:           f.opts.Repo,
		MCPProjectPath: f.opts.MCPProjectPath,
		Skills:         skills,
	})
}

// SetGlobalFactory sets a global factory instance for convenience
func SetGlobalFactory(factory *ToolsFactory) {
	globalFactory = factory
}

// GetGlobalFactory returns the global factory instance if set
func GetGlobalFactory() *ToolsFactory {
	return globalFactory
}

// File tools
func (f *ToolsFactory) View() Tool {
	return NewViewTool()
}

func (f *ToolsFactory) Write() Tool {
	return NewWriteTool()
}

func (f *ToolsFactory) Edit() Tool {
	return NewEditTool()
}

func (f *ToolsFactory) FindAndReplace() Tool {
	return NewFindAndReplaceTool()
}

// Search tools
func (f *ToolsFactory) Grep() Tool {
	return NewGrepTool()
}

func (f *ToolsFactory) Glob() Tool {
	return NewGlobTool()
}

// Execution tools

// Shell returns the unified shell tool (bash on Unix, PowerShell on Windows)
func (f *ToolsFactory) Shell() Tool {
	return NewShellTool()
}

func (f *ToolsFactory) BashList() Tool {
	return NewBashListTool()
}

func (f *ToolsFactory) BashOutput() Tool {
	return NewBashOutputTool()
}

func (f *ToolsFactory) BashKill() Tool {
	return NewBashKillTool()
}

// Network tools
func (f *ToolsFactory) Fetch() Tool {
	return NewFetchTool()
}

func (f *ToolsFactory) WebSearch() Tool {
	return NewWebSearchTool()
}

// Planning tools
func (f *ToolsFactory) CreatePlan() Tool {
	return NewCreatePlanTool(f.opts.Repo)
}

func (f *ToolsFactory) UpdatePlan() Tool {
	return NewUpdatePlanTool(f.opts.Repo)
}

func (f *ToolsFactory) GetPlan() Tool {
	return NewGetPlanTool(f.opts.Repo)
}

// Task tools
func (f *ToolsFactory) ListTasks() Tool {
	return NewListTasksTool(f.opts.Repo)
}

func (f *ToolsFactory) AddTask() Tool {
	return NewAddTaskTool(f.opts.Repo)
}

func (f *ToolsFactory) UpdateTask() Tool {
	return NewUpdateTaskTool(f.opts.Repo)
}

func (f *ToolsFactory) CreateSubtask() Tool {
	return NewCreateSubtaskTool(f.opts.Repo)
}

func (f *ToolsFactory) AddDependency() Tool {
	return NewAddDependencyTool(f.opts.Repo)
}

func (f *ToolsFactory) RemoveDependency() Tool {
	return NewRemoveDependencyTool(f.opts.Repo)
}

func (f *ToolsFactory) ListReadyTasks() Tool {
	return NewListReadyTasksTool(f.opts.Repo)
}

// Analysis tools
func (f *ToolsFactory) ProjectAnalyzer() Tool {
	return NewProjectAnalyzerTool()
}

func (f *ToolsFactory) Sourcegraph() Tool {
	return NewSourcegraphTool()
}

// Metadata tools
func (f *ToolsFactory) MetadataWriter() Tool {
	return NewMetadataWriterTool()
}

// Layout tools
func (f *ToolsFactory) LayoutLibrary() Tool {
	return NewLayoutLibraryTool()
}

// Component library
func (f *ToolsFactory) ComponentLibrary() Tool {
	return NewComponentLibraryTool()
}

// Worktree tools
func (f *ToolsFactory) Worktree() Tool {
	return NewWorktreeTool(f.opts.Repo)
}

// Skill tools
func (f *ToolsFactory) Skill() Tool {
	return NewSkillTool(f.opts.Skills)
}

// Load tool (dynamic tool loading)
func (f *ToolsFactory) LoadTool() Tool {
	return NewLoadToolTool()
}

// Code manipulation tools
func (f *ToolsFactory) MoveCode() Tool {
	return NewMoveCodeTool()
}

// Workflow editing tools
func (f *ToolsFactory) CreateWorkflow() Tool {
	return NewCreateWorkflowTool(f.opts.Repo)
}

func (f *ToolsFactory) EditWorkflow() Tool {
	return NewEditWorkflowTool(f.opts.Repo)
}

func (f *ToolsFactory) WriteWorkflow() Tool {
	return NewWriteWorkflowTool(f.opts.Repo)
}

// Workflow discovery tools
func (f *ToolsFactory) GetSchema() Tool {
	return NewGetSchemaTool()
}

func (f *ToolsFactory) GetCELReference() Tool {
	return NewGetCELReferenceTool()
}

func (f *ToolsFactory) ListWorkflows() Tool {
	return NewListWorkflowsTool(f.opts.Repo)
}

func (f *ToolsFactory) GetWorkflow() Tool {
	return NewGetWorkflowTool(f.opts.Repo)
}

func (f *ToolsFactory) GetWorkflowSuggestions() Tool {
	return NewGetWorkflowSuggestionsTool()
}

func (f *ToolsFactory) ListPresets() Tool {
	return NewListPresetsTool()
}

func (f *ToolsFactory) GetPreset() Tool {
	return NewGetPresetTool()
}

// Scenario tools
func (f *ToolsFactory) ListScenarios() Tool {
	return NewListScenariosTool(f.opts.Repo)
}

func (f *ToolsFactory) ViewScenario() Tool {
	return NewViewScenarioTool(f.opts.Repo)
}

func (f *ToolsFactory) EditScenario() Tool {
	return NewEditScenarioTool(f.opts.Repo)
}

func (f *ToolsFactory) WriteScenario() Tool {
	return NewWriteScenarioTool(f.opts.Repo)
}

func (f *ToolsFactory) DeleteScenario() Tool {
	return NewDeleteScenarioTool(f.opts.Repo)
}

func (f *ToolsFactory) RunScenario() Tool {
	return NewRunScenarioTool(f.opts.Repo)
}

// GetToolByName returns a tool by name using the given execution-time MCP runtime.
func (f *ToolsFactory) GetToolByName(name string, runtime MCPRuntime) Tool {
	// SPECIAL CASE: agent tool is schema-only and workflow-native
	// It doesn't execute through the normal tool registry
	// Return a schema-only stub that will error if executed
	// (execution should be intercepted by workflow before reaching here)
	if name == "agent" {
		return NewSchemaOnlyTool(
			"agent",
			"Workflow-native agent delegation tool",
			map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]interface{}{
						"type":        "string",
						"description": "Name of the sub-agent to spawn",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Task description for the sub-agent",
					},
				},
				"required": []string{"agent", "prompt"},
			},
		)
	}

	// First check the static registry
	registry := GetToolRegistry()
	for _, def := range registry {
		if def.Name == name {
			return def.Factory(f)
		}
	}

	// If not found in registry, check MCP tools
	if f.mcpProvider != nil {
		mcpTools := f.mcpProvider.GetTools(runtime)
		for _, tool := range mcpTools {
			if tool.Name() == name {
				return tool
			}
		}
	}

	return nil
}

// ListAvailableTools returns a list of all available tool names
func (f *ToolsFactory) ListAvailableTools() []string {
	registry := GetToolRegistry()
	names := make([]string, len(registry))
	for i, def := range registry {
		names[i] = def.Name
	}
	return names
}

// ListAvailableToolsForLocation returns tool names that can run at the given location.
// Tools with ToolRunsAnywhere are included for all locations.
func (f *ToolsFactory) ListAvailableToolsForLocation(location ToolLocation) []string {
	registry := GetToolRegistry()
	var names []string
	for _, def := range registry {
		if def.RunsOn == location || def.RunsOn == ToolRunsAnywhere || location == "" {
			names = append(names, def.Name)
		}
	}
	return names
}
