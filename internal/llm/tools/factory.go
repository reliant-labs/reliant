// Copyright (c) 2025 Reliant Labs
package tools

import (
	"log/slog"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/netguard"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

// ToolsOptions contains all potential dependency injection parameters for tools
type ToolsOptions struct {
	MCPProjectPath string // Optional MCP project scope path for MCP tool discovery/calls
	// MCPToolCache remembers the last successful MCP tool listing per scope and
	// caller, so a request made while the machine is suspended or starting
	// carries the same tools array as the one before it. Optional:
	// NewToolsFactory supplies one, and every factory cloned from this one
	// shares it.
	MCPToolCache *MCPToolCache
	// ControlPlaneURL is the control plane hosting this deployment, "" when it
	// is self-hosted (the daemon, tests). It picks the dial policy of the web
	// tools (netguard.ForDeployment): a hosted deployment's shared worker must
	// not reach its own network; a self-hosted one is the user's machine.
	ControlPlaneURL string
	// Repo is the database the tools read and write. The factory is
	// composition: it hands Repo to each tool as the role that tool declares
	// (deps.go). Optional: nil means every database-backed tool reports that
	// it is unavailable here (the daemon runtime, catalog listings).
	//
	// It becomes *db.Repo once no caller passes a db.Repository (P6). That
	// change must keep an absent repo a nil role in each tool: a nil *db.Repo
	// handed to a role is a non-nil interface, which every tool's "requires a
	// database connection" check would miss.
	Repo db.Repository
	// Skills are injected from the config sync pipeline (daemon → DB → provider).
	// The skill tool operates on this slice exclusively — no filesystem access.
	Skills []config.StoredSkill
	// AgentMessageNotifier wakes a thread parked on its background spawns
	// when spawn_send queues a message into its mailbox. Optional: nil means
	// no doorbell, and delivery falls back to the recipient's next loop
	// boundary (the daemon runtime has no Temporal connection).
	AgentMessageNotifier AgentMessageNotifier
	// SpawnStopper lets spawn_stop reach the workflow running a sub-agent.
	// Optional: nil means spawn_stop reports that stopping is unavailable
	// here, which is the honest answer in the daemon runtime (no Temporal
	// connection) — unlike the doorbell above there is no degraded fallback,
	// because an undelivered stop stops nothing.
	SpawnStopper SpawnStopper
	// IntegrationCredentials resolves a saved connection for integration tools
	// (http__request's `connection`). Optional: nil makes a call that names a
	// connection fail with a FailedPrecondition rather than run unauthenticated.
	IntegrationCredentials httpaction.CredentialSource
	// WriteLedger records the agent's mutating integration calls, so an event
	// they cause can be attributed. Optional: nil records nothing.
	WriteLedger writeledger.Service
	// CatalogSearch backs search_integrations / get_integration_schema with
	// the caller's connection state. Optional: nil searches the embedded
	// catalog with no connections, so refs and schemas are still found but
	// nothing that needs a connection reads as connected.
	CatalogSearch CatalogSearcher
	// RunStarter launches a new top-level run for start_run. Optional: nil
	// means start_run reports it is unavailable here (the daemon runtime has
	// no Temporal connection and no launcher).
	RunStarter RunStarter
	// RunLifecycle pauses, resumes and cancels an existing top-level run for
	// control_run. Optional, same as above.
	RunLifecycle RunLifecycle
	// RunMessenger delivers a message to a live top-level run for
	// send_to_run. Optional, same as above.
	RunMessenger RunMessenger
	// TriggerLister lists the calling user's triggers for list_triggers.
	// Optional: nil means the tool reports it is unavailable here (the daemon
	// runtime has no trigger service).
	TriggerLister TriggerLister
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
	// VideoGeneratorResolver and VideoJobs bind generate_video to the driver
	// layer and to the durable job record. Optional, like the image resolver:
	// nil means generate_video reports it is unavailable here.
	VideoGeneratorResolver VideoGeneratorResolver
	VideoJobs              VideoJobStore
	// ScenarioRunner executes run_scenario on the real DynamicWorkflow.
	// Injected because the runner imports the runtime's activities, which
	// import this package. Optional: nil means run_scenario reports that
	// scenario execution is unavailable here (the daemon runtime).
	ScenarioRunner ScenarioRunner
	// RunMachine sends the worktree tool's git and filesystem work to the
	// machine the run's tools execute on. Optional: nil means the worktree
	// tool reports that creating and deleting worktrees is unavailable here.
	RunMachine RunMachine
	// MachineObserver lets wait_for_machine see whether the run's machine is
	// connected. Wired where tools reach the machine over the network (the
	// worker); its presence is what makes a run's machine REMOTE, and so what
	// earns a run wait_for_machine (see ObservesMachine). Optional: nil means
	// there is no remote machine to wait for here.
	MachineObserver MachineObserver
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

	if opts.MCPToolCache == nil {
		opts.MCPToolCache = NewMCPToolCache()
	}
	f := &ToolsFactory{opts: opts}

	// Initialize MCP provider lazily; execution-time MCP runtime is resolved from tool context.
	if opts.MCPProjectPath != "" {
		f.mcpProvider = NewProjectMCPToolProvider(opts.MCPProjectPath)
	} else {
		f.mcpProvider = NewMCPToolProvider()
	}
	f.mcpProvider.WithCache(opts.MCPToolCache)

	return f
}

// ToolFor builds def's tool through this factory. It is def.Factory(f) as a
// method, so a consumer that only reads tools (the catalog) can depend on an
// interface rather than on *ToolsFactory.
func (f *ToolsFactory) ToolFor(def ToolDefinition) Tool {
	return def.Factory(f)
}

// GetMCPTools returns MCP tools from this factory's provider using the given runtime.
func (f *ToolsFactory) GetMCPTools(runtime MCPRuntime) []Tool {
	return f.GetMCPToolSet(runtime).Tools
}

// GetMCPToolSet is GetMCPTools with where the tools came from.
func (f *ToolsFactory) GetMCPToolSet(runtime MCPRuntime) MCPToolSet {
	if f.mcpProvider == nil {
		return MCPToolSet{Tools: []Tool{}, Source: MCPToolsUnrequested}
	}
	return f.mcpProvider.GetToolSet(runtime)
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

// Test tools
func (f *ToolsFactory) ProveTest() Tool {
	return NewProveTestTool()
}

// Network tools
func (f *ToolsFactory) Fetch() Tool {
	return NewFetchTool(f.netPolicy())
}

func (f *ToolsFactory) WebSearch() Tool {
	return NewWebSearchTool(f.netPolicy())
}

// netPolicy is the dial policy for tools that reach the network from this
// process: hosted deployments may not dial their own network.
func (f *ToolsFactory) netPolicy() netguard.Policy {
	return netguard.ForDeployment(f.opts.ControlPlaneURL)
}

// Media tools

// GenerateImage generates an image, stores it as an attachment, and returns
// the bytes to the model.
func (f *ToolsFactory) GenerateImage() Tool {
	return NewGenerateImageTool(f.opts.Repo, f.opts.ImageGeneratorResolver)
}

// GenerateVideo generates a video, stores it as an attachment, and returns its
// id. The bytes never enter the tool result.
func (f *ToolsFactory) GenerateVideo() Tool {
	return NewGenerateVideoTool(f.opts.Repo, f.opts.VideoJobs, f.opts.VideoGeneratorResolver)
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

// ListPendingQuestions lists the caller's and its sub-agents' unanswered
// ask_user questions.
func (f *ToolsFactory) ListPendingQuestions() Tool {
	return NewListPendingQuestionsTool(f.opts.Repo)
}

// WithdrawQuestion withdraws an async question in the caller's subtree.
func (f *ToolsFactory) WithdrawQuestion() Tool {
	return NewWithdrawQuestionTool(f.opts.Repo)
}

// Spawn observability/messaging tools
func (f *ToolsFactory) SpawnStatus() Tool {
	return NewSpawnStatusTool(f.opts.Repo)
}

func (f *ToolsFactory) SpawnSend() Tool {
	return NewSpawnSendTool(f.opts.Repo, f.opts.AgentMessageNotifier)
}

func (f *ToolsFactory) SpawnStop() Tool {
	return NewSpawnStopTool(f.opts.Repo, f.opts.SpawnStopper)
}

func (f *ToolsFactory) StartRun() Tool {
	return NewStartRunTool(f.opts.Repo, f.opts.RunStarter)
}

func (f *ToolsFactory) ListRuns() Tool {
	return NewListRunsTool(f.opts.Repo)
}

// ReportBug files a Reliant or forge defect with engineering via Sentry.
func (f *ToolsFactory) ReportBug() Tool {
	return NewReportBugTool(f.opts.Repo)
}

func (f *ToolsFactory) GetRun() Tool {
	return NewGetRunTool(f.opts.Repo)
}

func (f *ToolsFactory) ControlRun() Tool {
	return NewControlRunTool(f.opts.Repo, f.opts.RunLifecycle)
}

func (f *ToolsFactory) SendToRun() Tool {
	return NewSendToRunTool(f.opts.Repo, f.opts.RunMessenger)
}

func (f *ToolsFactory) ListTriggers() Tool {
	return NewListTriggersTool(f.opts.Repo, f.opts.TriggerLister)
}

// Analysis tools
func (f *ToolsFactory) ProjectAnalyzer() Tool {
	return NewProjectAnalyzerTool()
}

func (f *ToolsFactory) Sourcegraph() Tool {
	return NewSourcegraphTool(f.netPolicy())
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

// WaitForMachine returns wait_for_machine, which watches the run's machine
// through the factory's MachineObserver.
func (f *ToolsFactory) WaitForMachine() Tool {
	return NewWaitForMachineTool(f.opts.MachineObserver)
}

// Worktree tools
func (f *ToolsFactory) Worktree() Tool {
	return NewWorktreeTool(f.opts.Repo, f.opts.RunMachine)
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

func (f *ToolsFactory) CELEval() Tool {
	return NewCELEvalTool()
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
	return NewWriteScenarioTool(f.opts.Repo, f.opts.ScenarioRunner)
}

func (f *ToolsFactory) DeleteScenario() Tool {
	return NewDeleteScenarioTool(f.opts.Repo)
}

func (f *ToolsFactory) RunScenario() Tool {
	return NewRunScenarioTool(f.opts.Repo, f.opts.ScenarioRunner)
}

// RequestMachine returns request_machine, the no-machine run's way to offer
// the user a machine.
func (f *ToolsFactory) RequestMachine() Tool {
	return NewRequestMachineTool()
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
// the normal tool execution path. With async: true the question is posted
// without waiting and the answer arrives later as a user message
// (research/ASYNC_ASK_USER.md).
func (f *ToolsFactory) AskUser() Tool {
	return NewSchemaOnlyTool(
		ToolAskUser,
		`Ask the user one or more questions. By default you stop and wait for their responses. Use this when you need to:
1. Clarify ambiguous instructions
2. Get user preferences or decisions
3. Offer choices about implementation direction
4. Confirm before taking significant actions

Blocking (default) vs async:
- Blocking: use when you cannot sensibly proceed without the answer — the instructions are ambiguous in a way that changes the work, or the next action is significant or hard to undo.
- async: true: use when you can proceed on a reasonable default and the answer would only refine the work — an FYI, a preference, or a decision where you will go with your recommended option unless told otherwise. The question is posted to the user (in the chat and their inbox) and the tool returns at once; keep working on your default. If the user answers, the answer arrives later as a message from the user; adjust then. Do not ask the same question again while waiting.

Before asking, call list_pending_questions if you may have asked already: do not ask a question that is still pending. Before finishing, withdraw async questions your default made moot (withdraw_question).

Usage notes:
- The user will always have an option to provide freetext input in addition to any predefined options.
- If you recommend a specific option, list it first and add "(Recommended)" to the label. With async: true, say in the question which option you are proceeding with.
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
				"async": map[string]interface{}{
					"type":        "boolean",
					"description": "If true, post the questions without waiting: the tool returns at once and you keep working on your best default; the user's answer, if any, arrives later as a message. Default is false (stop and wait for the answer).",
					"default":     false,
				},
			},
			"required":             []string{"questions"},
			"additionalProperties": false,
		},
	)
}

// ListAvailableToolsForPlacement returns tool names that can run at the given location.
// Tools with PlacementAny are included for all locations.
func (f *ToolsFactory) ListAvailableToolsForPlacement(location Placement) []string {
	registry := GetToolRegistry()
	var names []string
	for _, def := range registry {
		if def.Placement == location || def.Placement == PlacementAny || location == "" {
			names = append(names, def.Name)
		}
	}
	return names
}
