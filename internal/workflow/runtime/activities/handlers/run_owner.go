// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// resolveRunOwner determines the user a run belongs to, so the run records its
// own identity instead of borrowing the chat's forever.
//
// Today the answer comes from the chat, because every run has one. That is
// exactly the coupling being removed: a triggered or API-started run has no
// conversation to read an owner off. Recording it at creation is what lets the
// read sites stop looking at the chat, with no second backfill over rows
// written in the meantime.
//
// The PARENT RUN IS TRIED FIRST for a spawned run. A child's owner is its
// parent's by construction — there is no case where they differ — and asking
// the parent is both cheaper than a chat lookup and correct for a child whose
// parent has no chat at all, which is the shape this work exists to allow.
//
// nil means the owner could not be determined, and callers must treat that as
// a fact worth acting on rather than a value to paper over: a run with no owner
// is a run the engine cannot attribute, and it should be visible.
func resolveRunOwner(ctx context.Context, repo db.Repository, parentWorkflowID *string, chatID string) *string {
	if repo == nil {
		return nil
	}

	if parentWorkflowID != nil && *parentWorkflowID != "" {
		if parent, err := repo.GetWorkflow(ctx, *parentWorkflowID); err == nil && parent != nil {
			if parent.OwnerUserID != nil && *parent.OwnerUserID != "" {
				owner := *parent.OwnerUserID
				return &owner
			}
		}
	}

	if chatID == "" {
		return nil
	}
	chat, err := repo.GetChat(ctx, chatID)
	if err != nil || chat == nil || chat.UserID == "" {
		return nil
	}
	owner := chat.UserID
	return &owner
}
