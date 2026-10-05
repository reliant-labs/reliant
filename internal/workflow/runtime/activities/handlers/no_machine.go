// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// A run with no machine (chats.no_machine; research/DAEMONLESS_RUNS.md) is
// never offered a tool that cannot run. These narrow a resolved tool set to the
// tools that run without the user's machine: call_llm applies them to what the
// model is handed and to what load_tool may reach, so neither this turn nor a
// later load can produce a tool whose every call would fail.

// withoutMachineToolNames drops every tool that needs the user's machine.
func withoutMachineToolNames(names []string) []string {
	kept := make([]string, 0, len(names))
	for _, name := range names {
		if !tools.NeedsMachine(name) {
			kept = append(kept, name)
		}
	}
	return kept
}

// withoutMachineTools narrows a scope's access the same way. "Load anything"
// becomes "load anything that runs without a machine", which is an explicit
// list: the registry is the universe load_tool searches, and MCP is excluded
// wholesale because every user-configured server is daemon-placed.
func withoutMachineTools(access tools.ToolAccess) tools.ToolAccess {
	narrowed := tools.ToolAccess{Preloaded: withoutMachineToolNames(access.Preloaded)}
	if access.LoadableAll {
		for _, def := range tools.GetToolRegistry() {
			if !tools.NeedsMachine(def.Name) {
				narrowed.Loadable = append(narrowed.Loadable, def.Name)
			}
		}
		return narrowed
	}
	narrowed.Loadable = withoutMachineToolNames(access.Loadable)
	return narrowed
}

// noMachineSystemNote tells the model why its tools are what they are, so it
// does not narrate a shell command it wishes it had or keep asking for files.
const noMachineSystemNote = "This run has no machine: it runs on Reliant's servers only. There is no " +
	"checkout, filesystem, shell or local MCP server, and the tools you have are the complete set — " +
	"web access, integrations, planning and Reliant's own run and workflow tools. Do not ask for or " +
	"refer to local files. If part of the task genuinely needs the user's computer, say so in your " +
	"response instead of attempting it."
