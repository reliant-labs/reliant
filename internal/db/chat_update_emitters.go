// forge:exclude-contract
//
// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
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
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeToolCall, EntityIDForToolCall(update.ToolCallID), data)
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

// EmitQuestionUpdate emits a question status update to chat_updates
func (r *Repo) EmitQuestionUpdate(ctx context.Context, chatID string, update QuestionUpdate) error {
	update.UpdateType = UpdateTypeQuestion
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal question update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeQuestion, EntityIDForQuestion(update.QuestionID), data)
}

// EmitStreamFinalizedUpdate emits a stream_finalized marker for a
// pre-allocated assistant message id (delta identity protocol). entity_id is
// the message id itself so snapshot dedup keeps exactly one marker per message.
func (r *Repo) EmitStreamFinalizedUpdate(ctx context.Context, chatID string, update StreamFinalizedUpdate) error {
	update.UpdateTypeName = "stream_finalized"
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal stream finalized update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeStreamFinalized, update.MessageID, data)
}

// EmitAgentMessagesDrainedUpdate announces the mailbox rows a drain just
// turned into transcript messages, so the pending-queue strip can drop them in
// the same commit those messages appear in rather than waiting for its next
// poll.
//
// Callers MUST pass the transaction context the drain's saves run in. Emitted
// outside it, this would be visible to a reader before the messages it
// announces, and the strip would clear against a transcript that has not shown
// them yet -- the same message missing from both places, which is worse than
// showing it in both.
//
// entity_id is unique per drain (not per thread): every drain is a distinct
// event naming a distinct set of ids, so collapsing two of them under one
// entity in the snapshot dedup would lose the older one's ids.
func (r *Repo) EmitAgentMessagesDrainedUpdate(ctx context.Context, chatID string, update AgentMessagesDrainedUpdate) error {
	update.UpdateTypeName = "agent_messages_drained"
	data, err := MarshalUpdate(update)
	if err != nil {
		return fmt.Errorf("failed to marshal agent messages drained update: %w", err)
	}
	return r.CreateChatUpdate(ctx, chatID, UpdateTypeAgentMessagesDrained,
		EntityIDForAgentMessagesDrained(update.Thread), data)
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
