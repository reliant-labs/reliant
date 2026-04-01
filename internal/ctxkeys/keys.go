// Copyright (c) 2025 Reliant Labs
package ctxkeys

// ContextKey is the type for context keys
type ContextKey string

// ToolCallContext holds tool call tracking information
type ToolCallContext struct {
	// ParentToolCallID is the database record ID for parent tool call (for sub-agents)
	ParentToolCallID string
	// CurrentToolCallID is the actual tool call ID from the LLM (e.g., toolu_xxx)
	CurrentToolCallID string
}

const (
	// UserIDKey is the context key for the user ID
	UserIDKey ContextKey = "user_id"

	// SessionTemperatureKey is the context key for session-specific temperature override
	SessionTemperatureKey ContextKey = "session_temperature"

	// SessionThinkingModeKey is the context key for session-specific thinking mode override
	SessionThinkingModeKey ContextKey = "session_thinking_mode"

	// SessionModelKey is the context key for a per-session model override
	// When set, the engine/agent should prefer this model over the agent's
	// YAML-configured default, falling back to the YAML preference if unset.
	SessionModelKey ContextKey = "session_model"

	// StreamingEnabledKey controls whether the LLM response should use
	// streaming events (true) or a single non-streaming completion (false).
	// Default is false (non-streaming).
	StreamingEnabledKey ContextKey = "streaming_enabled"

	// ToolCallContextKey is the context key for tool call tracking information
	// This consolidates both parent and current tool call IDs
	ToolCallContextKey ContextKey = "tool_call_context"
)
