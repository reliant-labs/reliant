// Copyright (c) 2025 Reliant Labs
package chataccess

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type service struct {
	store Store
}

func (s *service) GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*core.ChatRow, error) {
	if err := requireIDs(id, userID); err != nil {
		return nil, err
	}
	// The store read filters on the owner; authz confirms the row it returns
	// as a write, the strict question, since these reads back both reads and
	// changes.
	chat, err := s.store.GetChatRowWithUserCheck(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if chat == nil || !authz.CanWriteChat(ctx, userID, chat) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}
	return chat, nil
}

func (s *service) GetChatWithUserCheck(ctx context.Context, id, userID string) (*core.Chat, error) {
	if err := requireIDs(id, userID); err != nil {
		return nil, err
	}
	chat, err := s.store.GetChatWithUserCheck(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if chat == nil || !authz.CanWriteChat(ctx, userID, chat) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, id)
	}
	return chat, nil
}

func requireIDs(id, userID string) error {
	if id == "" {
		return fmt.Errorf("chat ID cannot be empty")
	}
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	return nil
}
