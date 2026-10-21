// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/store"
)

// GetChatSelection returns the chat's selection, nil with no error when it has
// none.
func (r *Repo) GetChatSelection(ctx context.Context, chatID string) (*chatselection.Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	return store.GetChatSelection(ctx, r.ORM(), chatID, false)
}

// MutateChatSelection is chatselection.Store.MutateChatSelection: one writer of
// a chat's record at a time, so a read-modify-write cannot lose an edit. The
// advisory lock covers the first write too, when there is no row to lock.
func (r *Repo) MutateChatSelection(ctx context.Context, chatID string, mutate func(current *chatselection.Selection) (*chatselection.Selection, error)) (*chatselection.Selection, error) {
	if chatID == "" {
		return nil, fmt.Errorf("chat ID cannot be empty")
	}
	var out *chatselection.Selection
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		if err := store.LockChatSelection(txCtx, r.ORM(), chatID); err != nil {
			return err
		}
		cur, err := store.GetChatSelection(txCtx, r.ORM(), chatID, true)
		if err != nil {
			return err
		}
		next, err := mutate(cur)
		if err != nil {
			return err
		}
		if next == nil || next == cur {
			out = cur
			return nil
		}
		out, err = store.UpsertChatSelection(txCtx, r.ORM(), chatID, next, time.Now().UTC())
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CopyChatSelection is chatselection.Store.CopyChatSelection.
func (r *Repo) CopyChatSelection(ctx context.Context, fromChatID, toChatID, userID string) error {
	if fromChatID == "" || toChatID == "" {
		return fmt.Errorf("chat IDs cannot be empty")
	}
	return store.CopyChatSelection(ctx, r.ORM(), fromChatID, toChatID, userID, time.Now().UTC())
}
