// Copyright (c) 2025 Reliant Labs
package tools

import "strings"

// Unattended runs (runtime.IsUnattended) have nobody attending them. A trigger
// fired the run — a schedule, a webhook, an integration event, another
// workflow's run — or a node ran a phase of it unattended, and every
// sub-workflow and spawned sub-agent inherits the fact. A person's turn is
// attended, even in a chat an automation started: it is a new run, and only
// the launcher marks a run unattended.
//
// Such a run can carry text nobody vetted — a GitHub issue body, an email, a
// Slack message — so it is not handed a tool that would let that text outlive
// the run or act on it outside Reliant: one that changes what a workflow does,
// creates standing work, starts, steers or stops the user's other runs, or
// changes something through an integration. The resolver applies this through
// exclusion, which the menu, load_tool and execution all consult.
//
// A workflow keeps such a tool for its unattended runs by NAMING it, exactly,
// in the step's preloaded_tools or loadable_tools. A name is a decision about
// that step. A tag or "*" sweeps the tool in with nobody deciding, which is the
// path a prompt injection takes, so neither counts. See
// research/TOOL_CAPABILITIES.md §3.9.

// unattendedWithheld is every category of tool an unattended run is not
// handed unless its declaration names the tool, each with the reason a refusal
// gives. Withholding another category is one more entry here.
var unattendedWithheld = []struct {
	withholds func(name string) bool
	reason    string
}{
	{
		// A workflow is standing work: its activations resolve it by name at
		// fire time, so an edit changes what every later run does with nobody
		// watching. create_workflow is here too although a draft cannot run —
		// a draft is one write_workflow away from complete. Scenarios are the
		// workflow's tests; rewriting or deleting one hides a change to it.
		withholds: toolNamed(ToolCreateWorkflow, ToolEditWorkflow, ToolWriteWorkflow,
			ToolWriteScenario, ToolEditScenario, ToolDeleteScenario),
		reason: "unattended runs can't create or change workflows or their test scenarios",
	},
	{
		// An activation creates standing work that starts unattended runs on
		// its own, long after this one has ended.
		withholds: toolNamed(ToolActivateTrigger),
		reason:    "unattended runs can't activate triggers",
	},
	{
		// start_run's run is attended (agent.start_run), so it would be handed
		// everything withheld here. send_to_run instructs a run that may hold
		// those tools, and control_run stops or resumes the user's own work.
		withholds: toolNamed(ToolStartRun, ToolSendToRun, ToolControlRun),
		reason:    "unattended runs can't start, message or control other runs",
	},
	{
		// An integration action that changes something outside Reliant —
		// posts to Slack, sends an email or a text, opens or comments on an
		// issue, makes an HTTP request — puts unvetted text in front of other
		// people under the user's name. Read-only actions stay reachable.
		withholds: MutatingIntegrationAction,
		reason:    "unattended runs can't take integration actions that change something outside Reliant",
	},
}

// unattendedNote follows the reason in every refusal of a withheld tool: the
// model may repeat it, and a person reading the FAILED row learns the opt-in.
const unattendedNote = "Nobody is attending this run, and a workflow step hands an unattended run this tool only by naming it in its tools."

func toolNamed(names ...string) func(string) bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return func(name string) bool { return set[name] }
}

// UnattendedWithholding is why an unattended run is not handed name unless its
// declaration names it, or "" when an unattended run may reach it like any
// other run.
func UnattendedWithholding(name string) string {
	name = strings.TrimSpace(name)
	for _, category := range unattendedWithheld {
		if category.withholds(name) {
			return category.reason
		}
	}
	return ""
}
