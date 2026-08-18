// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonruntime

import "context"

// backgroundRequestKey carries the per-execution "detach into a background
// process" probe down to a command handler.
//
// The handler signature is (ctx, payload) — deliberately narrow — so the
// request id and the daemon client that owns the background registry cannot be
// passed as arguments without changing every handler. A context value keeps the
// seam to the one handler that needs it (exec.run) while leaving the rest
// untouched.
type backgroundRequestKeyType struct{}

var backgroundRequestKey backgroundRequestKeyType

// BackgroundProbe reports whether the user has asked to detach THIS execution,
// returning the LLM tool-call id to attribute the resulting process to. It
// consumes the request, so it fires at most once per execution.
type BackgroundProbe func() (toolCallID string, requested bool)

// WithBackgroundProbe attaches the probe for one command execution.
func WithBackgroundProbe(ctx context.Context, probe BackgroundProbe) context.Context {
	if probe == nil {
		return ctx
	}
	return context.WithValue(ctx, backgroundRequestKey, probe)
}

// backgroundRequested reports whether a background detach was asked for. It is
// false whenever no probe is installed, which is the case for every non-stream
// caller (local executor, tests) — those simply never background.
func backgroundRequested(ctx context.Context) (string, bool) {
	probe, ok := ctx.Value(backgroundRequestKey).(BackgroundProbe)
	if !ok || probe == nil {
		return "", false
	}
	return probe()
}
