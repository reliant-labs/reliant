// Copyright (c) 2025 Reliant Labs
package tools

import (
	"log/slog"

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
	// AgentMessageNotifier wakes a thread parked on its background spawns
	// when spawn_send queues a message into its mailbox. Optional: nil means
	// no doorbell, and delivery falls back to the recipient's next loop
	// boundary (the daemon runtime has no Temporal connection).
	AgentMessageNotifier AgentMessageNotifier
	// ShellPlatform is the shell family of the DAEMON that will execute shell
	// commands, which is frequently not this process's own platform: the
	// server and worker run Linux while the daemon may be Windows. The shell
	// tool's description is written for whatever runs the command, so it is
	// resolved per request from the daemon record and carried here.
	//
	// The zero value (ShellPlatformUnknown) is the honest default — it yields
	// portable, probe-first guidance rather than asserting bash.
	ShellPlatform ShellPlatform
	// ImageGeneratorResolver binds generate_image to the driver layer's model
	// selection. Injected rather than imported because internal/llm/drivers
	// already imports this package. Optional: nil means generate_image reports
	// that image generation is unavailable here, which is correct for the
	// daemon runtime.
	ImageGeneratorResolver ImageGeneratorResolver
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

// cloneOpts copies this factory's options so a With* helper can override one
// field without restating the others.
//
// It copies the struct wholesale rather than listing fields. Hand-enumerated
// clones silently drop any option added later — the new field is simply absent
// from three call sites nobody thinks to revisit — and a dropped ShellPlatform
// would reintroduce exactly the "description does not match the executing
// machine" bug this field exists to fix, with no compile error to catch it.
func (f *ToolsFactory) cloneOpts() *ToolsOptions {
	if f == nil || f.opts == nil {
		return &ToolsOptions{}
	}
	cloned := *f.opts
	return &cloned
}

// WithMCPProjectPath returns a cloned factory scoped to the provided MCP project path.
func (f *ToolsFactory) WithMCPProjectPath(projectPath string) *ToolsFactory {
	if f == nil {
		return nil
	}
	if f.opts != nil && f.opts.MCPProjectPath == projectPath {
		return f
	}

	opts := f.cloneOpts()
	opts.MCPProjectPath = projectPath
	return NewToolsFactory(opts)
}

// WithSkills returns a cloned factory carrying the provided skills. Callers in
// the activity layer build this from a project config load so the skill tool
// (and any future skill-aware tools) never touches the filesystem.
func (f *ToolsFactory) WithSkills(skills []config.StoredSkill) *ToolsFactory {
	if f == nil {
		return nil
	}
	opts := f.cloneOpts()
	opts.Skills = skills
	return NewToolsFactory(opts)
}

// WithShellPlatform returns a cloned factory whose shell tool describes itself
// for the given platform.
//
// This is the seam that carries the daemon's OS to where tool descriptions are
// built. It rides on the factory rather than on GetToolRegistry() because the
// registry is a static name→constructor table with 13 call sites, while the
// factory is already the per-request dependency carrier that every one of those
// sites funnels through when it actually instantiates a tool (def.Factory(f)).
// Threading a parameter through the registry instead would have touched all 13
// sites, most of which only want tool NAMES and have no daemon in scope.
func (f *ToolsFactory) WithShellPlatform(platform ShellPlatform) *ToolsFactory {
	if f == nil {
		return nil
	}
	if f.opts != nil && f.opts.ShellPlatform == platform {
		return f
	}
	opts := f.cloneOpts()
	opts.ShellPlatform = platform
	return NewToolsFactory(opts)
}

// ShellPlatform returns the platform this factory's shell tool describes.
func (f *ToolsFactory) ShellPlatform() ShellPlatform {
	if f == nil || f.opts == nil {
		return ShellPlatformUnknown
	}
	return f.opts.ShellPlatform
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

// ReadAttachment reads user-attached files (paginating PDFs) from the database.
func (f *ToolsFactory) ReadAttachment() Tool {
	return NewReadAttachmentTool(f.opts.Repo)
}

func (f *ToolsFactory) SaveAttachment() Tool {
	return NewSaveAttachmentTool(f.opts.Repo)
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

// Execution tools

// Shell returns the unified shell tool. Its description is written for the
// DAEMON's platform (bash on Unix, PowerShell on Windows, portable guidance
// when unknown) — not for whatever OS this process was compiled on.
func (f *ToolsFactory) Shell() Tool {
	return NewShellTool(f.ShellPlatform())
}

func (f *ToolsFactory) ShellList() Tool {
	return NewShellListTool()
}

func (f *ToolsFactory) ShellOutput() Tool {
	return NewShellOutputTool()
}

func (f *ToolsFactory) ShellKill() Tool {
	return NewShellKillTool()
}

func (f *ToolsFactory) ShellWait() Tool {
	return NewShellWaitTool()
}

// Network tools
func (f *ToolsFactory) Fetch() Tool {
	return NewFetchTool()
}

func (f *ToolsFactory) WebSearch() Tool {
	return NewWebSearchTool()
}

// Media tools

// GenerateImage generates an image, stores it as an attachment, and returns
// the bytes to the model.
func (f *ToolsFactory) GenerateImage() Tool {
	return NewGenerateImageTool(f.opts.Repo, f.opts.ImageGeneratorResolver)
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

// Spawn observability/messaging tools
func (f *ToolsFactory) SpawnStatus() Tool {
	return NewSpawnStatusTool(f.opts.Repo)
}

func (f *ToolsFactory) SpawnSend() Tool {
	return NewSpawnSendTool(f.opts.Repo, f.opts.AgentMessageNotifier)
}

// Analysis tools
func (f *ToolsFactory) ProjectAnalyzer() Tool {
	return NewProjectAnalyzerTool()
}

func (f *ToolsFactory) Sourcegraph() Tool {
	return NewSourcegraphTool()
}

func (f *ToolsFactory) CodeContext() Tool {
	return NewCodeContextTool()
}

// Metadata tools
func (f *ToolsFactory) MetadataWriter() Tool {
	return NewMetadataWriterTool()
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
	slog.Debug("[ToolsFactory] Creating skill tool", "skillCount", len(f.opts.Skills))
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

// AskUser returns the ask_user schema-only tool.
// The ask_user tool lets the LLM ask the user questions. Execution is intercepted
// by the workflow runtime's splitProtoToolCalls → executeAskUserInline, not by
// the normal tool execution path.
func (f *ToolsFactory) AskUser() Tool {
	return NewSchemaOnlyTool(
		ToolAskUser,
		`Ask the user one or more questions and wait for their responses. Use this when you need to:
1. Clarify ambiguous instructions
2. Get user preferences or decisions
3. Offer choices about implementation direction
4. Confirm before taking significant actions

Usage notes:
- The user will always have an option to provide freetext input in addition to any predefined options.
- If you recommend a specific option, list it first and add "(Recommended)" to the label.
- Use allow_multiple: true when choices are not mutually exclusive.
- Group related questions in a single call (up to 4 questions).`,
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"questions": map[string]interface{}{
					"type":        "array",
					"description": "Questions to ask the user (1-4 questions).",
					"minItems":    1,
					"maxItems":    4,
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"question": map[string]interface{}{
								"type":        "string",
								"description": "The question to ask. Should be clear and specific.",
							},
							"options": map[string]interface{}{
								"type":        "array",
								"description": "Available choices. The user can always provide freetext instead.",
								"minItems":    2,
								"maxItems":    6,
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"label": map[string]interface{}{
											"type":        "string",
											"description": "Short display text for this option (1-5 words).",
										},
										"description": map[string]interface{}{
											"type":        "string",
											"description": "Explanation of what this option means.",
										},
										"preview": map[string]interface{}{
											"type":        "string",
											"description": "Optional preview content (code snippet, mockup) rendered as markdown.",
										},
									},
									"required":             []string{"label", "description"},
									"additionalProperties": false,
								},
							},
							"allow_multiple": map[string]interface{}{
								"type":        "boolean",
								"description": "If true, the user can select multiple options. Default is false.",
								"default":     false,
							},
						},
						"required":             []string{"question", "options"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"questions"},
			"additionalProperties": false,
		},
	)
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
