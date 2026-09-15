// Copyright (c) 2025 Reliant Labs
package tools

// LoadableWildcard means "anything in the registry" in a loadable_tools list.
const LoadableWildcard = "*"

// ToolAccess is a workflow's answer to two different questions:
//
//	preloaded — what is the agent handed, with full schemas, up front
//	loadable  — what may it reach for later, via load_tool
//
// They are separate because collapsing them forces an omission to be read as
// either permission or refusal, and whichever you choose is wrong for half of
// all workflows. A list like ["tag:default"] is a starting bundle; it says
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
// declaredLoadable distinguishes "the workflow said nothing" from "the workflow
// said nothing is loadable". Absent resolves to ALL; an empty-but-present list
// resolves to nothing, which is how a workflow says "exactly what I preloaded".
// That distinction cannot be carried by the slice alone, since both arrive as
// len 0.
func ResolveToolAccess(preloaded []string, loadable []string, declaredLoadable bool, mcpToolNames []string) ToolAccess {
	access := ToolAccess{
		Preloaded: ExpandToolFilter(preloaded, mcpToolNames),
	}

	if !declaredLoadable {
		access.LoadableAll = true
		return access
	}

	for _, entry := range loadable {
		if entry == LoadableWildcard {
			access.LoadableAll = true
			return access
		}
	}

	access.Loadable = ExpandToolFilter(loadable, mcpToolNames)
	return access
}
