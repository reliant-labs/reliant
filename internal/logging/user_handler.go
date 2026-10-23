// Copyright (c) 2025 Reliant Labs
package logging

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/reliant/internal/telemetry"
)

// userHandler stamps user.id on a record logged in the context of an
// authenticated request (telemetry.BindUser), so a server's error and
// "rpc failed" lines name the user they happened to. Records outside a
// request, and any that already carry the attribute, are untouched.
type userHandler struct{ next slog.Handler }

func newUserHandler(next slog.Handler) slog.Handler { return userHandler{next: next} }

func (h userHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h userHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := telemetry.UserIDFromContext(ctx); id != "" {
		present := false
		r.Attrs(func(a slog.Attr) bool {
			present = a.Key == telemetry.AttrUserID
			return !present
		})
		if !present {
			r.AddAttrs(slog.String(telemetry.AttrUserID, id))
		}
	}
	return h.next.Handle(ctx, r)
}

func (h userHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return userHandler{next: h.next.WithAttrs(attrs)}
}

func (h userHandler) WithGroup(name string) slog.Handler {
	return userHandler{next: h.next.WithGroup(name)}
}
