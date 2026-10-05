// Copyright (c) 2025 Reliant Labs

// Package nomachine carries the "this run has no machine" fact through a
// context, so the transport layers below an activity — the tool executor, the
// daemon router, the MCP binder — can refuse to reach a daemon without each
// one being told about chats or workflows.
//
// The fact itself lives on the chat row (chats.no_machine, set at launch; see
// research/DAEMONLESS_RUNS.md). An activity that loaded a no-machine chat marks
// its context with With, and everything it calls checks Is. This is the last of
// several layers: the tool menu never offers a machine tool and execution
// refuses one before dispatch, so a marked context reaching the router means a
// layer above was bypassed — and the router must still not resolve, and so must
// never wake, a daemon.
package nomachine

import (
	"context"
	"errors"
)

type contextKey struct{}

// With marks ctx as belonging to a run that has no machine.
func With(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, true)
}

// Is reports whether ctx belongs to a run that has no machine.
func Is(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(contextKey{}).(bool)
	return marked
}

// ErrNoMachine is what a transport returns for a marked context. Its text
// deliberately does NOT contain daemonoffline.ErrorSubstring ("no daemon
// connected"): the offline circuit breaker counts that substring, and a run
// with no machine by design is not a run whose machine went offline.
var ErrNoMachine = errors.New("this run has no machine, so nothing can run on the user's computer")

// Refusal is the text a no-machine run gets back for a tool it cannot use. The
// tool menu does not offer such tools, so the model only sees this when it
// names one anyway (from history, or a hallucination). Same constraint as
// ErrNoMachine: it must not contain "no daemon connected".
func Refusal(toolName string) string {
	return "Tool '" + toolName + "' needs the user's computer, and this run has no machine: it runs on " +
		"Reliant's servers only, with no access to files, a shell, a git checkout or local MCP servers. " +
		"Do not retry it. Continue with the tools you were given (web, integrations, planning), or report " +
		"that this part of the task needs a machine."
}
