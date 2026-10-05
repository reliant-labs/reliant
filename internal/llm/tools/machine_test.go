// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// NeedsMachine is what a run with no machine filters its tool menu by, so it
// must name every tool that touches the user's machine — not just the
// daemon-placed ones. The file tools are the case that matters: they are
// any-placed (they execute in the worker) but reach the user's disk through the
// worker's daemon client, so a placement-only filter would offer them to a
// no-machine run and each call would land on, or wake, the user's default
// daemon.
func TestNeedsMachine_CoversToolsThatReachTheUsersDiskFromTheServer(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		// daemon-placed
		ShellToolName, ToolShellList, ToolShellOutput, ToolShellWait, ToolShellKill, ToolCodeContext,
		// any/server-placed, but the work is on the user's disk
		ToolView, ToolWrite, ToolEdit, ToolFindReplace, ToolMoveCode, ToolSaveAttachment,
		ToolComponentLibrary, ToolWorktree, ToolMetadataWriter, ToolProjectAnalyzer,
		// every MCP tool: user-configured servers are always daemon-placed
		"mcp__chrome-devtools__new_page", "mcp__serena__find_symbol",
	} {
		assert.True(t, NeedsMachine(name), "%s must count as needing a machine", name)
	}
}

func TestNeedsMachine_LeavesServerWorkOffered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		ToolFetch, ToolWebSearch, ToolSourcegraph,
		ToolCreatePlan, ToolAddTask, ToolListTasks,
		ToolLoadTool, ToolSkill, ToolAskUser, ToolReadAttachment,
		ToolStartRun, ToolListRuns, ToolSpawnStatus, ToolSpawnSend,
		ToolCreateWorkflow, ToolGetSchema,
		// save_to is optional and already degrades to a warning, so the
		// media tools stay useful without a machine.
		ToolGenerateImage, ToolGenerateVideo,
	} {
		assert.False(t, NeedsMachine(name), "%s runs entirely on the server", name)
	}
}

// An unknown name fails closed: the menu filter and the execution refusal both
// key on this, and "unknown" must never read as "safe to run with no machine".
func TestNeedsMachine_UnknownToolFailsClosed(t *testing.T) {
	t.Parallel()
	assert.True(t, NeedsMachine("no_such_tool"))
}

// Every registry entry is classified on purpose: a newly registered tool that
// reaches the user's disk through the daemon client but is not daemon-placed
// has to be added to machineBoundTools, and this test is what asks.
func TestNeedsMachine_EveryAnyOrServerToolIsDeliberatelyClassified(t *testing.T) {
	t.Parallel()

	for _, def := range GetToolRegistry() {
		if def.Placement == PlacementDaemon {
			continue
		}
		if _, listed := machineBoundTools[def.Name]; listed {
			continue
		}
		if _, cleared := serverSafeTools[def.Name]; cleared {
			continue
		}
		if def.hasTag(TagIntegration) {
			continue // placement comes from a curated manifest
		}
		t.Errorf("tool %q (placement %q) is in neither machineBoundTools nor serverSafeTools; "+
			"decide whether it touches the user's machine", def.Name, def.Placement)
	}
}
