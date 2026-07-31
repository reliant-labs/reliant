// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// TYPES
// ============================================================================

// EmitStreamFinalizedInput is the input for the EmitStreamFinalized activity.
// Field names mirror the map built in runtime.emitStreamFinalized.
type EmitStreamFinalizedInput struct {
	ChatID        string `json:"chat_id" reliant:"-"`
	MessageID     string `json:"message_id"`
	Thread        string `json:"thread,omitempty"`
	Reason        string `json:"reason"` // "completed", "aborted", or "cancelled"
	LastStreamSeq int64  `json:"last_stream_seq,omitempty"`
}

// EmitStreamFinalizedOutput is the output for the EmitStreamFinalized activity.
type EmitStreamFinalizedOutput struct {
	Success bool `json:"success"`
}

// ============================================================================
// ACTIVITY IMPLEMENTATION
// ============================================================================

// EmitStreamFinalizedActivity writes the stream_finalized chat update for a
// pre-allocated assistant message id (delta identity protocol). Fired by the
// workflow whenever a message stream reaches a terminal state, regardless of
// whether the corresponding SaveMessage succeeded — the marker's contract is
// "this id will produce no more deltas", not "this message was persisted".
type EmitStreamFinalizedActivity struct {
	repo db.Repository
}

// NewEmitStreamFinalizedActivity creates a new EmitStreamFinalizedActivity.
func NewEmitStreamFinalizedActivity(repo db.Repository) *EmitStreamFinalizedActivity {
	return &EmitStreamFinalizedActivity{repo: repo}
}

// Name returns the activity name for registration.
func (a *EmitStreamFinalizedActivity) Name() string {
	return "EmitStreamFinalized"
}

// Execute writes the stream_finalized marker to chat_updates.
func (a *EmitStreamFinalizedActivity) Execute(ctx context.Context, input EmitStreamFinalizedInput) (EmitStreamFinalizedOutput, error) {
	if input.ChatID == "" || input.MessageID == "" {
		return EmitStreamFinalizedOutput{}, fmt.Errorf("chat_id and message_id are required")
	}
	reason := db.StreamFinalizedReason(input.Reason)
	switch reason {
	case db.StreamFinalizedCompleted, db.StreamFinalizedAborted, db.StreamFinalizedCancelled:
	default:
		return EmitStreamFinalizedOutput{}, fmt.Errorf("invalid stream_finalized reason %q", input.Reason)
	}

	err := a.repo.EmitStreamFinalizedUpdate(ctx, input.ChatID, db.StreamFinalizedUpdate{
		MessageID:     input.MessageID,
		Thread:        input.Thread,
		Reason:        reason,
		LastStreamSeq: input.LastStreamSeq,
	})
	if err != nil {
		return EmitStreamFinalizedOutput{}, fmt.Errorf("failed to emit stream_finalized update: %w", err)
	}
	return EmitStreamFinalizedOutput{Success: true}, nil
}
