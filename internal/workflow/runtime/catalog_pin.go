// Copyright (c) 2025 Reliant Labs
package runtime

import "go.temporal.io/sdk/workflow"

// A run executes one definition tree. Its root load pins the project's
// workflow catalog as it is then (ActivityLoadWorkflow's CatalogPin), and
// every later load of the run (an inline child, a loop body, a router
// target, a spawned sub-agent, a preset) passes that pin, so a child loaded
// an hour in is the one synced when the run started, not a later push.
//
// The pin travels in the workflow context: every executor derives its
// context from the root's after the load. It is read from recorded activity
// results only, so replay sees the same pin.

type catalogPinKey struct{}

// withCatalogPin is ctx carrying the run's catalog pin.
func withCatalogPin(ctx workflow.Context, pin string) workflow.Context {
	if pin == "" {
		return ctx
	}
	return workflow.WithValue(ctx, catalogPinKey{}, pin)
}

// catalogPinFrom is the run's catalog pin, "" when it has none (a run
// recorded before pins, or one with no project).
func catalogPinFrom(ctx workflow.Context) string {
	if ctx == nil {
		return ""
	}
	pin, _ := ctx.Value(catalogPinKey{}).(string)
	return pin
}
