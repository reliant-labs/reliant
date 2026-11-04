// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// DefinitionSource renders where t's runs read their workflow from (desc,
// resolved with the automation precedence on t's machine) for display: the
// kind, the file and machine, and a warning for each thing the owner should
// act on.
//
// The precedence is explicit here because it differs from a chat's: an
// automation runs the project's workflow of its name before the owner's own
// account workflow of that name, so an account draft cannot silently replace
// it. A same-named account workflow is still reported, since a person
// editing it would expect the automation to change.
func DefinitionSource(desc *workflowsource.Description, t *core.Trigger) *reliantv1.TriggerDefinitionSource {
	if desc == nil || desc.Resolved == nil {
		return nil
	}
	machine := t.DaemonName
	if machine == "" {
		machine = t.DaemonID
	}
	out := &reliantv1.TriggerDefinitionSource{Kind: string(desc.Source)}
	switch desc.Source {
	case workflowref.SourceBuiltin:
		out.Summary = fmt.Sprintf("builtin workflow %s", desc.Ref.Raw)
	case workflowref.SourceUser:
		out.Summary = fmt.Sprintf("your account workflow %q (the project has none of that name)", t.Workflow)
	case workflowref.SourceProject:
		out.Path = desc.Path
		out.Summary = "project workflow at " + desc.Path
		switch {
		case desc.MachineCatalog:
			out.DaemonId, out.DaemonName = t.DaemonID, t.DaemonName
			out.Summary += " on machine " + machine
		case t.DaemonID != "":
			out.Summary += " (every machine's catalog)"
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"machine %s has never synced this project, so this reads whichever machine synced the file last; it switches to %s's own files once it connects", machine, machine))
		default:
			out.Summary += " (every machine's catalog)"
		}
	}
	if desc.Shadowed != "" {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"%s has the same name; automations run the project's workflow, not yours, so editing yours changes nothing here: rename it to tell them apart", desc.Shadowed))
	}
	return out
}
