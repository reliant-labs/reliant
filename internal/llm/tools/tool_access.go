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

	for _, entry := range loadable {
		if entry == LoadableWildcard {
			access.LoadableAll = true
			return access
		}
	}

	access.Loadable = ExpandToolFilter(loadable, mcpToolNames)
	return access
}
