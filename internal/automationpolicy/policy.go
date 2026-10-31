// Copyright (c) 2025 Reliant Labs
// Package automationpolicy holds the explicit delegation policy for standing work.
//
//forge:exclude-contract: concrete policy values and context helpers
package automationpolicy

import (
	"context"
	"time"
)

type revisionKey struct{}

// WithRevision binds the approved activation revision through the app RPC to
// the storage row lock, closing the preview-to-write race.
func WithRevision(ctx context.Context, revision time.Time) context.Context {
	return context.WithValue(ctx, revisionKey{}, revision)
}
func Revision(ctx context.Context) (time.Time, bool) {
	r, ok := ctx.Value(revisionKey{}).(time.Time)
	return r, ok
}

func Refusal() string {
	return "This activation change needs the owner's approval card, and an unattended run cannot show one. Ask the owner to make it in an interactive chat, or to relax Settings > Automations > Automation approvals."
}

// Execution is supplied by the workflow runtime, never by model parameters.
type Execution struct {
	Unattended bool
	Approvals  map[string]string // tool call id -> approved preview hash
}
type executionKey struct{}

func WithExecution(ctx context.Context, e Execution) context.Context {
	return context.WithValue(ctx, executionKey{}, e)
}
func From(ctx context.Context) Execution { e, _ := ctx.Value(executionKey{}).(Execution); return e }

// Actor attributes an atomic configuration audit entry to its originating call.
type Actor struct{ ChatID, RunID, ToolCallID string }
type actorKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}
func ActorFrom(ctx context.Context) Actor { a, _ := ctx.Value(actorKey{}).(Actor); return a }
