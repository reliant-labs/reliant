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

// withoutMachineTools narrows a run's declaration the same way. The
// capability resolver enforces the no-machine rule per name on its own —
// machine tools and MCP are never reachable on a no-machine run, so "load
// anything" stays exactly that and is not expanded into a list — and this
// narrows the explicit lists so the recorded set does not carry names that
// can never apply.
//
// A run that was given any tools is also handed request_machine, the one tool
// that exists only here (research/NO_MACHINE_CHATS.md §3): a node that wanted
// the shell or the file tools is exactly the one that may need to ask for a
// machine. A node given no tools at all (a title or summary call) stays
// without any.
func withoutMachineTools(access tools.ToolAccess) tools.ToolAccess {
	narrowed := tools.ToolAccess{
		Preloaded:   withoutMachineToolNames(access.Preloaded),
		Loadable:    withoutMachineToolNames(access.Loadable),
		LoadableAll: access.LoadableAll,
	}
	if len(access.Preloaded) > 0 || access.LoadableAll || len(access.Loadable) > 0 {
		narrowed.Preloaded = withRequestMachine(narrowed.Preloaded)
	}
	return narrowed
}

func withRequestMachine(names []string) []string {
	if slices.Contains(names, tools.ToolRequestMachine) {
		return names
	}
	return append(names, tools.ToolRequestMachine)
}

// noMachineSystemNote tells the model why its tools are what they are, so it
// does not narrate a shell command it wishes it had or keep asking for files.
// It names request_machine, so it is the note for a turn that was offered it;
// noMachineSystemNoteWithoutRequest is the same note for a turn that was not
// (a node given no tools), which must not name a tool it does not have.
const (
	noMachineSystemNotePrefix = "This run has no machine: it runs on Reliant's servers only. There is no " +
		"checkout, filesystem, shell or local MCP server, and the tools you have are the complete set — " +
		"web access, integrations, planning and Reliant's own run and workflow tools. Do not ask for or " +
		"refer to local files. "
	noMachineSystemNote = noMachineSystemNotePrefix + "If part of the task genuinely needs the user's computer, call " +
		"request_machine with a one-sentence reason instead of attempting it or describing a workaround: " +
		"the user is offered a \"Connect a machine\" button. Then stop and wait for them."
	noMachineSystemNoteWithoutRequest = noMachineSystemNotePrefix + "If part of the task genuinely needs the " +
		"user's computer, say so in your response instead of attempting it."
)

// noMachineNotes are the system notes a no-machine turn gets: why its tools
// are what they are, then — for a project on GitHub — where its code is and
// how to read it (noMachineRepoNote). Both are read from the turn's capability
// set, so they point at request_machine only when this turn was offered it,
// and say GitHub is readable only when the set lets the turn read it.
func noMachineNotes(caps *tools.Capabilities, repos []githubRepo) []string {
	canRequest := caps.Offers(tools.ToolRequestMachine)
	notes := []string{noMachineSystemNoteWithoutRequest}
	if canRequest {
		notes[0] = noMachineSystemNote
	}
	if note := noMachineRepoNote(repos, githubReadable(caps), canRequest); note != "" {
		notes = append(notes, note)
	}
	return notes
}
