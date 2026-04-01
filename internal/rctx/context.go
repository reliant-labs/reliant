// Copyright (c) 2025 Reliant Labs
package rctx

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
)

// WorktreeInfo contains worktree-related context
type WorktreeInfo struct {
	ID   string
	Path string
}

// ToolContext is a minimal context for tool execution in V2
// It provides only what tools actually need without requiring V1 sessions
type ToolContext struct {
	context.Context // Standard context for cancellation, etc.

	// Identifiers
	ChatID    string // Chat this tool execution belongs to
	MessageID string // Message that triggered this tool call (optional)
	Thread    string // Thread path for this execution (UUID matching workflow ID)

	// Execution context
	Project  *db.Project   // V2 Project for file operations
	Worktree *WorktreeInfo // Worktree for working directory

	// Daemon provides filesystem and execution primitives on the user's machine.
	// Nil when no daemon is available (tools that need it should check and return an error).
	Daemon daemon.Client
}

// NewToolContext creates a context for V2 tool execution
// This doesn't require a session since V2 uses threads/contexts instead
func NewToolContext(ctx context.Context, chatID, thread string, proj *db.Project, worktree *WorktreeInfo) *ToolContext {
	return &ToolContext{
		Context:  ctx,
		ChatID:   chatID,
		Project:  proj,
		Worktree: worktree,
		Thread:   thread,
	}
}

// WithDaemon returns a copy of the ToolContext with the given daemon client.
func (tc *ToolContext) WithDaemon(d daemon.Client) *ToolContext {
	newTC := *tc
	newTC.Daemon = d
	return &newTC
}

// WithMessageID adds message context to a ToolContext
func (tc *ToolContext) WithMessageID(messageID string) *ToolContext {
	newTC := *tc
	newTC.MessageID = messageID
	return &newTC
}

// WorkingDir returns the working directory for file operations
func (tc *ToolContext) WorkingDir() string {
	if tc.Worktree != nil {
		return tc.Worktree.Path
	}
	if tc.Project != nil {
		return tc.Project.Path
	}
	return ""
}

// WithCancel returns a copy with a new Done channel
func (c *ToolContext) WithCancel() (*ToolContext, context.CancelFunc) {
	ctx, cancel := context.WithCancel(c.Context)
	newC := *c // Copy all fields
	newC.Context = ctx
	return &newC, cancel
}

// WithTimeout returns a copy with a timeout
func (c *ToolContext) WithTimeout(timeout time.Duration) (*ToolContext, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(c.Context, timeout)
	newC := *c // Copy all fields
	newC.Context = ctx
	return &newC, cancel
}

// WithDeadline returns a copy with a deadline
func (c *ToolContext) WithDeadline(deadline time.Time) (*ToolContext, context.CancelFunc) {
	ctx, cancel := context.WithDeadline(c.Context, deadline)
	newC := *c // Copy all fields
	newC.Context = ctx
	return &newC, cancel
}

// WithValue returns a copy with an additional value
// This is for compatibility but should be avoided in favor of structured fields
func (c *ToolContext) WithValue(key, val interface{}) *ToolContext {
	newC := *c // Copy all fields
	newC.Context = context.WithValue(c.Context, key, val)
	return &newC
}

// HasProject returns true if a project is set in the context
func (c *ToolContext) HasProject() bool {
	return c.Project != nil
}

// Clone creates a shallow copy of the context
func (c *ToolContext) Clone() *ToolContext {
	newC := *c
	return &newC
}

// WithContext returns a new rctx.Context with the underlying context replaced
func (c *ToolContext) WithContext(ctx context.Context) *ToolContext {
	newC := *c
	newC.Context = ctx
	return &newC
}

// SIMPLIFIED CANCELLATION DESIGN:
// 1. Most operations (LLM calls, tools, sub-agents) respect cancellation immediately
// 2. Only critical database operations get a grace period to complete
// 3. No complex context switching - everything uses the main context

// IsCancelled checks if the context has been cancelled
// All operations should check this and stop immediately
func (c *ToolContext) IsCancelled() bool {
	return c.Err() != nil
}

type Worktree struct {
	ID   string
	Path string
}
