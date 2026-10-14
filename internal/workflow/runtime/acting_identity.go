// Copyright (c) 2025 Reliant Labs
package runtime

// The acting identity: who the integration writes of a run speak as.
//
// An automation can act as its owner, through their own GitHub token, or as
// the GitHub App's bot, through a per-repository installation token. That is
// chosen per activation (Trigger.acting_identity), recorded by the launcher on
// the run it starts, and read at the one place an integration call resolves its
// credential. It is a fact about the RUN, not a workflow input, so it travels
// as a runtime-injected key exactly as the launch marker does.
//
// Three rules follow from what it protects:
//
//   - Only a launch sets it. It is a RuntimeInjectedInput, so
//     BuildWorkflowInputs refuses it from a client: a person cannot make their
//     own run speak as the bot.
//   - It never crosses to a run a person starts. A reply in the chat (and a late
//     wake, which is a run a person's reply started) deletes it, as it deletes
//     the launch marker: the bot is the AUTOMATION's identity, and a person
//     typing in the chat acts as themselves.
//   - It is inherited by every sub-workflow, loop body and spawned sub-agent of
//     the run, monotonically, like unattended: a child cannot opt out of the
//     identity its parent was given, because the automation's author chose it
//     for everything the run does.
const InputKeyActingIdentity = "__acting_identity"

// InputKeyAutomation names the trigger whose run this is, for the footer on
// what the bot writes. Same lifecycle as InputKeyActingIdentity.
const InputKeyAutomation = "__automation"

// ActingIdentity returns the identity recorded on a run's inputs, or "" for a
// run that has none (every run a person started, and every owner run).
func ActingIdentity(inputs map[string]interface{}) string {
	identity, _ := inputs[InputKeyActingIdentity].(string)
	return identity
}

// propagateActingIdentity makes a child's identity EXACTLY its parent's. Call it
// LAST in child-input assembly, beside propagateUnattended.
//
// Exactly, not "at least": unlike unattended, which may only be turned on, the
// identity may not be raised either. A workflow's `args` could otherwise hand a
// child of an owner run `__acting_identity: app`, and the child would write as
// the bot though the activation never chose it. The parent's absence is as
// authoritative as its presence.
func propagateActingIdentity(parent, child map[string]interface{}) {
	if child == nil {
		return
	}
	for _, key := range []string{InputKeyActingIdentity, InputKeyAutomation} {
		if value, ok := parent[key]; ok {
			child[key] = value
		} else {
			delete(child, key)
		}
	}
}

// DropActingIdentity removes the identity from inputs for a run a person
// starts. It is the one place the "a human turn never acts as the bot" rule is
// written, so the reply paths and the late wake cannot drift apart.
func DropActingIdentity(inputs map[string]interface{}) {
	delete(inputs, InputKeyActingIdentity)
	delete(inputs, InputKeyAutomation)
}
