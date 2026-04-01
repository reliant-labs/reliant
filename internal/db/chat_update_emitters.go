package db

import (
	"context"
	"fmt"
)

// ============================================================================
// TYPED CHAT UPDATE EMITTERS
// ============================================================================
// These methods provide type-safe emission of chat updates, replacing manual
// map[string]interface{} construction throughout the codebase.
//
// Each emitter:
// 1. Sets the update_type field automatically
// 2. Generates a consistent entity ID
// 3. Marshals to JSON
// 4. Calls CreateChatUpdate
// ============================================================================

// EmitToolCallUpdate emits a tool call status update to chat_updates
func (r *Repo) EmitToolCallUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error {
	update.UpdateType = UpdateTypeToolCall
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal tool call update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeToolCall, EntityIDForToolCall(update.ContentBlockID), data)
}

// EmitToolCallCancelledUpdate emits a tool call cancelled update
func (r *Repo) EmitToolCallCancelledUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error {
	update.UpdateType = UpdateTypeToolCall
	update.Status = ToolCallStatusCancelled
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal tool call cancelled update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeToolCall, EntityIDForToolCancelled(update.ToolCallID), data)
}

// EmitToolCallBackgroundedUpdate emits a tool call backgrounded update
func (r *Repo) EmitToolCallBackgroundedUpdate(ctx context.Context, chatID string, update ToolCallUpdate) error {
	update.UpdateType = UpdateTypeToolCall
	update.Status = ToolCallStatusBackgrounded
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal tool call backgrounded update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeToolCall, EntityIDForToolBackgrounded(update.ToolCallID), data)
}

// EmitYieldUpdate emits a yield state update to chat_updates
func (r *Repo) EmitYieldUpdate(ctx context.Context, chatID string, update YieldUpdate) error {
	update.UpdateType = UpdateTypeYield
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal yield update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeYield, EntityIDForYield(update.YieldID), data)
}

// EmitSkillInvocationUpdate emits a skill invocation lifecycle update
func (r *Repo) EmitSkillInvocationUpdate(ctx context.Context, chatID string, update SkillInvocationUpdate) error {
	update.UpdateType = UpdateTypeSkillInvocation
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal skill invocation update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeSkillInvocation, EntityIDForSkillInvocation(update.ID), data)
}
