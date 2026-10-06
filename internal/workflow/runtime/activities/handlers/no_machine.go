// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"slices"

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
//
// A run that was given any tools is also handed request_machine, the one tool
// that exists only here (research/NO_MACHINE_CHATS.md §3): a node that wanted
// the shell or the file tools is exactly the one that may need to ask for a
// machine. A node given no tools at all (a title or summary call) stays
// without any.
func withoutMachineTools(access tools.ToolAccess) tools.ToolAccess {
	narrowed := tools.ToolAccess{Preloaded: withoutMachineToolNames(access.Preloaded)}
	if access.LoadableAll {
		for _, def := range tools.GetToolRegistry() {
			if !tools.NeedsMachine(def.Name) && !tools.OnlyWithoutMachine(def.Name) {
				narrowed.Loadable = append(narrowed.Loadable, def.Name)
			}
		}
	} else {
		narrowed.Loadable = withoutMachineToolNames(access.Loadable)
	}
	if len(access.Preloaded) > 0 || access.LoadableAll || len(access.Loadable) > 0 {
		narrowed.Preloaded = withRequestMachine(narrowed.Preloaded)
	}
	return narrowed
}

// noMachineMenu is the tool list a no-machine run is handed this turn: names
// narrowed to tools that run without a machine, plus request_machine when the
// narrowed access (withoutMachineTools) preloads it.
func noMachineMenu(names []string, narrowed tools.ToolAccess) []string {
	menu := withoutMachineToolNames(names)
	if slices.Contains(narrowed.Preloaded, tools.ToolRequestMachine) {
		menu = withRequestMachine(menu)
	}
	return menu
}

func withRequestMachine(names []string) []string {
	if slices.Contains(names, tools.ToolRequestMachine) {
		return names
	}
	return append(names, tools.ToolRequestMachine)
}

// noMachineSystemNote tells the model why its tools are what they are, so it
// does not narrate a shell command it wishes it had or keep asking for files.
const noMachineSystemNote = "This run has no machine: it runs on Reliant's servers only. There is no " +
	"checkout, filesystem, shell or local MCP server, and the tools you have are the complete set — " +
	"web access, integrations, planning and Reliant's own run and workflow tools. Do not ask for or " +
	"refer to local files. If part of the task genuinely needs the user's computer, call " +
	"request_machine with a one-sentence reason instead of attempting it or describing a workaround: " +
	"the user is offered a \"Connect a machine\" button. Then stop and wait for them."
