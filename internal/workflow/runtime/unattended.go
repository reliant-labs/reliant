// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/askuser"
)

// Unattended runs: no human is at the keyboard.
//
// `unattended` is a fact about the RUN, not a parameter of a workflow. Either a
// person is there to answer or they are not, and that answer is the same for the
// root workflow, every sub-workflow it refs, every loop body, and every agent it
// spawns. So the runtime propagates it, rather than asking each YAML author to
// remember a passthrough entry.
//
// That choice is deliberate and it is the whole point. The `ask` input is
// declared, defaulted, and threaded by hand — and reaches exactly one edge
// condition, so setting it changes nothing on the node that actually blocks. A
// flag whose correctness depends on every future YAML remembering to forward it
// will be wrong again. This one cannot be dropped on the way down.
//
// Propagation is MONOTONE: a child can turn unattended ON (a node may run one
// phase without a human), but it can never turn it OFF, because there is no
// human for it to turn back on. Same shape as parent_permission capping in
// buildSpawnChildInputs.
//
// Two things block a run on a human, and each resolves differently — see
// executeAskUserInline (the ask_user tool) and executeAskQuestionSignalFlow (the
// ask_question node). Neither fabricates an answer, and neither writes a
// questions row.
const (
	// InputKeyUnattended is the workflow input name.
	InputKeyUnattended = "unattended"

	// UnattendedResolver is the value recorded in an auto-resolved node output's
	// resolved_by field. A human answer never carries it.
	UnattendedResolver = "unattended"

	// UnattendedMarker prefixes every tool result and node record produced by an
	// auto-resolution, so a run's history is greppable for "the agent tried to
	// ask and nobody was there".
	UnattendedMarker = "[UNATTENDED]"
)

// IsUnattended reports whether this run has no human available to answer.
// It accepts bool and the string forms a toolbar/CLI input can arrive as.
func IsUnattended(inputs map[string]interface{}) bool {
	if inputs == nil {
		return false
	}
	switch v := inputs[InputKeyUnattended].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

// propagateUnattended asserts the parent's unattended flag onto a child input
// map. Call it LAST in child-input assembly: presets, passthrough, args and
// schema defaults all run first, and none of them may switch an unattended run
// back to interactive.
func propagateUnattended(parent, child map[string]interface{}) {
	if child == nil || !IsUnattended(parent) {
		return
	}
	child[InputKeyUnattended] = true
}

// unattendedAskUserResponse is the tool result an ask_user call gets when the run
// has no human in it. It says plainly that nothing was answered and nothing was
// chosen, echoes the questions back so the agent does not have to re-derive them,
// and requires the decision AND its reason to be stated — that statement is the
// auto-resolution record.
//
// It is deliberately not the timeout wording ("The user did not respond in time"),
// which would blame a person who was never asked.
func unattendedAskUserResponse(metadata string) string {
	var b strings.Builder
	b.WriteString(UnattendedMarker + " No human is available to answer.\n\n")
	b.WriteString("This run was started unattended. Your question was NOT shown to anyone, " +
		"nobody answered it, and nothing was selected on the user's behalf.\n")

	if md, ok := askuser.ParseMetadata(metadata); ok {
		b.WriteString("\nUnanswered:\n")
		for _, q := range md.Questions {
			b.WriteString("- " + q.Question + "\n")
			if labels := q.OptionLabels(); len(labels) > 0 {
				b.WriteString("  options offered: " + strings.Join(labels, " | ") + "\n")
			}
		}
	}

	b.WriteString("\nResolve this yourself from the brief you already have. In your next message, " +
		"state the decision you made and the reason you made it — that statement is the only " +
		"record that this was auto-resolved rather than chosen by a person, so do not skip it. " +
		"Then continue. Do not call ask_user again in this run; it will keep coming back here.")
	return b.String()
}
