package observe

import (
	"context"
	"log/slog"
)

// loggerCtxKey is the unexported context key under which a *slog.Logger
// is carried. Unexported so the only way in or out is WithLogger /
// FromContext — no other package can collide on the key or smuggle a
// value past the accessor.
type loggerCtxKey struct{}

// WithLogger returns a copy of ctx carrying logger. The runtime
// (serverkit.Run, a cobra RunE, a worker fan-out) calls this once at the
// top of a request/command so downstream code can recover the
// request-scoped logger without threading it through every signature.
//
// A nil logger is stored as-is; FromContext substitutes a usable default
// when it reads one back, so callers never have to nil-check.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey{}, logger)
}

// FromContext returns the *slog.Logger previously stored by WithLogger,
// or slog.Default() when none is present (or a nil one was stored). It
// never returns nil, so call sites can log unconditionally:
//
//	observe.FromContext(ctx).Info("doing the thing", "id", id)
//
// This is the read half of the logger-from-context convention that lets
// non-server shapes (CLI commands, standalone binaries) share the server
// logger without a global or an extra parameter.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(loggerCtxKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}
