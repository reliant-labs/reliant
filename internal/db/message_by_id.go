// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/store"
)

// FindMessage retrieves a message by id, returning (nil, nil) when no such row
// exists.
//
// GetMessage treats a miss as an error, which is right for callers that are
// dereferencing a known id. This variant exists for the idempotency checks in
// SaveMessage, which ask "is this id already taken?" and must be able to hear
// "no" without unwrapping an error string. It mirrors GetMessageByActivityID's
// contract deliberately, so both idempotency lookups read the same way.
func (r *Repo) FindMessage(ctx context.Context, id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("message ID cannot be empty")
	}
	msg, err := store.FindMessage(ctx, r.ORM(), id)
	if err != nil {
		return nil, fmt.Errorf("failed to find message by ID: %w", err)
	}
	return msg, nil
}

// IsLatestTurnInThread reports whether messageID is the newest turn of the
// thread: its newest message that is not a system note. Notes the server posts
// about a run are not turns, so they never make an unanswered message look
// answered (the same rule as ThreadAwaitsReply).
//
// A user message that is not the latest turn has been followed by one, so a run
// has already read it. A false answer for an id that is not in the thread at
// all is also correct: nothing there is waiting to be read.
func (r *Repo) IsLatestTurnInThread(ctx context.Context, threadID, messageID string) (bool, error) {
	if threadID == "" {
		return false, fmt.Errorf("thread ID cannot be empty")
	}
	if messageID == "" {
		return false, fmt.Errorf("message ID cannot be empty")
	}
	turn, err := store.LatestTurnInThread(ctx, r.ORM(), threadID)
	if err != nil {
		return false, fmt.Errorf("failed to read the latest turn: %w", err)
	}
	return turn != nil && turn.ID == messageID, nil
}
