// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// ownerForChat resolves the user a run belongs to, so the run can record its
// own identity instead of borrowing the chat's forever.
//
// Today the answer always comes from the chat, because every run has one. That
// is exactly the coupling being removed: a triggered or API-started run has no
// conversation to read an owner off. Recording it on the run at creation is
// what lets the read sites stop looking at the chat later, without a second
// backfill over rows written in the meantime.
//
// Returns nil when the owner cannot be determined — a chat that is missing, or
// a run genuinely created without one. nil is written as SQL NULL and is a
// normal value for this column, not an error: the readers still fall back to
// the chat while both sources exist, so an unknown owner costs nothing today
// and is honest about what we know.
func ownerForChat(ctx context.Context, repo db.Repository, chatID string) *string {
	if repo == nil || chatID == "" {
		return nil
	}
	chat, err := repo.GetChat(ctx, chatID)
	if err != nil || chat == nil || chat.UserID == "" {
		return nil
	}
	owner := chat.UserID
	return &owner
}
