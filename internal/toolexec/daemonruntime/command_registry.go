// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"sync"

	"github.com/reliant-labs/reliant/internal/version"
)

// CommandHandler processes a daemon command and returns a JSON-encoded response payload.
type CommandHandler func(ctx context.Context, payload []byte) ([]byte, error)

// CommandRegistry manages registered daemon command handlers.
// New commands only need to call Register to be available — no proto or routing changes required.
type CommandRegistry struct {
	mu       sync.RWMutex
	handlers map[string]CommandHandler
}

// NewCommandRegistry creates an empty command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{
		handlers: make(map[string]CommandHandler),
	}
}

// Register adds a handler for the given command type.
func (r *CommandRegistry) Register(commandType string, handler CommandHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[commandType] = handler
}

// Handle dispatches a command to the registered handler.
func (r *CommandRegistry) Handle(ctx context.Context, commandType string, payload []byte) ([]byte, error) {
	r.mu.RLock()
	handler, ok := r.handlers[commandType]
	r.mu.RUnlock()
	if !ok {
		return nil, unknownCommandError(commandType)
	}
	return handler(ctx, payload)
}

// unknownCommandError explains a registry miss as what it always is: version
// skew.
//
// Every command type is registered from an init() in this package, so the set a
// daemon serves is fixed at build time. The server only ever asks for a command
// it knows about, which means a miss says the server is newer than this daemon
// — never that something is broken, and never anything the user can fix by
// retrying.
//
// The old text was the raw lookup failure:
//
//	unknown daemon command type: "auth.open_oauth_helper"
//
// which reached a user in production as an opaque `{"code":"internal"}` blob
// after their daemon predated the PR that added that handler. It named the
// symptom and buried the one thing they could act on. This leads with the cause
// and the fix, and keeps the command name — the detail that makes a bug report
// actionable — at the end.
//
// The version is read locally rather than plumbed through the wire protocol:
// the daemon is the process that knows which build it is, and the error travels
// back to the server as a plain string, so stating it here needs no proto
// change and cannot disagree with the binary actually running.
func unknownCommandError(commandType string) error {
	return fmt.Errorf(
		"this machine is running an older version of Reliant (%s) that doesn't support %q — update it to continue",
		version.Get().Version, commandType)
}

// defaultRegistry is the global command registry for the daemon runtime.
var defaultRegistry = NewCommandRegistry()

// RegisterCommand registers a command handler in the default registry.
// Call this from init() functions or during daemon startup.
func RegisterCommand(commandType string, handler CommandHandler) {
	defaultRegistry.Register(commandType, handler)
}

// DefaultRegistry returns the default global command registry.
func DefaultRegistry() *CommandRegistry {
	return defaultRegistry
}
