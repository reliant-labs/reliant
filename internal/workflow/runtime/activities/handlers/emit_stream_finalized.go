// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// ============================================================================
// TYPES
// ============================================================================

// EmitStreamFinalizedInput is the input for the EmitStreamFinalized activity.
//
// A type ALIAS, not a defined type: the workflow dispatches this activity
// locally, and local-activity arguments reach the registered function by
// reflection rather than through the data converter, so the value the workflow
// passes must be the very same reflect.Type this function declares. See
// types/mailbox.go.
type EmitStreamFinalizedInput = types.EmitStreamFinalizedInput

// EmitStreamFinalizedOutput is the output for the EmitStreamFinalized activity.
type EmitStreamFinalizedOutput = types.EmitStreamFinalizedOutput

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
