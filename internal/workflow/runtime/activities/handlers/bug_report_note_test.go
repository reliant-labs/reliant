// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/llm/tools"
)

func TestBugReportNote(t *testing.T) {
	t.Parallel()

	resolve := func(access tools.ToolAccess) *tools.Capabilities {
		return tools.ResolveCapabilities(tools.CapabilityInputs{Access: access, Permission: tools.PermissionMutating})
	}

	// A preset that preloads a narrow set and may load the rest — every
	// spawned researcher and reviewer — learns the tool exists here, since
	// it is only a name in load_tool's list.
	loadable := resolve(tools.ToolAccess{Preloaded: []string{tools.ToolView}, LoadableAll: true})
	note := bugReportNote(loadable)
	assert.Contains(t, note, tools.ToolReportBug)
	assert.Contains(t, note, tools.ToolLoadTool)

	// Handed the tool: its own description already says when to use it.
	offered := resolve(tools.ToolAccess{Preloaded: []string{tools.ToolView, tools.ToolReportBug}})
	assert.Empty(t, bugReportNote(offered))

	// Cannot reach it at all: a note would point at a tool that is not there.
	unreachable := resolve(tools.ToolAccess{Preloaded: []string{tools.ToolView}, Loadable: []string{tools.ToolFetch}})
	assert.Empty(t, bugReportNote(unreachable))

	assert.Empty(t, bugReportNote(nil))
}
