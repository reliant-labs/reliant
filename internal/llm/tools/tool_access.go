// Copyright (c) 2025 Reliant Labs
package tools

import "strings"

// LoadableWildcard means "anything in the registry" in a loadable_tools list.
const LoadableWildcard = "*"

// ToolAccess is a workflow's answer to two different questions:
//
//	preloaded — what is the agent handed, with full schemas, up front
//	loadable  — what may it reach for later, via load_tool
//
// They are separate because collapsing them forces an omission to be read as
// either permission or refusal, and whichever you choose is wrong for half of
// all workflows. A list like ["tag:coding:default"] is a starting bundle; it says
// nothing about the tools outside it. Treating that omission as refusal is what
// made generate_image — deliberately kept out of every default bundle because
// it spends real money on a provider the user may not have configured —
// permanently unreachable.
type ToolAccess struct {
	// Preloaded is the expanded set handed to the model.
	Preloaded []string

	// Loadable is the expanded set load_tool may reach. Meaningful only when
	// LoadableAll is false.
	Loadable []string

	// LoadableAll is true when the workflow declared "*", or declared nothing.
	//
	// UNSET MEANS ALL. That is how the product already behaves — an agent starts
	// with a focused bundle and reaches for the rest on demand — so every
	// workflow written before loadable_tools existed keeps working untouched.
	// Narrowing what an agent can reach is a real decision and has to be
	// written down rather than inferred.
	LoadableAll bool

	// Named is every tool either list spelled out by its exact name, sorted:
	// not reached through a tag, a glob or "*", and not excluded by the same
	// list. Naming a tool is a decision about the step, where a tag or "*" is
	// a convenience — which is why only a name keeps a tool an unattended run
	// is otherwise not handed (UnattendedWithholding).
	Named []string
}

// CanLoad reports whether load_tool may reach a tool.
//
// A preloaded tool is always loadable: it is already in the agent's hands, so
// refusing to "load" it would be incoherent.
func (a ToolAccess) CanLoad(name string) bool {
	if a.LoadableAll {
		return true
	}
	for _, n := range a.Loadable {
		if n == name {
			return true
		}
	}
	for _, n := range a.Preloaded {
		if n == name {
			return true
		}
	}
	return false
}

// ResolveToolAccess expands a workflow's declared lists into the two sets.
//
// A WORKFLOW GETS WHAT IT DECLARES. An absent list and an empty list both mean
// nothing, so nothing here has to distinguish them and no caller has to carry
// a "was it declared?" flag alongside the slice.
//
// It used to. `declaredLoadable` separated "said nothing" from "said nothing is
// loadable", and absent resolved to ALL — so a node that configured a couple of
// preloaded tools silently also granted load access to every tool in the
// registry, including ones deliberately left out of every bundle because they
// cost money to call. Least privilege is the right default, and it is also the
// only one that can be stated in a sentence.
//
// Reaching everything is still available; it just has to be asked for, with
// `loadable_tools: ["*"]`. The builtin workflows already wrote that out —
// there was a comment explaining that the intent should be visible rather than
// inferred — so this promotes a convention they already followed into the rule.
func ResolveToolAccess(preloaded []string, loadable []string, mcpToolNames []string) ToolAccess {
	access := ToolAccess{
		Preloaded: ExpandToolFilter(preloaded, mcpToolNames),
	}
	named := namedTools(preloaded, func() []string { return access.Preloaded })

	for _, entry := range loadable {
		if entry == LoadableWildcard {
			access.LoadableAll = true
			// "*" reaches everything, but a name beside it is still a name.
			access.Named = sortedUnique(append(named,
				namedTools(loadable, func() []string { return ExpandToolFilter(loadable, mcpToolNames) })...))
			return access
		}
	}

	access.Loadable = ExpandToolFilter(loadable, mcpToolNames)
	access.Named = sortedUnique(append(named, namedTools(loadable, func() []string { return access.Loadable })...))
	return access
}

// namedTools are the entries of filter that name one tool exactly and that the
// filter's own expansion kept — so `!edit_workflow` beside `edit_workflow`
// names nothing. expanded is called only when filter names something.
func namedTools(filter []string, expanded func() []string) []string {
	var candidates []string
	for _, spec := range filter {
		switch {
		case spec == "", strings.HasPrefix(spec, "!"), strings.HasPrefix(spec, "tag:"),
			strings.HasPrefix(spec, "spawn:"), containsGlobChars(spec):
			continue
		}
		candidates = append(candidates, spec)
	}
	if len(candidates) == 0 {
		return nil
	}
	kept := make(map[string]bool)
	for _, name := range expanded() {
		kept[name] = true
	}
	var named []string
	for _, name := range candidates {
		if kept[name] {
			named = append(named, name)
		}
	}
	return named
}

// mcpProbeName is a name shaped like any MCP tool, used to test whether a glob
// filter entry can match one without knowing which servers are connected.
const mcpProbeName = "mcp__server__tool"

// FilterReachesMCP reports whether any of the filters can name an `mcp__*`
// tool. It is static: MCP tool names are only known once a daemon has been
// asked, so deciding whether to ask has to work from the filter text alone.
// Exclusions never grant reach and are ignored.
func FilterReachesMCP(filters ...[]string) bool {
	for _, filter := range filters {
		for _, spec := range filter {
			switch {
			case strings.HasPrefix(spec, "!"), strings.HasPrefix(spec, "spawn:"):
				continue
			case spec == "tag:"+string(TagMCP), strings.HasPrefix(spec, "mcp__"):
				return true
			case containsGlobChars(spec) && matchGlob(spec, mcpProbeName):
				return true
			}
		}
	}
	return false
}
