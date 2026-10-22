// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// AttrUserID is the attribute that carries the authenticated user on a server
// span and a log record, the same key product events use.
const AttrUserID = "user.id"

type (
	requestScopeKey struct{}
	boundUserKey    struct{}
)

// requestScope is the mutable cell WithRequestScope installs at the outside
// of a request, so authentication (which runs inside logging and tracing) can
// tell the layers that wrote their records around it who the caller was.
type requestScope struct {
	userID atomic.Pointer[string]
	span   trace.Span
}

// WithRequestScope returns ctx with an empty request scope for BindUser to
// fill. It belongs in the outermost layer of a request, inside the server
// span it will also label: a context value set by an inner layer is invisible
// to the layers outside it, and the "rpc failed" record is written there.
func WithRequestScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, &requestScope{span: trace.SpanFromContext(ctx)})
}

// BindUser records userID as the caller of this request: on the request
// scope (for records written outside the authenticating layer), on the
// server span, and on the returned context for everything downstream. Error
// reports read it back through UserIDFromContext, so each carries the user of
// its own request rather than whichever user was seen last.
func BindUser(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	attr := attribute.String(AttrUserID, userID)
	if scope, ok := ctx.Value(requestScopeKey{}).(*requestScope); ok {
		scope.userID.Store(&userID)
		scope.span.SetAttributes(attr)
	}
	trace.SpanFromContext(ctx).SetAttributes(attr)
	return context.WithValue(ctx, boundUserKey{}, userID)
}

// UserIDFromContext is the caller BindUser recorded, or "".
func UserIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(boundUserKey{}).(string); ok {
		return id
	}
	if scope, ok := ctx.Value(requestScopeKey{}).(*requestScope); ok {
		if id := scope.userID.Load(); id != nil {
			return *id
		}
	}
	return ""
}
