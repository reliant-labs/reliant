// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"strings"
)

// IsPreemptable reports whether a call to the named tool may be cut short when
// a message arrives for the calling thread (ToolDefinition.Preemptable).
// Unknown and MCP tools are not: a preemptable tool promises that ending a
// call early loses nothing, and nobody has made that promise for them.
func IsPreemptable(name string) bool {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, MCPToolPrefix) {
		return false
	}
	for _, def := range GetToolRegistry() {
		if def.Name == name {
			return def.Preemptable
		}
	}
	return false
}

type preemptionContextKey struct{}

// WithPreemption attaches the channel execute_tools closes when a message
// arrives for the thread a preemptable call is running on.
//
// It is a signal, not a cancellation, because the executor must deliver it
// differently by placement. A call on this process is cut short by cancelling
// its context. A call on the daemon is told to stop with a cancel push, and the
// executor then keeps waiting for the tool's own answer — cancelling the wait
// instead would throw away the very result preemption exists to return early.
func WithPreemption(ctx context.Context, preempt <-chan struct{}) context.Context {
	if preempt == nil {
		return ctx
	}
	return context.WithValue(ctx, preemptionContextKey{}, preempt)
}

// PreemptionFrom returns the channel WithPreemption attached, or nil when the
// call is not preemptable. A nil channel never fires, so a select on it is safe.
func PreemptionFrom(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	preempt, _ := ctx.Value(preemptionContextKey{}).(<-chan struct{})
	return preempt
}
