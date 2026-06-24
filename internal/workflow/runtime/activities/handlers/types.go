// Copyright (c) 2025 Reliant Labs
package handlers

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// =============================================================================
// ACTIVITY INPUT (proto-based, non-generic)
// =============================================================================

// ActivityInput is defined in activities/types to avoid import cycles.
// Both the runtime engine and handlers import it from there.
type ActivityInput = activitytypes.ActivityInput

// =============================================================================
// COMMON OUTPUT TYPES
// =============================================================================

// RuntimeContext is the runtime execution context passed into activities.
type RuntimeContext = activitytypes.RuntimeContext

// SpawnConfig aliases activity SpawnConfig for handler-local usage.
type SpawnConfig = activitytypes.SpawnConfig

// ToolCall is a handler-level alias to the canonical message ToolCall type.
type ToolCall = message.ToolCall

// ToolResult is a handler-level alias to the canonical message ToolResult type.
type ToolResult = message.ToolResult

// MessageOutput is the standardized output for activities that produce messages.
type MessageOutput = reliantv1.MessageOutput

// ThinkingOutput contains extended thinking content from the LLM.
type ThinkingOutput = reliantv1.ThinkingOutput

// =============================================================================
// ACTIVITY OUTPUT TYPES
// =============================================================================

// CallLLMOutput is the output from call_llm nodes.
type CallLLMOutput = reliantv1.CallLLMOutput

// ExecuteToolsOutput is the output from execute_tools nodes.
type ExecuteToolsOutput = reliantv1.ExecuteToolsOutput

// CompactOutput is the output from the compact activity.
type CompactOutput = reliantv1.CompactOutput

// ApprovalOutput is the output from the approval activity.
type ApprovalOutput = reliantv1.ApprovalOutput

// SaveMessageOutput is the output from the save_message activity.
type SaveMessageOutput = reliantv1.SaveMessageOutput

// CreateWorktreeOutput is the output from the create_worktree activity.
type CreateWorktreeOutput = reliantv1.CreateWorktreeOutput

// DeleteWorktreeOutput is the output from the delete_worktree activity.
type DeleteWorktreeOutput = reliantv1.DeleteWorktreeOutput

// =============================================================================
// THINKING LEVEL
// =============================================================================

// ThinkingLevel represents the extended thinking level for LLM calls.
type ThinkingLevel string

const (
	ThinkingLevelLow    ThinkingLevel = "low"
	ThinkingLevelMedium ThinkingLevel = "medium"
	ThinkingLevelHigh   ThinkingLevel = "high"
)

// IsValid returns true if the thinking level is a recognized value.
func (tl ThinkingLevel) IsValid() bool {
	switch tl {
	case ThinkingLevelLow, ThinkingLevelMedium, ThinkingLevelHigh, "xhigh":
		return true
	}
	return false
}

// =============================================================================
// RESPONSE TOOL NAMING CONVENTION
// =============================================================================
//
