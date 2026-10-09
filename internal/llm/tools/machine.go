// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// PreflightConfig is the registry's tool classification in the shape the
// runtime's static analyses take (RequiresDaemon, MachineRequirements), which
// cannot import this package. The activities package installs it for the
// in-workflow preflight; the launcher and the trigger service call it directly
// for their write-time checks, so neither depends on init order.
func PreflightConfig() *v2.PreflightConfig {
	return &v2.PreflightConfig{
		// Static analysis cannot see which MCP servers a user will have, and
		// PlacementOf resolves every mcp__ name (including the "mcp__*" probe
		// below) to its server placement. An unresolvable tool is treated as
		// daemon-bound: better to check for a daemon than to skip the check.
		IsDaemonTool: func(name string) bool {
			placement, err := PlacementOf(name)
			return err != nil || placement == PlacementDaemon
		},
		NeedsMachine: NeedsMachine,
		ExpandToolFilter: func(filter []string) []string {
			expanded := ExpandToolFilter(filter, nil)
			// MCP names are unknown statically; surface a probe name for any
			// filter that can reach one so IsDaemonTool sees it.
			if FilterReachesMCP(filter) {
				expanded = append(expanded, "mcp__*")
			}
			return expanded
		},
	}
}

// NeedsMachine reports whether a tool touches the user's machine at all. A run
// with no machine (research/DAEMONLESS_RUNS.md) is offered no tool for which
// this is true, and refuses one at execution.
//
// It is NOT `Placement == PlacementDaemon`. Placement says which process
// executes a tool; this says whether the tool's work happens on the user's
// machine. The two disagree for the file tools: they are any/server-placed and
// execute in the worker, but every read and write goes to the user's disk
// through the worker's daemon client, which resolves — and can wake — the
// user's default daemon. Offering them to a no-machine run would recreate the
// failure this exists to prevent.
//
// An unknown tool counts as needing one: every caller uses this to decide
// whether a no-machine run may touch something, and "unknown" must not read
// as "safe".
func NeedsMachine(name string) bool {
	name = strings.TrimSpace(name)
	if _, ok := machineBoundTools[name]; ok {
		return true
	}
	placement, err := PlacementOf(name)
	return err != nil || placement == PlacementDaemon
}

// machineBoundTools are any/server-placed tools whose work is on the user's
// machine anyway, with the reason. Daemon-placed tools and every mcp__ tool are
// covered by PlacementOf and need no entry.
var machineBoundTools = map[string]string{
	ToolView:             "reads the user's files through the daemon client",
	ToolWrite:            "writes the user's files through the daemon client",
	ToolEdit:             "edits the user's files through the daemon client",
	ToolFindReplace:      "edits the user's files through the daemon client",
	ToolMoveCode:         "edits the user's files through the daemon client",
	ToolSaveAttachment:   "its only job is writing a file to the user's disk",
	ToolComponentLibrary: "installs components into the user's checkout",
	ToolWorktree:         "creates and deletes git worktrees on disk",
}

// serverSafeTools are the any/server-placed tools that were checked and do
// their whole job without the user's machine. It exists so that a tool added
// to the registry has to be classified on purpose (see machine_test.go)
// rather than defaulting to "safe" by omission.
var serverSafeTools = map[string]struct{}{
	ToolReadAttachment: {}, ToolFetch: {}, ToolWebSearch: {}, ToolSourcegraph: {},
	// save_to is optional and already reports a failed write as a warning on
	// a successful result.
	ToolGenerateImage: {}, ToolGenerateVideo: {},
	ToolCreatePlan: {}, ToolUpdatePlan: {}, ToolGetPlan: {},
	ToolListTasks: {}, ToolAddTask: {}, ToolUpdateTask: {}, ToolCreateSubtask: {},
	ToolAddDependency: {}, ToolRemoveDependency: {}, ToolListReadyTasks: {},
	ToolSpawnStatus: {}, ToolSpawnSend: {}, ToolSpawnStop: {},
	ToolStartRun: {}, ToolListRuns: {}, ToolGetRun: {}, ToolControlRun: {}, ToolSendToRun: {},
	ToolSkill: {}, ToolLoadTool: {}, ToolAskUser: {},
	ToolCreateWorkflow: {}, ToolEditWorkflow: {}, ToolWriteWorkflow: {},
	ToolGetSchema: {}, ToolGetCELReference: {}, ToolListWorkflows: {}, ToolGetWorkflow: {},
	ToolGetWorkflowSuggestions: {}, ToolListPresets: {}, ToolGetPreset: {},
	ToolListScenarios: {}, ToolViewScenario: {}, ToolEditScenario: {}, ToolWriteScenario: {},
	ToolDeleteScenario: {}, ToolRunScenario: {},
	// Catalog search and trigger activation: database and embedded-catalog
	// reads and writes only.
	ToolSearchIntegrations: {}, ToolGetIntegrationSchema: {},
	ToolActivateTrigger: {}, ToolListTriggers: {},
	// Returns a fixed text; the offer is the rendered call itself.
	ToolRequestMachine: {},
	// Database reads and a Sentry event.
	ToolReportBug: {},
}

// noMachineOnlyTools are tools that exist only for a run with no machine, and
// are never offered to any other. The no-machine narrowing in call_llm hands
// one over directly; the capability resolver (capabilities.go, exclusion)
// keeps it from every run that has a machine however a filter names it, and
// never lets load_tool grant, advertise or list one.
var noMachineOnlyTools = map[string]struct{}{
	// Asking for a machine is meaningless on one.
	ToolRequestMachine: {},
}

// OnlyWithoutMachine reports whether a tool is offered only to a run with no
// machine (see noMachineOnlyTools).
func OnlyWithoutMachine(name string) bool {
	_, ok := noMachineOnlyTools[strings.TrimSpace(name)]
	return ok
}

func (d ToolDefinition) hasTag(tag ToolTag) bool {
	for _, t := range d.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
