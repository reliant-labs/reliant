// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"sync"
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
		return nil, fmt.Errorf("unknown daemon command type: %q", commandType)
	}
	return handler(ctx, payload)
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
