// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// scopeHandler stamps who and what a record was logged for, once, here, so no
// call site has to: user.id for a record logged in the context of an
// authenticated request (telemetry.BindUser), and user.id plus
// reliant.chat.id, thread.id and workflow.id for one logged in the context of a
// run (telemetry.WithRunScope: a Temporal activity, a reconciler pass). It is
// what lets an issue name the users, or failing that the chats, it affected.
//
// Records outside both, and any attribute a record already carries, are
// untouched. The request's user wins over the run's: they are the same person
// whenever both are known, and the request is the authenticated one.
type scopeHandler struct{ next slog.Handler }

func newScopeHandler(next slog.Handler) slog.Handler { return scopeHandler{next: next} }

// NewScopeHandler is the handler install puts in every process's chain, for a
// test that wants the production stamping in front of a handler of its own.
func NewScopeHandler(next slog.Handler) slog.Handler { return newScopeHandler(next) }

func (h scopeHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h scopeHandler) Handle(ctx context.Context, r slog.Record) error {
	run := telemetry.RunScopeFromContext(ctx)
	if id := telemetry.UserIDFromContext(ctx); id != "" {
		run.UserID = id
	}
	attrs := run.Attrs()
	if len(attrs) == 0 {
		return h.next.Handle(ctx, r)
	}
	have := map[string]bool{}
	r.Attrs(func(a slog.Attr) bool {
		have[a.Key] = true
		return true
	})
	for _, a := range attrs {
		if !have[a.Key] {
			r.AddAttrs(a)
		}
	}
	return h.next.Handle(ctx, r)
}

func (h scopeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return scopeHandler{next: h.next.WithAttrs(attrs)}
}

func (h scopeHandler) WithGroup(name string) slog.Handler {
	return scopeHandler{next: h.next.WithGroup(name)}
}
