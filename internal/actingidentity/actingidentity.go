// Copyright (c) 2025 Reliant Labs

// Package actingidentity carries "who this run's integration writes speak as"
// through a context, so the layers between an activity and an integration call
// (the tool executor, the integration tool, the action node) can pick the right
// credential without each being told about triggers or workflows.
//
// The fact is the run's, recorded by the launcher from the activation
// (Trigger.acting_identity) and read by an activity off the run's inputs; an
// activity marks its context with With, and the call that resolves a credential
// reads From. A context nobody marked is the owner: absence is the safe,
// pre-existing behaviour, so a code path that forgot to mark can only ever
// under-claim, never speak as the bot by accident.
//
//forge:exclude-contract: pure context marker carrying the acting identity through a context.Context; no I/O, state or time
package actingidentity

import "context"

// App is the GitHub App's bot (`reliant-labs[bot]`).
const App = "app"

// Identity is who a run's integration writes speak as.
type Identity struct {
	// Kind is App, or empty for the owner.
	Kind string
	// Automation names the trigger the run belongs to, for the footer on what
	// the bot writes.
	Automation string
}

// IsApp reports whether writes speak as the App's bot.
func (i Identity) IsApp() bool { return i.Kind == App }

type contextKey struct{}

// With marks ctx with the run's identity. An owner identity (and the zero
// value) leaves ctx unmarked, so absence stays the single way to say "owner".
func With(ctx context.Context, i Identity) context.Context {
	if !i.IsApp() {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, i)
}

// From returns the identity With attached, or the owner's (the zero Identity).
func From(ctx context.Context) Identity {
	if ctx == nil {
		return Identity{}
	}
	i, _ := ctx.Value(contextKey{}).(Identity)
	return i
}
