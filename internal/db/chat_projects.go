// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// ListChatProjects returns the projects attached to a chat beside its
// primary, oldest attachment first.
func (r *Repo) ListChatProjects(ctx context.Context, chatID string) ([]*core.ChatProject, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.ListChatProjectsForChat(ctx, r.ORM(), chatID)
}

// AttachChatProject records cp, unless the chat has it attached already
// (then nothing changes). A chat with core.MaxChatProjects attachments is
// refused with core.ErrTooManyChatProjects. The count and the insert run
// under the chat's attachment lock, so two attaches cannot both pass the cap.
func (r *Repo) AttachChatProject(ctx context.Context, cp *core.ChatProject) error {
	if cp == nil || cp.ChatID == "" || cp.ProjectID == "" {
		return fmt.Errorf("chat ID and project ID are required")
	}
	// READ COMMITTED: the advisory lock, not the snapshot, makes the count
	// true at the insert.
	return r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := store.LockChatProjects(ctx, r.ORM(), cp.ChatID); err != nil {
			return err
		}
		existing, err := store.ListChatProjectsForChat(ctx, r.ORM(), cp.ChatID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.ProjectID == cp.ProjectID {
				return nil
			}
		}
		if len(existing) >= core.MaxChatProjects {
			return core.ErrTooManyChatProjects
		}
		return store.InsertChatProject(ctx, r.ORM(), cp)
	})
}

// DetachChatProject removes an attachment; one that is not there is not an
// error.
func (r *Repo) DetachChatProject(ctx context.Context, chatID, projectID string) error {
	return store.DeleteChatProject(ctx, r.ORM(), chatID, projectID)
}

// CopyChatProjects gives toChatID fromChatID's attachments (a branch of a
// chat works in the same projects).
func (r *Repo) CopyChatProjects(ctx context.Context, fromChatID, toChatID string) error {
	return store.CopyChatProjects(ctx, r.ORM(), fromChatID, toChatID)
}
