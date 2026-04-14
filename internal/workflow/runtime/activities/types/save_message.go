// Copyright (c) 2025 Reliant Labs
package types

import (
	"github.com/reliant-labs/reliant/internal/models/message"
)

// ThinkingOutput contains extended thinking content from the LLM.
type ThinkingOutput struct {
	Content   string `json:"content"`
	Signature string `json:"signature"`
}

// SaveMessageInput is the input for SaveMessage activity.
// This is the canonical definition shared between:
//   - workflow/runtime (activity invocation)
//   - workflow/runtime/activities/handlers (activity implementation)
//
// Placing it in a separate package avoids circular imports.
// NOTE: This flat struct is still used by the v2 engine as an intermediate
// representation when building SaveMessage activity inputs via CEL evaluation.
// It gets converted to an ActivityInput with SaveMessageNodeArgs at runtime.
type SaveMessageInput struct {
	ChatID          string               `json:"chat_id" reliant:"-"`
	Thread          string               `json:"thread" reliant:"-"` // Auto-injected from workflow execution context
	StepID          string               `json:"step_id,omitempty" reliant:"-"`
	Role            string               `json:"role" reliant:"desc=Message role in conversation. Use display_style=hidden to hide from UI.,enum=user|assistant|tool|system"`
	DisplayStyle    string               `json:"display_style,omitempty" reliant:"desc=UI display style hint. hidden=not shown in UI but sent to LLM.,enum=info|warning|success|hidden"`
	Content         string               `json:"content,omitempty" reliant:"desc=Text content of the message,ui=textarea"`
	Attachments     []string             `json:"attachments,omitempty" reliant:"-"`
	ToolResults     []message.ToolResult `json:"tool_results,omitempty" reliant:"desc=Tool execution results (required for tool role messages)"`
	ToolCalls       []message.ToolCall   `json:"tool_calls,omitempty" reliant:"desc=Tool calls from LLM response (for assistant messages)"`
	TokenCount      int                  `json:"token_count,omitempty" reliant:"-"`       // Total tokens (prompt + response + context)
	Cost            float64              `json:"cost,omitempty" reliant:"-"`              // Request cost in USD returned by the LLM provider
	ContextWindowID string               `json:"context_window_id,omitempty" reliant:"-"` // FK to context_windows (nullable during migration)
	WorkflowID      string               `json:"workflow_id,omitempty" reliant:"-"`
	LoopNodeID      string               `json:"loop_node_id,omitempty" reliant:"-"`
	LoopIteration   int                  `json:"loop_iteration" reliant:"-"`

	// Extended thinking support - contains both content and signature
	Thinking ThinkingOutput `json:"thinking,omitempty" reliant:"desc=Extended thinking content and signature (for multi-turn thinking preservation)"`

	// InjectFiles carries binary file data for inject file attachments.
	// These are loaded eagerly at inject construction time and stored as DB attachments
	// at save time, then referenced as IMAGE or DOCUMENT content blocks.
	InjectFiles []InjectFileData `json:"inject_files,omitempty" reliant:"-"`
}

// InjectFileData holds binary file data for injection.
type InjectFileData struct {
	Filename string `json:"filename"`
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}
